/*
Manage a deduplicated versionned backups repository.

Sample repository:

```
repo/
├── 00000/
│   ├── chunks/
│   │   ├── 000000000000000
│   │   ├── 000000000000001
│   │   ├── 000000000000002
│   │   ├── 000000000000003
│   ├── files
│   ├── fingerprints
│   ├── recipe
│   └── sketches
└── 00001/
    ├── chunks/
    │   ├── 000000000000000
    │   ├── 000000000000001
    ├── files
│   ├── fingerprints
│   ├── recipe
│   └── sketches
```
*/

package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/chmduquesne/rollinghash/rabinkarp64"
	"github.com/n-peugnet/dna-backup/cache"
	"github.com/n-peugnet/dna-backup/logger"
	"github.com/n-peugnet/dna-backup/sketch"
	"github.com/n-peugnet/dna-backup/utils"
)

type FingerprintMap map[uint64]*ChunkId
type SketchMap map[uint64][]*ChunkId

func (m SketchMap) Set(key []uint64, value *ChunkId) {
	for _, s := range key {
		prev := m[s]
		if contains(prev, value) {
			continue
		}
		m[s] = append(prev, value)
	}
}

type Repo struct {
	path              string
	chunkSize         int
	sketchWSize       int
	sketchSfCount     int
	sketchFCount      int
	pol               rabinkarp64.Pol
	differ            Differ
	patcher           Patcher
	fingerprints      FingerprintMap
	sketches          SketchMap
	chunkCache        cache.Cacher
	chunkReadWrapper  func(r io.Reader) (io.ReadCloser, error)
	chunkWriteWrapper func(w io.Writer) io.WriteCloser
}

type chunkHashes struct {
	Fp uint64
	Sk []uint64
}

type chunkData struct {
	hashes  chunkHashes
	content []byte
	id      *ChunkId
}

type File struct {
	Path string
	Size int64
}

func NewRepo(path string) *Repo {
	err := os.MkdirAll(path, 0775)
	if err != nil {
		logger.Panic(err)
	}
	var seed int64 = 1
	p, err := rabinkarp64.RandomPolynomial(seed)
	if err != nil {
		logger.Panic(err)
	}
	return &Repo{
		path:              path,
		chunkSize:         8 << 10,
		sketchWSize:       32,
		sketchSfCount:     3,
		sketchFCount:      4,
		pol:               p,
		differ:            &Bsdiff{},
		patcher:           &Bsdiff{},
		fingerprints:      make(FingerprintMap),
		sketches:          make(SketchMap),
		chunkCache:        cache.NewFifoCache(10000),
		chunkReadWrapper:  utils.ZlibReader,
		chunkWriteWrapper: utils.ZlibWriter,
	}
}

func (r *Repo) Differ() Differ {
	return r.differ
}

func (r *Repo) Patcher() Patcher {
	return r.patcher
}

func (r *Repo) Commit(source string) {
	source = utils.TrimTrailingSeparator(source)
	versions := r.loadVersions()
	newVersion := len(versions) // TODO: add newVersion functino
	newPath := filepath.Join(r.path, fmt.Sprintf(versionFmt, newVersion))
	newChunkPath := filepath.Join(newPath, chunksName)
	newFilesPath := filepath.Join(newPath, filesName)
	newRecipePath := filepath.Join(newPath, recipeName)
	os.Mkdir(newPath, 0775)      // TODO: handle errors
	os.Mkdir(newChunkPath, 0775) // TODO: handle errors
	reader, writer := io.Pipe()
	files := listFiles(source)
	r.loadHashes(versions)
	go concatFiles(files, writer)
	recipe := r.matchStream(reader, newVersion)
	storeFileList(newFilesPath, unprefixFiles(files, source))
	storeRecipe(newRecipePath, recipe)
	logger.Info(files)
}

