package nuggets

// Portable native storage codec, adapted from beads-browser-engine/engine/decode.go
// at 4f88869e3a547361830dd8fb471607f3b1bd36b5 and its current-head benchmark.
// Format references: Dolt v2.3.1 NBS table.go/archive.go (Apache-2.0).
// Dolt's nbs package requires C-backed gozstd, unavailable in bundled nugs builds.
import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/dolthub/dolt/go/store/chunks"
	"github.com/golang/snappy"
	"github.com/klauspost/compress/zstd"
)

var servingCRC = crc32.MakeTable(crc32.Castagnoli)
var servingBE = binary.BigEndian

const servingMaxChunk = 32 << 20

func servingSnappy(record []byte) ([]byte, error) {
	if len(record) < 4 {
		return nil, fmt.Errorf("short native record")
	}
	data := record[:len(record)-4]
	if crc32.Checksum(data, servingCRC) != servingBE.Uint32(record[len(record)-4:]) {
		return nil, fmt.Errorf("native CRC32C mismatch")
	}
	size, err := snappy.DecodedLen(data)
	if err != nil || size > servingMaxChunk {
		return nil, fmt.Errorf("invalid native chunk size")
	}
	return snappy.Decode(nil, data)
}

func decodeServingFile(b []byte, sink func([]byte, []byte) error) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("invalid native table bounds: %v", value)
		}
	}()
	checked := func(address, data []byte) error {
		digest := sha512.Sum512(data)
		if !bytes.Equal(address, digest[:20]) {
			return fmt.Errorf("native SHA512/160 mismatch")
		}
		return sink(address, data)
	}
	if len(b) >= 7 && string(b[len(b)-7:]) == "DOLTARC" {
		return decodeServingArchive(b, checked)
	}
	if len(b) < 20 || !bytes.Equal(b[len(b)-8:], []byte{255, 181, 216, 194, 36, 99, 238, 80}) {
		return fmt.Errorf("invalid NBS footer")
	}
	footer := b[len(b)-20:]
	n := int(servingBE.Uint32(footer))
	if n < 0 || n > (len(b)-20)/28 {
		return fmt.Errorf("invalid NBS chunk count")
	}
	start := len(b) - 20 - n*28
	idx := b[start : len(b)-20]
	offsets := make([]int, n+1)
	for i := 0; i < n; i++ {
		offsets[i+1] = offsets[i] + int(servingBE.Uint32(idx[n*12+i*4:]))
	}
	if offsets[n] != start {
		return fmt.Errorf("NBS data length mismatch")
	}
	seen := make([]bool, n)
	decoded := uint64(0)
	for i := 0; i < n; i++ {
		ordinal := int(servingBE.Uint32(idx[i*12+8:]))
		if ordinal >= n || seen[ordinal] {
			return fmt.Errorf("invalid NBS ordinal")
		}
		seen[ordinal] = true
		address := append(append([]byte{}, idx[i*12:i*12+8]...), idx[n*16+ordinal*12:n*16+(ordinal+1)*12]...)
		data, err := servingSnappy(b[offsets[ordinal]:offsets[ordinal+1]])
		if err != nil {
			return err
		}
		if err := checked(address, data); err != nil {
			return err
		}
		decoded += uint64(len(data))
	}
	if decoded != servingBE.Uint64(footer[4:]) {
		return fmt.Errorf("NBS decoded length mismatch")
	}
	return nil
}

func decodeServingArchive(b []byte, sink func([]byte, []byte) error) error {
	version := b[len(b)-8]
	if version < 1 || version > 3 {
		return fmt.Errorf("unsupported native archive version")
	}
	footerSize := 220
	if version < 3 {
		footerSize = 216
	}
	if len(b) < footerSize {
		return fmt.Errorf("short native archive")
	}
	footer := b[len(b)-footerSize:]
	indexLen, offset := 0, 0
	if version < 3 {
		indexLen, offset = int(servingBE.Uint32(footer)), 4
	} else {
		indexLen, offset = int(servingBE.Uint64(footer)), 8
	}
	spanCount, n := int(servingBE.Uint32(footer[offset:])), int(servingBE.Uint32(footer[offset+4:]))
	if spanCount < 0 || n < 0 || spanCount > len(b)/8 || n > len(b)/28 {
		return fmt.Errorf("invalid native archive counts")
	}
	metadataLen := int(servingBE.Uint32(footer[offset+8:]))
	if indexLen != spanCount*8+n*28 {
		return fmt.Errorf("native archive index size mismatch")
	}
	start := len(b) - footerSize - metadataLen - indexLen
	if start < 0 {
		return fmt.Errorf("invalid native archive index")
	}
	idx := b[start : start+indexLen]
	ends := make([]int, spanCount+1)
	for i := 1; i <= spanCount; i++ {
		ends[i] = int(servingBE.Uint64(idx[(i-1)*8:]))
		if ends[i] < ends[i-1] || ends[i] > start {
			return fmt.Errorf("invalid native archive offsets")
		}
	}
	if ends[spanCount] != start {
		return fmt.Errorf("native archive data length mismatch")
	}
	span := func(id uint32) []byte {
		if id == 0 || int(id) > spanCount {
			panic("invalid native span")
		}
		return b[ends[id-1]:ends[id]]
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(servingMaxChunk))
	if err != nil {
		return err
	}
	defer decoder.Close()
	dictionaries := map[uint32]*zstd.Decoder{}
	defer func() {
		for _, dictionary := range dictionaries {
			dictionary.Close()
		}
	}()
	prefixes, refs := spanCount*8, spanCount*8+n*8
	suffixes := refs + n*8
	for i := 0; i < n; i++ {
		dictID, dataID := servingBE.Uint32(idx[refs+i*8:]), servingBE.Uint32(idx[refs+i*8+4:])
		var data []byte
		if dictID == 0 && version >= 2 {
			data, err = servingSnappy(span(dataID))
		} else {
			d := decoder
			if dictID != 0 {
				d = dictionaries[dictID]
				if d == nil {
					raw, err := decoder.DecodeAll(span(dictID), nil)
					if err != nil {
						return err
					}
					d, err = zstd.NewReader(nil, zstd.WithDecoderDicts(raw), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(servingMaxChunk))
					if err != nil {
						return err
					}
					dictionaries[dictID] = d
				}
			}
			data, err = d.DecodeAll(span(dataID), nil)
		}
		if err != nil {
			return err
		}
		address := append(append([]byte{}, idx[prefixes+i*8:prefixes+(i+1)*8]...), idx[suffixes+i*12:suffixes+(i+1)*12]...)
		if err := sink(address, data); err != nil {
			return err
		}
	}
	return nil
}

func encodeServingNBS(batch []chunks.Chunk) []byte {
	var data []byte
	index := make([]byte, 28*len(batch))
	var decoded uint64
	for i, chunk := range batch {
		record := snappy.Encode(nil, chunk.Data())
		record = servingBE.AppendUint32(record, crc32.Checksum(record, servingCRC))
		data = append(data, record...)
		address := chunk.Hash()
		copy(index[i*12:], address[:8])
		servingBE.PutUint32(index[i*12+8:], uint32(i))
		servingBE.PutUint32(index[len(batch)*12+i*4:], uint32(len(record)))
		copy(index[len(batch)*16+i*12:], address[8:])
		decoded += uint64(len(chunk.Data()))
	}
	data = append(data, index...)
	data = servingBE.AppendUint32(data, uint32(len(batch)))
	data = servingBE.AppendUint64(data, decoded)
	return append(data, 255, 181, 216, 194, 36, 99, 238, 80)
}
