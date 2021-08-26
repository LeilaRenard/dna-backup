package main

import (
	"path"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSketchChunk(t *testing.T) {
	dataDir := path.Join("test", "data", "repo_8k")
	chunks := make(chan Chunk, 16)
	repo := NewRepo(dataDir)
	versions := repo.loadVersions()
	go repo.loadChunks(versions, chunks)
	var i int
	for c := range chunks {
		if i < 1 {
			sketch, err := SketchChunk(c, 32, 3, 4)
			if err != nil {
				t.Error(err)
			}
			expected := []uint64{429857165471867, 6595034117354675, 8697818304802825}
			if !cmp.Equal(sketch, expected) {
				t.Errorf("Sketch does not match, expected: %d, actual: %d", expected, sketch)
			}
		}
		i++
	}
}