func (r *Repo) Restore(destination string) {
	versions := r.loadVersions()
	latest := versions[len(versions)-1]
	latestFilesPath := filepath.Join(latest, filesName)
	latestRecipePath := filepath.Join(latest, recipeName)
	files := loadFileList(latestFilesPath)
	recipe := loadRecipe(latestRecipePath)
	reader, writer := io.Pipe()
	go r.restoreStream(writer, recipe)
	bufReader := bufio.NewReaderSize(reader, r.chunkSize*2)
	for _, file := range files {
		filePath := filepath.Join(destination, file.Path)
		dir := filepath.Dir(filePath)
		os.MkdirAll(dir, 0775)      // TODO: handle errors
		f, _ := os.Create(filePath) // TODO: handle errors
		n, err := io.CopyN(f, bufReader, file.Size)
		if err != nil {
			logger.Errorf("storing file content for '%s', written %d/%d bytes: %s", filePath, n, file.Size, err)
		}
		if err := f.Close(); err != nil {
			logger.Errorf("closing restored file '%s': %s", filePath, err)
		}
	}
}

func (r *Repo) loadVersions() []string {
	var versions []string
	files, err := os.ReadDir(r.path)
	if err != nil {
		logger.Fatal(err)
	}
	for _, f := range files {
		if !f.IsDir() {
			continue
		}
		versions = append(versions, filepath.Join(r.path, f.Name()))
	}
	return versions
}

func listFiles(path string) []File {
	var files []File
	err := filepath.Walk(path,
		func(p string, i fs.FileInfo, err error) error {
			if err != nil {
				logger.Error(err)
				return err
			}
			if i.IsDir() {
				return nil
			}
			files = append(files, File{p, i.Size()})
			return nil
		})
	if err != nil {
		logger.Error(err)
	}
	return files
}

func unprefixFiles(files []File, prefix string) (ret []File) {
	ret = make([]File, len(files))
	preSize := len(prefix)
	for i, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			logger.Warning(f.Path, "is not prefixed by", prefix)
		} else {
			f.Path = f.Path[preSize:]
		}
		ret[i] = f
	}
	return
}

func concatFiles(files []File, stream io.WriteCloser) {
	for _, f := range files {
		file, err := os.Open(f.Path)
		if err != nil {
			logger.Error(err)
			continue
		}
		if n, err := io.Copy(stream, file); err != nil {
			logger.Error(n, err)
			continue
		}
		if err = file.Close(); err != nil {
			logger.Panic(err)
		}
	}
	stream.Close()
}

func storeBasicStruct(dest string, obj interface{}) {
	file, err := os.Create(dest)
	if err == nil {
		encoder := gob.NewEncoder(file)
		err = encoder.Encode(obj)
	}
	if err != nil {
		logger.Panic(err)
	}
	if err = file.Close(); err != nil {
		logger.Panic(err)
	}
}

func loadBasicStruct(path string, obj interface{}) {
	file, err := os.Open(path)
	if err == nil {
		decoder := gob.NewDecoder(file)
		err = decoder.Decode(obj)
	}
	if err != nil {
		logger.Panic(err)
	}
	if err = file.Close(); err != nil {
		logger.Panic(err)
	}
}

func storeFileList(dest string, files []File) {
	storeBasicStruct(dest, files)
}

func loadFileList(path string) []File {
	var files []File
	loadBasicStruct(path, &files)
	return files
}

func (r *Repo) storageWorker(version int, storeQueue <-chan chunkData, end chan<- bool) {
	hashesFile := filepath.Join(r.path, fmt.Sprintf(versionFmt, version), hashesName)
	file, err := os.Create(hashesFile)
	if err != nil {
		logger.Panic(err)
	}
	encoder := gob.NewEncoder(file)
	for data := range storeQueue {
		err = encoder.Encode(data.hashes)
		err := r.StoreChunkContent(data.id, bytes.NewReader(data.content))
		if err != nil {
			logger.Error(err)
		}
		logger.Info("stored", data.id)
	}
	if err = file.Close(); err != nil {
		logger.Panic(err)
	}
	end <- true
}

func (r *Repo) StoreChunkContent(id *ChunkId, reader io.Reader) error {
	path := id.Path(r.path)
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating chunk for '%s'; %s\n", path, err)
	}
	wrapper := r.chunkWriteWrapper(file)
	n, err := io.Copy(wrapper, reader)
	if err != nil {
		return fmt.Errorf("writing chunk content for '%s', written %d bytes: %s\n", path, n, err)
	}
	if err := wrapper.Close(); err != nil {
		return fmt.Errorf("closing write wrapper for '%s': %s\n", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing chunk for '%s': %s\n", path, err)
	}
	return nil
}

