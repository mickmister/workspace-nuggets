package nuggets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dolthub/dolt/go/gen/fb/serial"
	"github.com/dolthub/dolt/go/store/chunks"
	"github.com/dolthub/dolt/go/store/hash"
	"github.com/dolthub/dolt/go/store/prolly"
	"github.com/dolthub/dolt/go/store/prolly/tree"
	"github.com/dolthub/dolt/go/store/types"
)

// BeadsServingPublication is derived read-only data, never a replacement Dolt
// remote. It is pinned to a separately retained canonical Git snapshot.
type BeadsServingPublication struct {
	Ref          string `json:"ref"`
	GitCommit    string `json:"git_commit"`
	EncodedBytes int    `json:"encoded_bytes"`
	Files        int    `json:"files"`
}

type servingFile struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type servingDescriptor struct {
	SchemaVersion   int           `json:"schemaVersion"`
	Format          string        `json:"format"`
	Workspace       string        `json:"workspace"`
	CollectionID    string        `json:"collectionId"`
	SourceGitCommit string        `json:"sourceGitCommit"`
	StorageRoot     string        `json:"storageRoot"`
	DoltCommit      string        `json:"doltCommit"`
	Root            string        `json:"root"`
	Manifest        servingFile   `json:"manifest"`
	Files           []servingFile `json:"files"`
}

var servingNativePath = regexp.MustCompile(`^(?:oldgen/)?[0-9a-v]{32}(?:\.darc)?$`)

func servingReference(path string, data []byte) servingFile {
	digest := sha256.Sum256(data)
	return servingFile{path, len(data), hex.EncodeToString(digest[:])}
}

// Materialize only an immutable remote snapshot, never the live embedded DB.
// Resolve archive dictionaries and old-generation tables rather than guessing
// which filenames contain current data.
func materializeServingSource(ctx context.Context, t *transaction, commit string) (string, []byte, error) {
	if _, err := t.git(ctx, nil, "fetch", "--depth=1", "--no-tags", t.remote, commit); err != nil {
		return "", nil, err
	}
	listing, err := t.git(ctx, nil, "ls-tree", "-rlz", commit)
	if err != nil {
		return "", nil, err
	}
	directory, err := os.MkdirTemp(t.dir, "serving-source-")
	if err != nil {
		return "", nil, err
	}
	var manifest []byte
	for _, record := range bytes.Split(listing, []byte{0}) {
		metadata, path, ok := strings.Cut(string(record), "\t")
		if !ok || !(path == "manifest" || path == "oldgen/manifest" || servingNativePath.MatchString(path)) {
			continue
		}
		fields := strings.Fields(metadata)
		if len(fields) != 4 || fields[0] != "100644" || fields[1] != "blob" {
			return "", nil, fmt.Errorf("native serving source must contain regular blobs")
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			return "", nil, fmt.Errorf("invalid native source size")
		}
		data, err := t.git(ctx, nil, "cat-file", "blob", fields[2])
		if err != nil {
			return "", nil, err
		}
		if int64(len(data)) != size {
			return "", nil, fmt.Errorf("native serving source size mismatch")
		}
		destination := filepath.Join(directory, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			return "", nil, err
		}
		if path == "manifest" {
			manifest = data
		}
	}
	if len(manifest) == 0 {
		return "", nil, fmt.Errorf("native serving source has no manifest")
	}
	return directory, manifest, nil
}

func servingStore(ctx context.Context, directory string) (chunks.ChunkStore, error) {
	store := &servingSourceStore{data: make(map[hash.Hash][]byte)}
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "manifest" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return decodeServingFile(data, store.load)
	})
	return store, err
}

func servingChunk(ctx context.Context, store chunks.ChunkStore, address hash.Hash) (chunks.Chunk, error) {
	chunk, err := store.Get(ctx, address)
	if err != nil {
		return chunk, err
	}
	digest := sha512.Sum512(chunk.Data())
	if chunk.IsEmpty() || !bytes.Equal(digest[:20], address[:]) {
		return chunk, fmt.Errorf("missing or corrupt current-state chunk %s", address)
	}
	return chunk, nil
}

