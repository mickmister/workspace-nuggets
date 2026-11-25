package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dolthub/dolt/go/gen/fb/serial"
	"github.com/dolthub/dolt/go/store/chunks"
	"github.com/dolthub/dolt/go/store/hash"
)

func TestServingCodecIntegrity(t *testing.T) {
	input := []chunks.Chunk{chunks.NewChunk([]byte("one")), chunks.NewChunk([]byte("two"))}
	sort.Slice(input, func(i, j int) bool { return input[i].Hash().String() < input[j].Hash().String() })
	encoded := encodeServingNBS(input)
	seen := 0
	if err := decodeServingFile(encoded, func(address, data []byte) error {
		if !bytes.Equal(data, input[seen].Data()) || hash.New(address) != input[seen].Hash() {
			t.Fatal("native round trip changed a chunk")
		}
		seen++
		return nil
	}); err != nil || seen != len(input) {
		t.Fatalf("native round trip: %d %v", seen, err)
	}
	corrupt := append([]byte{}, encoded...)
	corrupt[0] ^= 1
	badCount := append([]byte{}, encoded...)
	servingBE.PutUint32(badCount[len(badCount)-20:], 0x7fffffff)
	for _, data := range [][]byte{corrupt, badCount, []byte("short"), encoded[:len(encoded)-1]} {
		if err := decodeServingFile(data, func([]byte, []byte) error { return nil }); err == nil {
			t.Fatal("corrupt native data accepted")
		}
	}
}