// LoadChunkContent loads a chunk from the repo.
// If the chunk is in cache, get it from cache, else read it from drive.
func (r *Repo) LoadChunkContent(id *ChunkId) *bytes.Reader {
	value, exists := r.chunkCache.Get(id)
	if !exists {
		path := id.Path(r.path)
		f, err := os.Open(path)
		if err != nil {
			logger.Errorf("cannot open chunk '%s': %s", path, err)
		}
		wrapper, err := r.chunkReadWrapper(f)
		if err != nil {
			logger.Errorf("cannot create read wrapper for chunk '%s': %s", path, err)
		}
		value, err = io.ReadAll(wrapper)
		if err != nil {
			logger.Panicf("could not read from chunk '%s': %s", path, err)
		}
		if err = f.Close(); err != nil {
			logger.Warningf("could not close chunk '%s': %s", path, err)
		}
		r.chunkCache.Set(id, value)
	}
	return bytes.NewReader(value)
}

// TODO: use atoi for chunkid ?
func (r *Repo) loadChunks(versions []string, chunks chan<- IdentifiedChunk) {
	for i, v := range versions {
		p := filepath.Join(v, chunksName)
		entries, err := os.ReadDir(p)
		if err != nil {
			logger.Errorf("reading version '%05d' in '%s' chunks: %s", i, v, err)
		}
		for j, e := range entries {
			if e.IsDir() {
				continue
			}
			id := &ChunkId{Ver: i, Idx: uint64(j)}
			c := NewStoredChunk(r, id)
			chunks <- c
		}
	}
	close(chunks)
}

func (r *Repo) loadHashes(versions []string) {
	for i, v := range versions {
		path := filepath.Join(v, hashesName)
		file, err := os.Open(path)
		if err == nil {
			decoder := gob.NewDecoder(file)
			for j := 0; err == nil; j++ {
				var h chunkHashes
				if err = decoder.Decode(&h); err == nil {
					id := &ChunkId{i, uint64(j)}
					r.fingerprints[h.Fp] = id
					r.sketches.Set(h.Sk, id)
				}
			}
		}
		if err != nil && err != io.EOF {
			logger.Panic(err)
		}
		if err = file.Close(); err != nil {
			logger.Panic(err)
		}
	}
}

func (r *Repo) chunkMinLen() int {
	return sketch.SuperFeatureSize(r.chunkSize, r.sketchSfCount, r.sketchFCount)
}

// hashChunks calculates the hashes for a channel of chunks.
// For each chunk, both a fingerprint (hash over the full content) and a sketch
// (resemblance hash based on maximal values of regions) are calculated and
// stored in an hashmap.
func (r *Repo) hashChunks(chunks <-chan IdentifiedChunk) {
	for c := range chunks {
		r.hashChunk(c.GetId(), c.Reader())
	}
}

// hashChunk calculates the hashes for a chunk and store them in th repo hashmaps.
func (r *Repo) hashChunk(id *ChunkId, reader io.Reader) (fp uint64, sk []uint64) {
	var buffSk bytes.Buffer
	var buffFp bytes.Buffer
	var wg sync.WaitGroup
	reader = io.TeeReader(reader, &buffSk)
	io.Copy(&buffFp, reader)
	wg.Add(2)
	go r.makeFingerprint(id, &buffFp, &wg, &fp)
	go r.makeSketch(id, &buffSk, &wg, &sk)
	wg.Wait()
	r.fingerprints[fp] = id
	r.sketches.Set(sk, id)
	return
}

func (r *Repo) makeFingerprint(id *ChunkId, reader io.Reader, wg *sync.WaitGroup, ret *uint64) {
	defer wg.Done()
	hasher := rabinkarp64.NewFromPol(r.pol)
	io.Copy(hasher, reader)
	*ret = hasher.Sum64()
}

func (r *Repo) makeSketch(id *ChunkId, reader io.Reader, wg *sync.WaitGroup, ret *[]uint64) {
	defer wg.Done()
	*ret, _ = sketch.SketchChunk(reader, r.pol, r.chunkSize, r.sketchWSize, r.sketchSfCount, r.sketchFCount)
}
func contains(s []*ChunkId, id *ChunkId) bool {
	for _, v := range s {
		if v == id {
			return true
		}
	}
	return false
}