func buildServingPack(ctx context.Context, store chunks.ChunkStore, manifest []byte, descriptor *servingDescriptor) (map[string][]byte, error) {
	fields := strings.Split(strings.TrimSpace(string(manifest)), ":")
	if len(fields) < 5 || fields[0] != "5" || fields[1] != "__DOLT__" || !nativeCommitPattern.MatchString(fields[3]) {
		return nil, fmt.Errorf("unsupported serving manifest")
	}
	storageRoot := hash.Parse(fields[3])
	chunk, err := servingChunk(ctx, store, storageRoot)
	if err != nil {
		return nil, err
	}
	if serial.GetFileID(chunk.Data()) != serial.StoreRootFileID {
		return nil, fmt.Errorf("invalid serving store root")
	}
	root, err := serial.TryGetRootAsStoreRoot(chunk.Data(), serial.MessagePrefixSz)
	if err != nil {
		return nil, err
	}
	node, kind, err := tree.NodeFromBytes(root.AddressMapBytes())
	if err != nil {
		return nil, err
	}
	if kind != serial.AddressMapFileID {
		return nil, fmt.Errorf("invalid serving refs map")
	}
	refs, err := prolly.NewAddressMap(node, tree.NewNodeStore(store))
	if err != nil {
		return nil, err
	}
	head, err := refs.Get(ctx, "refs/heads/main")
	if err != nil || head.IsEmpty() {
		return nil, fmt.Errorf("serving snapshot requires refs/heads/main")
	}
	chunk, err = servingChunk(ctx, store, head)
	if err != nil {
		return nil, err
	}
	if serial.GetFileID(chunk.Data()) != serial.CommitFileID {
		return nil, fmt.Errorf("invalid serving head commit")
	}
	commit, err := serial.TryGetRootAsCommit(chunk.Data(), serial.MessagePrefixSz)
	if err != nil {
		return nil, err
	}
	currentRoot := hash.New(commit.RootBytes())
	descriptor.StorageRoot, descriptor.DoltCommit, descriptor.Root = storageRoot.String(), head.String(), currentRoot.String()
	walk, err := types.WalkAddrsForChunkStore(store)
	if err != nil {
		return nil, err
	}
	seen, selected := hash.HashSet{}, hash.HashSet{}
	queue := []hash.Hash{storageRoot, head, currentRoot}
	decoded := 0
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		address := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen.Has(address) {
			continue
		}
		seen.Insert(address)
		chunk, err := servingChunk(ctx, store, address)
		if err != nil {
			return nil, err
		}
		isCommit := serial.GetFileID(chunk.Data()) == serial.CommitFileID
		if isCommit && address != head {
			continue
		}
		selected.Insert(address)
		decoded += len(chunk.Data())
		if decoded > 256<<20 || len(selected) > 100000 {
			return nil, fmt.Errorf("current-state serving data exceeds browser decoded bounds")
		}
		// Commit parents are deliberately absent; seed the selected root explicitly.
		if isCommit {
			continue
		}
		if err := walk(chunk, func(child hash.Hash, _ bool) error { queue = append(queue, child); return nil }); err != nil {
			return nil, err
		}
	}
	addresses := make([]hash.Hash, 0, len(selected))
	for address := range selected {
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].String() < addresses[j].String() })
	files := map[string][]byte{"manifest": manifest}
	descriptor.Manifest = servingReference("manifest", manifest)
	var batch []chunks.Chunk
	batchSize, encoded := 0, 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		data := encodeServingNBS(batch)
		name := hash.Of(data).String()
		encoded += len(data)
		if encoded > 64<<20 || len(data) > 16<<20 || len(descriptor.Files) >= 512 {
			return fmt.Errorf("current-state serving pack exceeds browser encoded bounds")
		}
		files[name] = data
		descriptor.Files = append(descriptor.Files, servingReference(name, data))
		batch, batchSize = nil, 0
		return nil
	}
	for _, address := range addresses {
		chunk, err := servingChunk(ctx, store, address)
		if err != nil {
			return nil, err
		}
		if batchSize+len(chunk.Data()) > 8<<20 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batch = append(batch, chunk)
		batchSize += len(chunk.Data())
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return files, nil
}

func (s Store) publishBeadsServing(ctx context.Context, t *transaction, info *BeadsInfo, source string) (*BeadsServingPublication, error) {
	directory, manifest, err := materializeServingSource(ctx, t, source)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	store, err := servingStore(ctx, directory)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	descriptor := servingDescriptor{SchemaVersion: 1, Format: "beads-current-head-v1", Workspace: s.Workspace, CollectionID: info.CollectionID, SourceGitCommit: source}
	files, err := buildServingPack(ctx, store, manifest, &descriptor)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(descriptor)
	if err != nil {
		return nil, err
	}
	files["serving.json"] = append(data, '\n')
	ref := "refs/beads/" + s.Workspace
	var parent string
	advertised, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, ref)
	if err == nil {
		parent = strings.Fields(string(advertised))[0]
		if _, err := t.git(ctx, nil, "fetch", "--depth=1", "--no-tags", t.remote, parent); err != nil {
			return nil, err
		}
	} else if !exitCode(err, 2) {
		return nil, err
	}
	if _, err := t.git(ctx, nil, "read-tree", "--empty"); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	encoded := 0
	for _, path := range paths {
		blob, err := t.git(ctx, files[path], "hash-object", "-w", "--stdin")
		if err != nil {
			return nil, err
		}
		if _, err := t.git(ctx, []byte("100644 "+strings.TrimSpace(string(blob))+"\t"+path+"\x00"), "update-index", "-z", "--index-info"); err != nil {
			return nil, err
		}
		if path != "manifest" && path != "serving.json" {
			encoded += len(files[path])
		}
	}
	tree, err := t.git(ctx, nil, "write-tree")
	if err != nil {
		return nil, err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree)), "-m", "Publish current-head Beads serving pack for " + source}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	candidate, err := t.git(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	// An old publisher must not deliberately overwrite a newer source's pack.
	current, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, info.DesiredRef)
	if err != nil {
		return nil, err
	}
	if fields := strings.Fields(string(current)); len(fields) != 2 || fields[0] != source {
		return nil, &OperationError{Code: "serving_conflict", Message: "native snapshot advanced during serving publication; retry synchronization"}
	}
	commit := strings.TrimSpace(string(candidate))
	if parent != "" {
		previousTree, err := t.git(ctx, nil, "rev-parse", parent+"^{tree}")
		if err != nil {
			return nil, err
		}
		if string(previousTree) == string(tree) {
			commit = parent
		}
	}
	if _, err := t.git(ctx, nil, "push", t.remote, commit+":"+ref); err != nil {
		if classifyBeadsPushError(err) == "push_diverged" {
			return nil, &OperationError{Code: "serving_conflict", Message: "another serving publisher advanced the ref; retry synchronization"}
		}
		return nil, err
	}
	readback, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, ref)
	if err != nil {
		return nil, err
	}
	if fields := strings.Fields(string(readback)); len(fields) != 2 || fields[0] != commit {
		return nil, &OperationError{Code: "serving_conflict", Message: "serving ref advanced during verification; retry synchronization"}
	}
	return &BeadsServingPublication{ref, commit, encoded, len(descriptor.Files)}, nil
}
