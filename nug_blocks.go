package nuggets

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

const (
	nugObjectLimit = 4 << 20
	nugBlockLimit  = 1 << 20
	nugValueLimit  = 256 << 10
	nugFanout      = 64
	nugRootKey     = "catalog/root.json"
)

// Immutable, compressed JSONL B+tree blocks. Only paths to changed leaves are
// rewritten. Staged intermediate nodes are never uploaded: flush walks final
// reachable roots, so batch imports don't publish one tree version per record.
type nugRow struct {
	Key   string          `json:"k"`
	Value json.RawMessage `json:"v"`
}
type nugChild struct {
	Max  string `json:"max"`
	Hash string `json:"ref"`
}
type nugNode struct {
	Children []nugChild `json:"children,omitempty"`
	Rows     []nugRow   `json:"-"`
}
type nugBlocks struct {
	messageContent    map[string][]messageContentPart
	messageCacheBytes int
	objects           messageObjects
	cache             map[string]nugNode
	pending           map[string][]byte
	Uploaded          int
	UploadedBytes     int64
}

func newNugBlocks(objects messageObjects) *nugBlocks {
	return &nugBlocks{objects: objects, cache: map[string]nugNode{}, pending: map[string][]byte{}, messageContent: map[string][]messageContentPart{}}
}