func (r *Repo) findSimilarChunk(chunk Chunk) (*ChunkId, bool) {
	var similarChunks = make(map[ChunkId]int)
	var max int
	var similarChunk *ChunkId
	sketch, _ := sketch.SketchChunk(chunk.Reader(), r.pol, r.chunkSize, r.sketchWSize, r.sketchSfCount, r.sketchFCount)
	for _, s := range sketch {
		chunkIds, exists := r.sketches[s]
		if !exists {
			continue
		}
		for _, id := range chunkIds {
			count := similarChunks[*id]
			count += 1
			logger.Infof("found %d %d time(s)", id, count)
			if count > max {
				similarChunk = id
			}
			similarChunks[*id] = count
		}
	}
	return similarChunk, similarChunk != nil
}

func (r *Repo) tryDeltaEncodeChunk(temp BufferedChunk) (Chunk, bool) {
	id, found := r.findSimilarChunk(temp)
	if found {
		var buff bytes.Buffer
		if err := r.differ.Diff(r.LoadChunkContent(id), temp.Reader(), &buff); err != nil {
			logger.Error("trying delta encode chunk:", temp, "with source:", id, ":", err)
		} else {
			return &DeltaChunk{
				repo:   r,
				Source: id,
				Patch:  buff.Bytes(),
				Size:   temp.Len(),
			}, true
		}
	}
	return temp, false
}

// encodeTempChunk first tries to delta-encode the given chunk before attributing
// it an Id and saving it into the fingerprints and sketches maps.
func (r *Repo) encodeTempChunk(temp BufferedChunk, version int, last *uint64, storeQueue chan<- chunkData) (chunk Chunk, isDelta bool) {
	chunk, isDelta = r.tryDeltaEncodeChunk(temp)
	if isDelta {
		logger.Info("add new delta chunk")
		return
	}
	if chunk.Len() == r.chunkSize {
		id := &ChunkId{Ver: version, Idx: *last}
		*last++
		fp, sk := r.hashChunk(id, temp.Reader())
		storeQueue <- chunkData{
			hashes:  chunkHashes{fp, sk},
			content: temp.Bytes(),
			id:      id,
		}
		r.chunkCache.Set(id, temp.Bytes())
		logger.Info("add new chunk", id)
		return NewStoredChunk(r, id), false
	}
	logger.Info("add new partial chunk of size:", chunk.Len())
	return
}

// encodeTempChunks encodes the current temporary chunks based on the value of the previous one.
// Temporary chunks can be partial. If the current chunk is smaller than the size of a
// super-feature and there exists a previous chunk, then both are merged before attempting
// to delta-encode them.
func (r *Repo) encodeTempChunks(prev BufferedChunk, curr BufferedChunk, version int, last *uint64, storeQueue chan<- chunkData) []Chunk {
	if reflect.ValueOf(prev).IsNil() {
		c, _ := r.encodeTempChunk(curr, version, last, storeQueue)
		return []Chunk{c}
	} else if curr.Len() < r.chunkMinLen() {
		tmp := NewTempChunk(append(prev.Bytes(), curr.Bytes()...))
		c, success := r.encodeTempChunk(tmp, version, last, storeQueue)
		if success {
			return []Chunk{c}
		} else {
			return []Chunk{prev, curr}
		}
	} else {
		prevD, _ := r.encodeTempChunk(prev, version, last, storeQueue)
		currD, _ := r.encodeTempChunk(curr, version, last, storeQueue)
		return []Chunk{prevD, currD}
	}
}

