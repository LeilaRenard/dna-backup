package sketch

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/chmduquesne/rollinghash/rabinkarp64"
	"github.com/n-peugnet/dna-backup/logger"
)

type Sketch []uint64

type ReadByteReader interface {
	io.Reader
	io.ByteReader
}

const fBytes = 8

// SketchChunk produces a sketch for a chunk based on wSize: the window size,
// sfCount: the number of super-features, and fCount: the number of feature
// per super-feature
func SketchChunk(r io.Reader, pol rabinkarp64.Pol, chunkSize int, wSize int, sfCount int, fCount int) (Sketch, error) {
	var fSize = FeatureSize(chunkSize, sfCount, fCount)
	var chunk bytes.Buffer
	superfeatures := make([]uint64, 0, sfCount)
	features := make([]uint64, 0, fCount*sfCount)
	sfBuff := make([]byte, fBytes*fCount)
	chunkLen, err := chunk.ReadFrom(r)
	if err != nil {
		logger.Panic(chunkLen, err)
	}
	for f := 0; f < int(chunkLen)/fSize; f++ {
		var fBuff bytes.Buffer
		n, err := io.CopyN(&fBuff, &chunk, int64(fSize))
		if err != nil {
			logger.Error(n, err)
			continue
		}
		features = append(features, 0)
		calcFeature(pol, &fBuff, wSize, fSize, &features[f])
	}
	hasher := rabinkarp64.NewFromPol(pol)
	for sf := 0; sf < len(features)/fCount; sf++ {
		for i := 0; i < fCount; i++ {
			binary.LittleEndian.PutUint64(sfBuff[i*fBytes:(i+1)*fBytes], features[i+sf*fCount])
		}
		hasher.Reset()
		hasher.Write(sfBuff)
		superfeatures = append(superfeatures, hasher.Sum64())
	}
	return superfeatures, nil
}

func calcFeature(p rabinkarp64.Pol, r ReadByteReader, wSize int, fSize int, result *uint64) {
	hasher := rabinkarp64.NewFromPol(p)
	n, err := io.CopyN(hasher, r, int64(wSize))
	if err != nil {
		logger.Error(n, err)
	}
	max := hasher.Sum64()
	for w := 0; w < fSize-wSize; w++ {
		b, _ := r.ReadByte()
		hasher.Roll(b)
		h := hasher.Sum64()
		if h > max {
			max = h
		}
	}
	*result = max
}

func SuperFeatureSize(chunkSize int, sfCount int, fCount int) int {
	return FeatureSize(chunkSize, sfCount, fCount) * sfCount
}

func FeatureSize(chunkSize int, sfCount int, fCount int) int {
	return chunkSize / (sfCount * fCount)
}
