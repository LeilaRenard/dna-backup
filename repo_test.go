package main

import (
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"reflect"
	"testing"

	"github.com/gabstv/go-bsdiff/pkg/bsdiff"
)

func chunkCompare(t *testing.T, dataDir string, testFiles []string, chunkCount int) {
	reader, writer := io.Pipe()
	chunks := make(chan []byte)
	files := listFiles(dataDir)
	go concatFiles(files, writer)
	go chunkStream(reader, chunks)

	offset := 0
	buff := make([]byte, chunkSize*chunkCount)
	for _, f := range testFiles {
		content, err := os.ReadFile(path.Join(dataDir, f))
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
		start := i * chunkSize
		end := (i + 1) * chunkSize
		if end > offset {
			end = offset
		}
		content := buff[start:end]
		if bytes.Compare(c, content) != 0 {
			t.Errorf("Chunk %d does not match file content", i)
			// for i, b := range c {
			// 	fmt.Printf("E: %d, A: %d\n", b, content[i])
			// }
			t.Log("Expected: ", c[:10], "...", c[end%chunkSize-10:])
			t.Log("Actual:", content)
		}
		i++
	}
	if i != chunkCount {
		t.Errorf("Incorrect number of chunks: %d, should be: %d", i, chunkCount)
	}
}

func TestReadFiles1(t *testing.T) {
	chunkCount := 590/chunkSize + 1
	dataDir := path.Join("test", "data", "logs", "1")
	files := []string{"logTest.log"}
	chunkCompare(t, dataDir, files, chunkCount)
}

func TestReadFiles2(t *testing.T) {
	chunkCount := 22899/chunkSize + 1
	dataDir := path.Join("test", "data", "logs", "2")
	files := []string{"csvParserTest.log", "slipdb.log"}
	chunkCompare(t, dataDir, files, chunkCount)
}

func TestReadFiles3(t *testing.T) {
	chunkCount := 119398/chunkSize + 1
	dataDir := path.Join("test", "data", "logs")
	files := []string{
		path.Join("1", "logTest.log"),
		path.Join("2", "csvParserTest.log"),
		path.Join("2", "slipdb.log"),
		path.Join("3", "indexingTreeTest.log"),
	}
	chunkCompare(t, dataDir, files, chunkCount)
}

func TestLoadChunks(t *testing.T) {
	resultDir := t.TempDir()
	dataDir := path.Join("test", "data", "logs")
	repo := NewRepo(resultDir)
	resultVersion := path.Join(resultDir, "00000")
	resultChunks := path.Join(resultVersion, chunksName)
	os.MkdirAll(resultChunks, 0775)
	reader1, writer1 := io.Pipe()
	reader2, writer2 := io.Pipe()
	chunks1 := make(chan []byte, 16)
	chunks2 := make(chan []byte, 16)
	chunks3 := make(chan StoredChunk, 16)
	files := listFiles(dataDir)
	go concatFiles(files, writer1)
	go concatFiles(files, writer2)
	go chunkStream(reader1, chunks1)
	go chunkStream(reader2, chunks2)
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

func TestExtractNewChunks(t *testing.T) {
	chunks := []Chunk{
		&TempChunk{value: []byte{'a'}},
		&LoadedChunk{id: &ChunkId{0, 0}},
		&TempChunk{value: []byte{'b'}},
		&TempChunk{value: []byte{'c'}},
		&LoadedChunk{id: &ChunkId{0, 1}},
	}
	newChunks := extractTempChunks(chunks)
	assertLen(t, 2, newChunks, "New chunks:")
	assertChunkContent(t, []byte{'a'}, newChunks[0], "First new:")
	assertChunkContent(t, []byte{'b', 'c'}, newChunks[1], "Second New:")
}

func TestStoreLoadFiles(t *testing.T) {
	resultDir := t.TempDir()
	dataDir := path.Join("test", "data", "logs")
	resultFiles := path.Join(resultDir, filesName)
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

func TestBsdiff(t *testing.T) {
	resultDir := t.TempDir()
	dataDir := path.Join("test", "data", "logs")
	addedFile := path.Join(dataDir, "2", "slogTest.log")
	resultVersion := path.Join(resultDir, "00000")
	resultChunks := path.Join(resultVersion, chunksName)
	os.MkdirAll(resultChunks, 0775)
	reader, writer := io.Pipe()
	chunks := make(chan []byte, 16)
	files := listFiles(dataDir)
	go concatFiles(files, writer)
	go chunkStream(reader, chunks)
	storeChunks(resultChunks, chunks)

	input := []byte("hello")
	ioutil.WriteFile(addedFile, input, 0664)
	defer os.Remove(addedFile)

	reader, writer = io.Pipe()
	oldChunks := make(chan StoredChunk, 16)
	files = listFiles(dataDir)
	repo := NewRepo(resultDir)
	versions := repo.loadVersions()
	go repo.loadChunks(versions, oldChunks)
	go concatFiles(files, writer)
	fingerprints, sketches := hashChunks(oldChunks)
	recipe := repo.matchStream(reader, fingerprints)
	newChunks := extractTempChunks(recipe)
	assertLen(t, 2, newChunks, "New chunks:")
	for _, c := range newChunks {
		id, exists := findSimilarChunk(c, sketches)
		log.Println(id, exists)
		if exists {
			patch := new(bytes.Buffer)
			stored := id.Reader(repo.path)
			new := c.Reader()
			bsdiff.Reader(stored, new, patch)
			log.Println("Patch size:", patch.Len())
			if patch.Len() >= chunkSize/10 {
				t.Errorf("Bsdiff of chunk is too large: %d", patch.Len())
			}
		}
	}
}

func assertLen(t *testing.T, expected int, actual interface{}, prefix string) {
	s := reflect.ValueOf(actual)
	if s.Len() != expected {
		t.Error(prefix, "incorrect length, expected:", expected, ", actual:", s.Len())
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
