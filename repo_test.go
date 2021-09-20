package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/n-peugnet/dna-backup/logger"
	"github.com/n-peugnet/dna-backup/utils"
)

func chunkCompare(t *testing.T, dataDir string, repo *Repo, testFiles []string, chunkCount int) {
	reader, writer := io.Pipe()
	chunks := make(chan []byte)
	files := listFiles(dataDir)
	go concatFiles(&files, writer)
	go repo.chunkStream(reader, chunks)

	offset := 0
	buff := make([]byte, repo.chunkSize*chunkCount)
	for _, f := range testFiles {
		content, err := os.ReadFile(filepath.Join(dataDir, f))
		if err != nil {
			t.Error("Error reading test data file")
		}
		for i := range content {
			buff[offset+i] = content[i]
		}
		offset += len(content)
	}

	i := 0
	for c := range chunks {
		start := i * repo.chunkSize
		end := (i + 1) * repo.chunkSize
		if end > offset {
			end = offset
		}
		content := buff[start:end]
		if bytes.Compare(c, content) != 0 {
			t.Errorf("Chunk %d does not match file content", i)
			// for i, b := range c {
			// 	fmt.Printf("E: %d, A: %d\n", b, content[i])
			// }
			t.Log("Expected: ", c[:10], "...", c[end%repo.chunkSize-10:])
			t.Log("Actual:", content)
		}
		i++
	}
	if i != chunkCount {
		t.Errorf("Incorrect number of chunks: %d, should be: %d", i, chunkCount)
	}
}

func (r *Repo) chunkStream(stream io.Reader, chunks chan<- []byte) {
	var buff []byte
	var prev, read = r.chunkSize, 0
	var err error

	for err != io.EOF {
		if prev == r.chunkSize {
			buff = make([]byte, r.chunkSize)
			prev, err = stream.Read(buff)
		} else {
			read, err = stream.Read(buff[prev:])
			prev += read
		}
		if err != nil && err != io.EOF {
			logger.Error(err)
		}
		if prev == r.chunkSize {
			chunks <- buff
		}
	}
	if prev != r.chunkSize {
		chunks <- buff[:prev]
	}
	close(chunks)
}

func storeChunks(dest string, chunks <-chan []byte) {
	i := 0
	for c := range chunks {
		path := filepath.Join(dest, fmt.Sprintf(chunkIdFmt, i))
		err := os.WriteFile(path, c, 0664)
		if err != nil {
			logger.Error(err)
		}
		i++
	}
}

func TestReadFiles1(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewRepo(tmpDir)
	chunkCount := 590/repo.chunkSize + 1
	dataDir := filepath.Join("testdata", "logs", "1")
	files := []string{"logTest.log"}
	chunkCompare(t, dataDir, repo, files, chunkCount)
}

func TestReadFiles2(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewRepo(tmpDir)
	chunkCount := 22899/repo.chunkSize + 1
	dataDir := filepath.Join("testdata", "logs", "2")
	files := []string{"csvParserTest.log", "slipdb.log"}
	chunkCompare(t, dataDir, repo, files, chunkCount)
}

func TestReadFiles3(t *testing.T) {
	tmpDir := t.TempDir()
	repo := NewRepo(tmpDir)
	chunkCount := 119398/repo.chunkSize + 1
	dataDir := filepath.Join("testdata", "logs")
	files := []string{
		filepath.Join("1", "logTest.log"),
		filepath.Join("2", "csvParserTest.log"),
		filepath.Join("2", "slipdb.log"),
		filepath.Join("3", "indexingTreeTest.log"),
	}
	chunkCompare(t, dataDir, repo, files, chunkCount)
}