// Independently exercise generated NBS files using the native Dolt executable,
// including preserved canonical history, exact provenance and stale writers.
func TestBeadsServingNativePublication(t *testing.T) {
	if os.Getenv("NUGS_TEST_BEADS") != "1" {
		t.Skip("set NUGS_TEST_BEADS=1 for native serving acceptance")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := newRig(t)
	remote := r.initGitRemote()
	db := filepath.Join(r.root, "database")
	if err := os.Mkdir(db, 0700); err != nil {
		t.Fatal(err)
	}
	r.run(db, "", "dolt", "init", "--name", "Nugs Test", "--email", "nugs@example.invalid")
	r.run(db, "", "dolt", "config", "--local", "--add", "user.name", "Nugs Test")
	r.run(db, "", "dolt", "config", "--local", "--add", "user.email", "nugs@example.invalid")
	r.run(db, "", "dolt", "sql", "-q", "CREATE TABLE issues (id INT PRIMARY KEY, title TEXT); INSERT INTO issues VALUES (1, 'historical');")
	r.run(db, "", "dolt", "commit", "-A", "-m", "Historical state")
	r.run(db, "", "dolt", "sql", "-q", "UPDATE issues SET title='current';")
	r.run(db, "", "dolt", "commit", "-A", "-m", "Current state")
	ref := "refs/dolt/serving-test"
	r.run(db, "", "dolt", "remote", "add", "--ref", ref, "origin", (&url.URL{Scheme: "git+file", Path: remote}).String())
	r.run(db, "", "dolt", "push", "-u", "origin", "main")
	source := r.run(r.root, "", "git", "--git-dir", remote, "rev-parse", ref)
	s := Store{Remote: remote, Workspace: "serving-test", CacheDir: filepath.Join(r.root, "cache")}
	tx, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tx.dir)
	info := &BeadsInfo{CollectionID: "11111111-1111-4111-8111-111111111111", DesiredRef: ref}
	publication, err := s.publishBeadsServing(ctx, tx, info, source)
	if err != nil {
		t.Fatal(err)
	}
	if publication.EncodedBytes == 0 || publication.Files == 0 {
		t.Fatal("empty serving publication")
	}
	if got := r.run(r.root, "", "git", "--git-dir", remote, "rev-parse", ref); got != source {
		t.Fatal("serving publication changed canonical history")
	}
	// Fetch the actual remote pack, independently of the writer's in-memory data.
	packDir, manifest, err := materializeServingSource(ctx, tx, publication.GitCommit)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := servingStore(ctx, packDir)
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	descriptorBytes, err := tx.git(ctx, nil, "show", publication.GitCommit+":serving.json")
	if err != nil {
		t.Fatal(err)
	}
	var descriptor servingDescriptor
	if err := json.Unmarshal(descriptorBytes, &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor.SourceGitCommit != source || descriptor.Workspace != s.Workspace || descriptor.CollectionID != info.CollectionID {
		t.Fatal("serving provenance mismatch")
	}
	head, err := servingChunk(ctx, pack, hash.Parse(descriptor.DoltCommit))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := serial.TryGetRootAsCommit(head.Data(), serial.MessagePrefixSz)
	if err != nil {
		t.Fatal(err)
	}
	if commit.ParentAddrsLength() < 20 {
		t.Fatal("fixture needs commit history")
	}
	if present, _ := pack.Has(ctx, hash.New(commit.ParentAddrsBytes()[:20])); present {
		t.Fatal("serving pack retained head's parent commit")
	}
	// A test-only local manifest lists the derived tables, allowing native SQL to
	// independently verify the codec/current root. The published original manifest
	// and descriptor explicitly describe a serving pack, not a full Dolt remote.
	consumer := filepath.Join(r.root, "consumer")
	if err := os.Mkdir(consumer, 0700); err != nil {
		t.Fatal(err)
	}
	r.run(consumer, "", "dolt", "init", "--name", "Nugs Test", "--email", "nugs@example.invalid")
	noms := filepath.Join(consumer, ".dolt", "noms")
	if err := os.RemoveAll(noms); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(noms, 0700); err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(string(manifest)), ":")[:5]
	for _, file := range descriptor.Files {
		data, err := os.ReadFile(filepath.Join(packDir, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		if servingReference(file.Path, data) != file {
			t.Fatal("serving checksum mismatch")
		}
		fields = append(fields, file.Path, fmt.Sprint(servingBE.Uint32(data[len(data)-20:])))
		if err := os.WriteFile(filepath.Join(noms, file.Path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Native Dolt eagerly resolves commit parents even for ordinary SQL, unlike
	// the read-only browser adapter. Supply only those metadata chunks in this
	// independent codec test; all current table/schema/row bytes remain pack-only.
	sourceDir, _, err := materializeServingSource(ctx, tx, source)
	if err != nil {
		t.Fatal(err)
	}
	full, err := servingStore(ctx, sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	var parents []chunks.Chunk
	for address, data := range full.(*servingSourceStore).data {
		if serial.GetFileID(data) == serial.CommitFileID {
			parents = append(parents, chunks.NewChunkWithHash(address, data))
		}
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].Hash().String() < parents[j].Hash().String() })
	parentData := encodeServingNBS(parents)
	parentName := hash.Of(parentData).String()
	if err := os.WriteFile(filepath.Join(noms, parentName), parentData, 0600); err != nil {
		t.Fatal(err)
	}
	fields = append(fields, parentName, fmt.Sprint(len(parents)))
	if err := os.WriteFile(filepath.Join(noms, "manifest"), []byte(strings.Join(fields, ":")), 0600); err != nil {
		t.Fatal(err)
	}
	rows := r.run(consumer, "", "dolt", "sql", "-r", "csv", "-q", "SELECT id,title FROM issues")
	if !strings.Contains(rows, "1,current") || strings.Contains(rows, "historical") {
		t.Fatalf("native SQL parity: %s", rows)
	}
	replay, err := s.publishBeadsServing(ctx, tx, info, source)
	if err != nil || replay.GitCommit != publication.GitCommit {
		t.Fatalf("serving replay is not idempotent: %+v %v", replay, err)
	}
	r.run(db, "", "dolt", "sql", "-q", "UPDATE issues SET title='next';")
	r.run(db, "", "dolt", "commit", "-A", "-m", "Next state")
	r.run(db, "", "dolt", "push", "origin", "main")
	if _, err := s.publishBeadsServing(ctx, tx, info, source); err == nil {
		t.Fatal("stale publisher accepted")
	}
	if got := r.run(r.root, "", "git", "--git-dir", remote, "rev-parse", publication.Ref); got != publication.GitCommit {
		t.Fatal("stale publisher changed serving ref")
	}
	if rows := r.run(db, "", "dolt", "sql", "-r", "csv", "-q", "SELECT title FROM issues AS OF 'HEAD~2'"); !strings.Contains(rows, "historical") {
		t.Fatal("canonical historical rows were lost")
	}
}