func (r *Repo) matchStream(stream io.Reader, version int) []Chunk {
	var b byte
	var chunks []Chunk
	var prev *TempChunk
	var last uint64
	var err error
	bufStream := bufio.NewReaderSize(stream, r.chunkSize*2)
	buff := make([]byte, r.chunkSize, r.chunkSize*2)
	if n, err := io.ReadFull(stream, buff); n < r.chunkSize {
		if err == io.EOF {
			chunks = append(chunks, NewTempChunk(buff[:n]))
			return chunks
		} else {
			logger.Panicf("matching stream, read only %d bytes with error '%s'", n, err)
		}
	}
	hasher := rabinkarp64.NewFromPol(r.pol)
	hasher.Write(buff)
	storeQueue := make(chan chunkData, 10)
	storeEnd := make(chan bool)
	go r.storageWorker(version, storeQueue, storeEnd)
	for err != io.EOF {
		h := hasher.Sum64()
		chunkId, exists := r.fingerprints[h]
		if exists {
			if len(buff) > r.chunkSize && len(buff) < r.chunkSize*2 {
				size := len(buff) - r.chunkSize
				temp := NewTempChunk(buff[:size])
				chunks = append(chunks, r.encodeTempChunks(prev, temp, version, &last, storeQueue)...)
				prev = nil
			} else if prev != nil {
				c, _ := r.encodeTempChunk(prev, version, &last, storeQueue)
				chunks = append(chunks, c)
				prev = nil
			}
			logger.Infof("add existing chunk: %d", chunkId)
			chunks = append(chunks, NewStoredChunk(r, chunkId))
			buff = make([]byte, 0, r.chunkSize*2)
			for i := 0; i < r.chunkSize && err == nil; i++ {
				b, err = bufStream.ReadByte()
				if err != io.EOF {
					hasher.Roll(b)
					buff = append(buff, b)
				}
			}
			continue
		}
		if len(buff) == r.chunkSize*2 {
			if prev != nil {
				chunk, _ := r.encodeTempChunk(prev, version, &last, storeQueue)
				chunks = append(chunks, chunk)
			}
			prev = NewTempChunk(buff[:r.chunkSize])
			tmp := buff[r.chunkSize:]
			buff = make([]byte, r.chunkSize, r.chunkSize*2)
			copy(buff, tmp)
		}
		b, err = bufStream.ReadByte()
		if err != io.EOF {
			hasher.Roll(b)
			buff = append(buff, b)
		}
	}
	if len(buff) > 0 {
		var temp *TempChunk
		if len(buff) > r.chunkSize {
			if prev != nil {
				chunk, _ := r.encodeTempChunk(prev, version, &last, storeQueue)
				chunks = append(chunks, chunk)
			}
			prev = NewTempChunk(buff[:r.chunkSize])
			temp = NewTempChunk(buff[r.chunkSize:])
		} else {
			temp = NewTempChunk(buff)
		}
		chunks = append(chunks, r.encodeTempChunks(prev, temp, version, &last, storeQueue)...)
	}
	close(storeQueue)
	<-storeEnd
	return chunks
}

func (r *Repo) restoreStream(stream io.WriteCloser, recipe []Chunk) {
	for _, c := range recipe {
		if rc, isRepo := c.(RepoChunk); isRepo {
			rc.SetRepo(r)
		}
		if n, err := io.Copy(stream, c.Reader()); err != nil {
			logger.Errorf("copying to stream, read %d bytes from chunk: %s", n, err)
		}
	}
	stream.Close()
}

func storeRecipe(dest string, recipe []Chunk) {
	gob.Register(&StoredChunk{})
	gob.Register(&TempChunk{})
	gob.Register(&DeltaChunk{})
	file, err := os.Create(dest)
	if err == nil {
		encoder := gob.NewEncoder(file)
		for _, c := range recipe {
			if err = encoder.Encode(&c); err != nil {
				logger.Panic(err)
			}
		}
	}
	if err != nil {
		logger.Panic(err)
	}
	if err = file.Close(); err != nil {
		logger.Panic(err)
	}
}

func loadRecipe(path string) []Chunk {
	var recipe []Chunk
	gob.Register(&StoredChunk{})
	gob.Register(&TempChunk{})
	gob.Register(&DeltaChunk{})
	file, err := os.Open(path)
	if err == nil {
		decoder := gob.NewDecoder(file)
		for i := 0; err == nil; i++ {
			var c Chunk
			if err = decoder.Decode(&c); err == nil {
				recipe = append(recipe, c)
			}
		}
	}
	if err != nil && err != io.EOF {
		logger.Panic(err)
	}
	if err = file.Close(); err != nil {
		logger.Panic(err)
	}
	return recipe
}

func extractDeltaChunks(chunks []Chunk) (ret []*DeltaChunk) {
	for _, c := range chunks {
		tmp, isDelta := c.(*DeltaChunk)
		if isDelta {
			ret = append(ret, tmp)
		}
	}
	return
}