func TestNoSuchFile(t *testing.T) {
	tmpDir := t.TempDir()
	os.Symlink("./notexisting", filepath.Join(tmpDir, "linknotexisting"))
	var buff bytes.Buffer
	files := listFiles(tmpDir)
	assertLen(t, 1, files, "Files")
	concatFiles(&files, utils.NopCloser(&buff))
	assertLen(t, 0, files, "Files")
	assertLen(t, 0, buff.Bytes(), "Buffer")
}

func TestLoadChunks(t *testing.T) {
	resultDir := t.TempDir()
	dataDir := filepath.Join("testdata", "logs")
	repo := NewRepo(resultDir)
	repo.chunkReadWrapper = utils.NopReadWrapper
	repo.chunkWriteWrapper = utils.NopWriteWrapper
	resultVersion := filepath.Join(resultDir, "00000")
	resultChunks := filepath.Join(resultVersion, chunksName)
	os.MkdirAll(resultChunks, 0775)
	reader1, writer1 := io.Pipe()
	reader2, writer2 := io.Pipe()
	chunks1 := make(chan []byte, 16)
	chunks2 := make(chan []byte, 16)
	chunks3 := make(chan IdentifiedChunk, 16)
	files := listFiles(dataDir)
	go concatFiles(&files, writer1)
	go concatFiles(&files, writer2)
	go repo.chunkStream(reader1, chunks1)
	go repo.chunkStream(reader2, chunks2)
	storeChunks(resultChunks, chunks1)
	versions := []string{resultVersion}
	go repo.loadChunks(versions, chunks3)

	i := 0
	for c2 := range chunks2 {
		c3 := <-chunks3
		buff, err := io.ReadAll(c3.Reader())
		if err != nil {
			t.Errorf("Error reading from chunk %d: %s\n", c3, err)
		}
		if bytes.Compare(c2, buff) != 0 {
			t.Errorf("Chunk %d does not match file content", i)
			t.Log("Expected: ", c2[:10], "...")
			t.Log("Actual:", buff)
		}
		i++
	}
}

func TestStoreLoadFiles(t *testing.T) {
	resultDir := t.TempDir()
	dataDir := filepath.Join("testdata", "logs")
	resultFiles := filepath.Join(resultDir, filesName)
	files1 := listFiles(dataDir)
	storeFileList(resultFiles, files1)
	files2 := loadFileList(resultFiles)
	assertLen(t, 4, files1, "Files:")
	for i, f := range files1 {
		if f != files2[i] {
			t.Errorf("Loaded file data %d does not match stored one", i)
			t.Log("Expected: ", f)
			t.Log("Actual: ", files2[i])
		}
	}
}

func prepareChunks(dataDir string, repo *Repo, streamFunc func(*[]File, io.WriteCloser)) {
	resultVersion := filepath.Join(repo.path, "00000")
	resultChunks := filepath.Join(resultVersion, chunksName)
	os.MkdirAll(resultChunks, 0775)
	reader := getDataStream(dataDir, streamFunc)
	chunks := make(chan []byte, 16)
	go repo.chunkStream(reader, chunks)
	storeChunks(resultChunks, chunks)
}

func getDataStream(dataDir string, streamFunc func(*[]File, io.WriteCloser)) io.Reader {
	reader, writer := io.Pipe()
	files := listFiles(dataDir)
	go streamFunc(&files, writer)
	return reader
}