func gzipNug(data []byte) ([]byte, error) {
	var buffer bytes.Buffer
	w, err := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(data); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
func ungzipNug(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, nugBlockLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > nugBlockLimit {
		return nil, invalid("decompressed nug block exceeds 1 MiB")
	}
	return raw, nil
}
func nodeKey(hash string) string { return "catalog/nodes/" + hash + ".jsonl.gz" }
func validNugHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func encodeNugNode(node nugNode) []byte {
	var out bytes.Buffer
	out.Write(encodeMessageJSON(struct {
		Schema   int        `json:"schema"`
		Children []nugChild `json:"children,omitempty"`
	}{1, node.Children}))
	out.WriteByte('\n')
	for _, row := range node.Rows {
		out.Write(encodeMessageJSON(row))
		out.WriteByte('\n')
	}
	return out.Bytes()
}
func (b *nugBlocks) node(ctx context.Context, hash string) (nugNode, error) {
	if hash == "" {
		return nugNode{}, nil
	}
	if !validNugHash(hash) {
		return nugNode{}, invalid("invalid nug node digest")
	}
	if node, ok := b.cache[hash]; ok {
		return node, nil
	}
	data, version, err := b.objects.get(ctx, nodeKey(hash))
	if err != nil {
		return nugNode{}, err
	}
	if version == "" || messageHash(data) != hash {
		return nugNode{}, &OperationError{Code: "corrupt_content", Message: "missing or corrupt nug index block"}
	}
	raw, err := ungzipNug(data)
	if err != nil {
		return nugNode{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var header struct {
		Schema   int        `json:"schema"`
		Children []nugChild `json:"children"`
	}
	if err = decoder.Decode(&header); err != nil || header.Schema != 1 {
		return nugNode{}, invalid("unsupported nug index block")
	}
	node := nugNode{Children: header.Children}
	for {
		var row nugRow
		err = decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return node, err
		}
		node.Rows = append(node.Rows, row)
	}
	if len(node.Rows) > nugFanout || len(node.Children) > nugFanout || len(node.Rows) > 0 && len(node.Children) > 0 {
		return node, invalid("invalid nug index fanout")
	}
	last := ""
	for _, row := range node.Rows {
		if row.Key == "" || row.Key <= last || len(row.Value) > nugValueLimit {
			return node, invalid("invalid nug row ordering/size")
		}
		last = row.Key
	}
	last = ""
	for _, child := range node.Children {
		if child.Max <= last || !validNugHash(child.Hash) {
			return node, invalid("invalid nug child ordering")
		}
		last = child.Max
	}
	b.cache[hash] = node
	return node, nil
}
func (b *nugBlocks) stage(node nugNode) (nugChild, error) {
	if len(node.Rows) == 0 && len(node.Children) == 0 {
		return nugChild{}, nil
	}
	raw := encodeNugNode(node)
	if len(raw) > nugBlockLimit {
		return nugChild{}, invalid("nug index block exceeds budget")
	}
	data, err := gzipNug(raw)
	if err != nil {
		return nugChild{}, err
	}
	hash := messageHash(data)
	b.cache[hash] = node
	b.pending[hash] = data
	max := ""
	if len(node.Rows) > 0 {
		max = node.Rows[len(node.Rows)-1].Key
	} else {
		max = node.Children[len(node.Children)-1].Max
	}
	return nugChild{max, hash}, nil
}
func (b *nugBlocks) get(ctx context.Context, root, key string) (json.RawMessage, error) {
	for depth := 0; root != ""; depth++ {
		if depth > 32 {
			return nil, invalid("nug index depth exceeded")
		}
		node, err := b.node(ctx, root)
		if err != nil {
			return nil, err
		}
		if len(node.Children) == 0 {
			i := sort.Search(len(node.Rows), func(i int) bool { return node.Rows[i].Key >= key })
			if i < len(node.Rows) && node.Rows[i].Key == key {
				return node.Rows[i].Value, nil
			}
			return nil, nil
		}
		i := sort.Search(len(node.Children), func(i int) bool { return node.Children[i].Max >= key })
		if i == len(node.Children) {
			return nil, nil
		}
		root = node.Children[i].Hash
	}
	return nil, nil
}
func (b *nugBlocks) set(ctx context.Context, root, key string, value []byte) (string, error) {
	if key == "" || len(key) > 4096 || len(value) > nugValueLimit {
		return "", invalid("nug row exceeds key/value budget")
	}
	children, err := b.change(ctx, root, key, value, 0)
	if err != nil {
		return "", err
	}
	if len(children) == 0 {
		return "", nil
	}
	if len(children) == 1 {
		return children[0].Hash, nil
	}
	parent, err := b.stage(nugNode{Children: children})
	return parent.Hash, err
}
func (b *nugBlocks) change(ctx context.Context, root, key string, value []byte, depth int) ([]nugChild, error) {
	if depth > 32 {
		return nil, invalid("nug index depth exceeded")
	}
	node, err := b.node(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(node.Children) == 0 {
		rows := append([]nugRow{}, node.Rows...)
		i := sort.Search(len(rows), func(i int) bool { return rows[i].Key >= key })
		if i < len(rows) && rows[i].Key == key {
			if bytes.Equal(rows[i].Value, value) {
				return b.unchanged(root, node), nil
			}
			if value == nil {
				rows = append(rows[:i], rows[i+1:]...)
			} else {
				rows[i] = nugRow{key, bytes.Clone(value)}
			}
		} else if value != nil {
			rows = append(rows, nugRow{})
			copy(rows[i+1:], rows[i:])
			rows[i] = nugRow{key, bytes.Clone(value)}
		} else {
			return b.unchanged(root, node), nil
		}
		node.Rows = rows
	} else {
		children := append([]nugChild{}, node.Children...)
		i := sort.Search(len(children), func(i int) bool { return children[i].Max >= key })
		if i == len(children) {
			i--
		}
		replacements, err := b.change(ctx, children[i].Hash, key, value, depth+1)
		if err != nil {
			return nil, err
		}
		updated := append([]nugChild{}, children[:i]...)
		updated = append(updated, replacements...)
		updated = append(updated, children[i+1:]...)
		node.Children = updated
	}
	count := len(node.Rows) + len(node.Children)
	if count == 0 {
		return nil, nil
	}
	if count <= nugFanout && len(encodeNugNode(node)) <= nugBlockLimit {
		child, err := b.stage(node)
		return []nugChild{child}, err
	}
	middle := count / 2
	left, right := nugNode{}, nugNode{}
	if len(node.Rows) > 0 {
		// Split by bytes as well as count. A cluster of large values on one
		// side of a count-only split could still exceed the block budget.
		total := 0
		for _, row := range node.Rows {
			total += len(encodeMessageJSON(row)) + 1
		}
		used := 0
		for i, row := range node.Rows {
			used += len(encodeMessageJSON(row)) + 1
			if used >= total/2 {
				middle = i + 1
				break
			}
		}
		if middle >= len(node.Rows) {
			middle = len(node.Rows) - 1
		}
		if middle < 1 {
			middle = 1
		}
		left.Rows, right.Rows = node.Rows[:middle], node.Rows[middle:]
	} else {
		left.Children, right.Children = node.Children[:middle], node.Children[middle:]
	}
	a, err := b.stage(left)
	if err != nil {
		return nil, err
	}
	c, err := b.stage(right)
	return []nugChild{a, c}, err
}
func (b *nugBlocks) unchanged(root string, node nugNode) []nugChild {
	if root == "" {
		return nil
	}
	max := ""
	if len(node.Rows) > 0 {
		max = node.Rows[len(node.Rows)-1].Key
	} else if len(node.Children) > 0 {
		max = node.Children[len(node.Children)-1].Max
	}
	return []nugChild{{max, root}}
}
func (b *nugBlocks) scan(ctx context.Context, root, after string, limit int) ([]nugRow, error) {
	rows := []nugRow{}
	var walk func(string, int) error
	walk = func(hash string, depth int) error {
		if hash == "" || len(rows) >= limit {
			return nil
		}
		if depth > 32 {
			return invalid("nug index depth exceeded")
		}
		node, err := b.node(ctx, hash)
		if err != nil {
			return err
		}
		for _, row := range node.Rows {
			if row.Key > after {
				rows = append(rows, row)
				if len(rows) >= limit {
					return nil
				}
			}
		}
		for _, child := range node.Children {
			if child.Max > after {
				if err := walk(child.Hash, depth+1); err != nil {
					return err
				}
				if len(rows) >= limit {
					return nil
				}
			}
		}
		return nil
	}
	err := walk(root, 0)
	return rows, err
}
func (b *nugBlocks) all(ctx context.Context, root string) ([]nugRow, error) {
	return b.scan(ctx, root, "", int(^uint(0)>>1))
}
func (b *nugBlocks) flush(ctx context.Context, roots ...string) error {
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(hash string) error {
		if hash == "" || seen[hash] {
			return nil
		}
		seen[hash] = true
		data, ok := b.pending[hash]
		if !ok {
			return nil
		}
		node := b.cache[hash]
		for _, child := range node.Children {
			if err := walk(child.Hash); err != nil {
				return err
			}
		}
		if err := immutableObject(ctx, b.objects, nodeKey(hash), data); err != nil {
			return err
		}
		b.Uploaded++
		b.UploadedBytes += int64(len(data))
		return nil
	}
	for _, root := range roots {
		if err := walk(root); err != nil {
			return err
		}
	}
	return nil
}
func (b *nugBlocks) prune(roots ...string) {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(hash string) {
		if hash == "" || seen[hash] {
			return
		}
		seen[hash] = true
		if _, ok := b.pending[hash]; !ok {
			return
		}
		for _, child := range b.cache[hash].Children {
			walk(child.Hash)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	for hash := range b.pending {
		if !seen[hash] {
			delete(b.pending, hash)
			delete(b.cache, hash)
		}
	}
}

type nugRoot struct {
	Schema     int    `json:"schema"`
	Workspace  string `json:"workspace_ref"`
	Kind       string `json:"kind"`
	Epoch      string `json:"epoch"`
	Generation uint64 `json:"generation"`
	High       uint64 `json:"high"`
	Sessions   string `json:"sessions,omitempty"`
	Latest     string `json:"latest,omitempty"`
	Events     string `json:"events,omitempty"`
	Content    string `json:"content,omitempty"`
}

func loadNugRoot(ctx context.Context, objects messageObjects, workspace, kind, snapshot string, allowMissing bool) (nugRoot, string, string, error) {
	root := nugRoot{Schema: 2, Workspace: workspace, Kind: kind}
	key := nugRootKey
	if snapshot != "" {
		if !validNugHash(snapshot) {
			return root, "", "", invalid("invalid nug snapshot digest")
		}
		key = "catalog/roots/" + snapshot + ".json"
	}
	data, version, err := objects.get(ctx, key)
	if err != nil {
		return root, "", "", err
	}
	if version == "" {
		if allowMissing && snapshot == "" {
			_, published, markerErr := objects.get(ctx, "catalog/published.json")
			if markerErr != nil {
				return root, "", "", markerErr
			}
			if published != "" {
				return root, "", "", &OperationError{Code: "catalog_unavailable", Message: "previously published catalog root is missing; explicit recovery required"}
			}
			return root, "", "", nil
		}
		return root, "", "", &OperationError{Code: "catalog_unavailable", Message: "nug catalog is missing; explicit initialization or recovery is required"}
	}
	if len(data) > 4096 || snapshot != "" && messageHash(data) != snapshot {
		return root, "", "", invalid("invalid nug root size/digest")
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return root, "", "", err
	}
	if root.Schema != 2 || root.Workspace != workspace || root.Kind != kind || root.Epoch == "" {
		return root, "", "", invalid("unsupported or misbound nug root")
	}
	marker, markerVersion, markerErr := objects.get(ctx, "catalog/published.json")
	if markerErr != nil {
		return root, "", "", markerErr
	}
	if markerVersion != "" {
		var identity struct {
			Epoch string `json:"epoch"`
		}
		if err := json.Unmarshal(marker, &identity); err != nil {
			return root, "", "", err
		}
		if identity.Epoch != root.Epoch {
			return root, "", "", &OperationError{Code: "catalog_reset", Message: "catalog epoch differs from established publication identity"}
		}
	}
	for _, hash := range []string{root.Sessions, root.Latest, root.Events, root.Content} {
		if hash != "" && !validNugHash(hash) {
			return root, "", "", invalid("invalid nug root index reference")
		}
	}
	return root, version, messageHash(data), nil
}
func publishNugRoot(ctx context.Context, objects messageObjects, root nugRoot, expected string) (string, string, error) {
	data := encodeMessageJSON(root)
	if len(data) > 4096 {
		return "", "", invalid("nug root exceeds 4 KiB")
	}
	snapshot := messageHash(data)
	if err := immutableObject(ctx, objects, "catalog/roots/"+snapshot+".json", data); err != nil {
		return snapshot, "", err
	}
	version, err := objects.cas(ctx, nugRootKey, expected, data, nil)
	if err == nil {
		err = immutableObject(ctx, objects, "catalog/published.json", encodeMessageJSON(struct {
			Schema int    `json:"schema"`
			Epoch  string `json:"epoch"`
		}{1, root.Epoch}))
	}
	return snapshot, version, err
}
func ordinalKey(n uint64) string { return fmt.Sprintf("%020d", n) }
