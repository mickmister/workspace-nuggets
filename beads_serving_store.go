package nuggets

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/dolthub/dolt/go/store/chunks"
	"github.com/dolthub/dolt/go/store/hash"
)

var errServingReadOnly = errors.New("serving source is read only")

// Source decoding is publisher-side, not constrained by the browser's 256 MiB
// output bound. Keep an explicit 2 GiB / one-million-chunk admission bound.
type servingSourceStore struct {
	data   map[hash.Hash][]byte
	loaded int
	root   hash.Hash
}

func (s *servingSourceStore) load(address, data []byte) error {
	h := hash.New(address)
	if old, ok := s.data[h]; ok {
		if !bytes.Equal(old, data) {
			return fmt.Errorf("conflicting native chunk")
		}
		return nil
	}
	if len(data) > (2<<30)-s.loaded || len(s.data) >= 1000000 {
		return fmt.Errorf("native serving source exceeds publisher decoding bound")
	}
	s.data[h] = data
	s.loaded += len(data)
	return nil
}
func (s *servingSourceStore) Get(_ context.Context, h hash.Hash) (chunks.Chunk, error) {
	if data, ok := s.data[h]; ok {
		return chunks.NewChunkWithHash(h, data), nil
	}
	return chunks.EmptyChunk, nil
}
func (s *servingSourceStore) GetMany(ctx context.Context, addresses hash.HashSet, found func(context.Context, *chunks.Chunk)) error {
	for address := range addresses {
		chunk, err := s.Get(ctx, address)
		if err != nil {
			return err
		}
		if !chunk.IsEmpty() {
			found(ctx, &chunk)
		}
	}
	return nil
}
func (s *servingSourceStore) Has(_ context.Context, address hash.Hash) (bool, error) {
	_, ok := s.data[address]
	return ok, nil
}
func (s *servingSourceStore) HasMany(_ context.Context, addresses hash.HashSet) (hash.HashSet, error) {
	absent := hash.HashSet{}
	for address := range addresses {
		if _, ok := s.data[address]; !ok {
			absent.Insert(address)
		}
	}
	return absent, nil
}
func (*servingSourceStore) Put(context.Context, chunks.Chunk, chunks.InsertAddrsCurry) error {
	return errServingReadOnly
}
func (*servingSourceStore) Version() string { return "__DOLT__" }
func (*servingSourceStore) AccessMode() chunks.ExclusiveAccessMode {
	return chunks.ExclusiveAccessMode_ReadOnly
}
func (*servingSourceStore) Rebase(context.Context) error              { return errServingReadOnly }
func (s *servingSourceStore) Root(context.Context) (hash.Hash, error) { return s.root, nil }
func (*servingSourceStore) Commit(context.Context, hash.Hash, hash.Hash) (bool, error) {
	return false, errServingReadOnly
}
func (*servingSourceStore) Stats() interface{}   { return nil }
func (*servingSourceStore) StatsSummary() string { return "immutable serving source" }
func (*servingSourceStore) PersistGhostHashes(context.Context, hash.HashSet) error {
	return errServingReadOnly
}
func (*servingSourceStore) Close() error                   { return nil }
func (*servingSourceStore) Teardown(context.Context) error { return nil }