func TestBsdiff(t *testing.T) {
	resultDir := t.TempDir()
	repo := NewRepo(resultDir)
	dataDir := filepath.Join("testdata", "logs")
	addedFile1 := filepath.Join(dataDir, "2", "slogTest.log")
	addedFile2 := filepath.Join(dataDir, "3", "slogTest.log")
	// Store initial chunks
	prepareChunks(dataDir, repo, concatFiles)

	// Modify data
	ioutil.WriteFile(addedFile1, []byte("hello"), 0664)
	defer os.Remove(addedFile1)
	ioutil.WriteFile(addedFile2, make([]byte, 4000), 0664)
	defer os.Remove(addedFile2)

	// configure repo
	repo.chunkReadWrapper = utils.NopReadWrapper
	repo.chunkWriteWrapper = utils.NopWriteWrapper

	// Load previously stored chunks
	oldChunks := make(chan IdentifiedChunk, 16)
	versions := repo.loadVersions()
	go repo.loadChunks(versions, oldChunks)
	repo.hashChunks(oldChunks)

	// Read new data
	newVersion := len(versions)
	newPath := filepath.Join(repo.path, fmt.Sprintf(versionFmt, newVersion))
	os.MkdirAll(newPath, 0775)
	reader := getDataStream(dataDir, concatFiles)
	recipe := repo.matchStream(reader, newVersion)
	newChunks := extractDeltaChunks(recipe)
	assertLen(t, 2, newChunks, "New delta chunks:")
	for _, c := range newChunks {
		logger.Info("Patch size:", len(c.Patch))
		if len(c.Patch) >= repo.chunkSize/10 {
			t.Errorf("Bsdiff of chunk is too large: %d", len(c.Patch))
		}
	}
}

func TestCommit(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join("testdata", "logs")
	expected := filepath.Join("testdata", "repo_8k")
	repo := NewRepo(dest)
	repo.chunkReadWrapper = utils.NopReadWrapper
	repo.chunkWriteWrapper = utils.NopWriteWrapper

	repo.Commit(source)
	assertSameTree(t, assertCompatibleRepoFile, expected, dest, "Commit")
}

func TestCommitZlib(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join("testdata", "logs")
	expected := filepath.Join("testdata", "repo_8k_zlib")
	repo := NewRepo(dest)
	repo.chunkReadWrapper = utils.ZlibReader
	repo.chunkWriteWrapper = utils.ZlibWriter

	repo.Commit(source)
	assertSameTree(t, assertCompatibleRepoFile, expected, dest, "Commit")
}

func TestRestore(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join("testdata", "repo_8k")
	expected := filepath.Join("testdata", "logs")
	repo := NewRepo(source)
	repo.chunkReadWrapper = utils.NopReadWrapper
	repo.chunkWriteWrapper = utils.NopWriteWrapper

	repo.Restore(dest)
	assertSameTree(t, assertSameFile, expected, dest, "Restore")
}

func TestRestoreZlib(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join("testdata", "repo_8k_zlib")
	expected := filepath.Join("testdata", "logs")
	repo := NewRepo(source)
	repo.chunkReadWrapper = utils.ZlibReader
	repo.chunkWriteWrapper = utils.ZlibWriter

	repo.Restore(dest)
	assertSameTree(t, assertSameFile, expected, dest, "Restore")
}

func TestHashes(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join("testdata", "repo_8k")

	chunks := make(chan IdentifiedChunk, 16)
	storeQueue := make(chan chunkData, 16)
	storeEnd := make(chan bool)

	repo1 := NewRepo(source)
	repo1.chunkReadWrapper = utils.NopReadWrapper
	repo1.chunkWriteWrapper = utils.NopWriteWrapper
	go repo1.loadChunks([]string{filepath.Join(source, "00000")}, chunks)
	for c := range chunks {
		fp, sk := repo1.hashChunk(c.GetId(), c.Reader())
		content, err := io.ReadAll(c.Reader())
		if err != nil {
			t.Error(err)
		}
		storeQueue <- chunkData{
			hashes:  chunkHashes{fp, sk},
			content: content,
			id:      c.GetId(),
		}
	}
	repo2 := NewRepo(dest)
	repo2.chunkReadWrapper = utils.NopReadWrapper
	repo2.chunkWriteWrapper = utils.NopWriteWrapper
	os.MkdirAll(filepath.Join(dest, "00000", chunksName), 0775)
	go repo2.storageWorker(0, storeQueue, storeEnd)
	close(storeQueue)
	<-storeEnd
	assertLen(t, 0, repo2.fingerprints, "Fingerprints")
	assertLen(t, 0, repo2.sketches, "Sketches")
	repo2.loadHashes([]string{filepath.Join(dest, "00000")})
	assertSame(t, repo1.fingerprints, repo2.fingerprints, "Fingerprint maps")
	assertSame(t, repo1.sketches, repo2.sketches, "Sketches maps")
}

