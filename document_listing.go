package nuggets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
)

const defaultDocumentLimit = 20

type DocumentPage struct {
	Snapshot  string `json:"snapshot"`
	Prefix    string `json:"prefix,omitempty"`
	Total     int    `json:"total"`
	NextToken string `json:"next_token,omitempty"`
}

type documentCursor struct {
	Version   int    `json:"version"`
	Store     string `json:"store"`
	Workspace string `json:"workspace"`
	Snapshot  string `json:"snapshot"`
	Prefix    string `json:"prefix"`
	After     string `json:"after"`
}

// ListPage pins continuation to one reachable snapshot. Enumerate names first;
// read document bodies only for the selected page's metadata, never the whole
// collection merely to discover its first few resources.
func (s Store) ListPage(ctx context.Context, prefix, snapshot, token string, limit int, all bool) ([]Resource, *DocumentPage, error) {
	if prefix != "" {
		if err := validateName(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, nil, err
		}
	}
	if limit == 0 {
		limit = defaultDocumentLimit
	}
	if limit < 1 || limit > 100 {
		return nil, nil, invalid("documents list limit must be 1–100; use --all for an explicit full inventory")
	}
	digest := sha256.Sum256([]byte(s.Remote))
	identity := hex.EncodeToString(digest[:])
	cursor := documentCursor{}
	if token != "" {
		if all || len(token) > 8192 {
			return nil, nil, invalid("invalid document page token or --all combination")
		}
		data, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, nil, invalid("invalid document page token")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 ||
			cursor.Store != identity || cursor.Workspace != s.Workspace || !snapshotPattern.MatchString(cursor.Snapshot) ||
			cursor.After == "" || validateName(cursor.After) != nil || !strings.HasPrefix(cursor.After, cursor.Prefix) ||
			(prefix != "" && prefix != cursor.Prefix) || (snapshot != "" && snapshot != cursor.Snapshot) {
			return nil, nil, invalid("document page token does not match this workspace, prefix or snapshot")
		}
		prefix, snapshot = cursor.Prefix, cursor.Snapshot
	}
	t, head, err := s.runTransaction(ctx, snapshot)
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(t.dir)
	page := &DocumentPage{Snapshot: head, Prefix: prefix}
	resources := []Resource{}
	if head == "" {
		return resources, page, nil
	}
	out, err := t.git(ctx, nil, "ls-tree", "-rz", "--name-only", head, "--", "documents/")
	if err != nil {
		return nil, nil, err
	}
	names := []string{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		name := strings.TrimPrefix(string(entry), "documents/")
		if err := validateName(name); err != nil {
			return nil, nil, err
		}
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	page.Total = len(names)
	start := 0
	if token != "" {
		start = sort.SearchStrings(names, cursor.After)
		if start == len(names) || names[start] != cursor.After {
			return nil, nil, invalid("document page token position is absent from its snapshot")
		}
		start++
	}
	end := len(names)
	if !all && end-start > limit {
		end = start + limit
		cursor = documentCursor{Version: 1, Store: identity, Workspace: s.Workspace, Snapshot: head, Prefix: prefix, After: names[end-1]}
		encoded, err := json.Marshal(cursor)
		if err != nil {
			return nil, nil, err
		}
		page.NextToken = base64.RawURLEncoding.EncodeToString(encoded)
	}
	for _, name := range names[start:end] {
		doc, err := readAt(ctx, t, head, name)
		if err != nil {
			return nil, nil, err
		}
		resources = append(resources, doc.Resource)
	}
	return resources, page, nil
}