func assertSameTree(t *testing.T, apply func(t *testing.T, expected string, actual string, prefix string), expected string, actual string, prefix string) {
	actualFiles := listFiles(actual)
	expectedFiles := listFiles(expected)
	efCount := len(expectedFiles)
	if efCount <= 0 {
		t.Fatalf("No expected files: %d", efCount)
	}
	afCount := len(actualFiles)
	if efCount != afCount {
		t.Fatalf("Incorrect number of files: %d, should be %d", afCount, efCount)
	}
	for i, ef := range expectedFiles {
		af := actualFiles[i]
		efRelPath := ef.Path[len(expected):]
		afRelPath := af.Path[len(actual):]
		if efRelPath != afRelPath {
			t.Fatalf("File path '%s' does not match '%s'", afRelPath, efRelPath)
		}
		apply(t, ef.Path, af.Path, prefix)
	}
}

func assertCompatibleRepoFile(t *testing.T, expected string, actual string, prefix string) {
	if filepath.Base(expected) == filesName {
		// Filelist file
		eFiles := loadFileList(expected)
		aFiles := loadFileList(actual)
		assertLen(t, len(eFiles), aFiles, prefix)
		for i, eFile := range eFiles {
			eFile.Path = filepath.FromSlash(eFile.Path)
			if eFile != aFiles[i] {
				t.Fatal(prefix, "file entry do not match:", aFiles[i], ", expected:", eFile)
			}
		}
	} else if filepath.Base(expected) == recipeName {
		// Recipe file
		eRecipe := loadRecipe(expected)
		aRecipe := loadRecipe(actual)
		assertLen(t, len(eRecipe), aRecipe, prefix)
		for i, eChunk := range eRecipe {
			assertSame(t, eChunk, aRecipe[i], prefix+" chunks")
		}
	} else if filepath.Base(expected) == hashesName {
		// Hashes file is checked in TestHashes
	} else {
		// Chunk content file
		assertSameFile(t, expected, actual, prefix)
	}
}

func assertSameFile(t *testing.T, expected string, actual string, prefix string) {
	efContent, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("%s Error reading from expected file '%s': %s", prefix, expected, err)
	}
	afContent, err := os.ReadFile(actual)
	if err != nil {
		t.Fatalf("%s Error reading from expected file '%s': %s", prefix, actual, err)
	}
	assertSameSlice(t, efContent, afContent, prefix+" files")
}

func assertLen(t *testing.T, expected int, actual interface{}, prefix string) {
	s := reflect.ValueOf(actual)
	if s.Len() != expected {
		t.Fatal(prefix, "incorrect length, expected:", expected, ", actual:", s.Len())
	}
}

func assertSameSlice(t *testing.T, expected []byte, actual []byte, prefix string) {
	assertLen(t, len(expected), actual, prefix)
	for i := 0; i < len(expected); i++ {
		if expected[i] != actual[i] {
			t.Fatal(prefix, "incorrect value", i, ", expected:", expected[i], ", actual:", actual[i])
		}
	}
}

func assertChunkContent(t *testing.T, expected []byte, c Chunk, prefix string) {
	buf, err := io.ReadAll(c.Reader())
	if err != nil {
		t.Fatal(err)
	}
	assertSameSlice(t, expected, buf, prefix+" Chunk content")
}

func assertSame(t *testing.T, expected interface{}, actual interface{}, prefix string) {
	if !reflect.DeepEqual(expected, actual) {
		t.Error(prefix, "do not match, expected:", expected, ", actual:", actual)
	}
}
