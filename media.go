package nuggets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

const mediaSizeLimit = 64 << 20

type MediaRecord struct {
	ID       string   `json:"id"`
	Revision string   `json:"revision"`
	Name     string   `json:"name"`
	MIME     string   `json:"mime"`
	Category string   `json:"category"`
	Source   string   `json:"source,omitempty"`
	Size     int      `json:"size"`
	SHA256   string   `json:"sha256"`
	Width    int      `json:"width,omitempty"`
	Height   int      `json:"height,omitempty"`
	Chunks   []string `json:"chunks"`
}
type MediaResult struct {
	Target        string        `json:"target"`
	Snapshot      string        `json:"snapshot"`
	Record        *MediaRecord  `json:"record,omitempty"`
	Records       []MediaRecord `json:"records,omitempty"`
	NextToken     string        `json:"next_token,omitempty"`
	ContentBase64 string        `json:"content_base64,omitempty"`
	OutputFile    string        `json:"output_file,omitempty"`
	Changed       bool          `json:"changed,omitempty"`
	Verified      bool          `json:"verified"`
}
type MediaStore struct {
	Workspace, Target string
	objects           messageObjects
}

func NewMediaStore(reference, target string, profile MessageTarget) (*MediaStore, error) {
	if !validWorkspaceReference(reference) || validateMessageLabel(target) != nil {
		return nil, invalid("media requires stable workspace and named target")
	}
	objects, err := newNugS3(profile, reference, "media")
	if err != nil {
		return nil, err
	}
	return &MediaStore{reference, target, objects}, nil
}
func (s *MediaStore) Import(ctx context.Context, id, name, category, source, expected string, data []byte) (*MediaResult, error) {
	if id == "" || len(id) > 1024 || strings.ContainsAny(id, "\x00\r\n") {
		return nil, invalid("media needs a logical ID of 1–1024 characters")
	}
	if len(data) > mediaSizeLimit {
		return nil, invalid("media exceeds supported 64 MiB")
	}
	if len(name) > 1024 || len(category) > 100 || len(source) > 4096 {
		return nil, invalid("media metadata exceeds budget")
	}
	if category == "" {
		category = "file"
	}
	if name == "" {
		name = id
	}
	kind := http.DetectContentType(data)
	if kind == "application/octet-stream" {
		if inferred := mime.TypeByExtension(filepath.Ext(name)); inferred != "" {
			kind = inferred
		}
	}
	record := MediaRecord{ID: id, Name: name, MIME: kind, Category: category, Source: source, Size: len(data), SHA256: messageHash(data)}
	if strings.HasPrefix(kind, "image/") {
		if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			record.Width, record.Height = config.Width, config.Height
		}
	}
	// Revision is independent of storage chunking/codec and protects metadata edits.
	record.Revision = messageDigest(record)
	result := &MediaResult{Target: s.Target, Record: &record}
	for attempt := 0; attempt < 8; attempt++ {
		root, version, snapshot, err := loadNugRoot(ctx, s.objects, s.Workspace, "media", "", true)
		if err != nil {
			return result, err
		}
		blocks := newNugBlocks(s.objects)
		before, err := blocks.get(ctx, root.Sessions, id)
		if err != nil {
			return result, err
		}
		if before != nil {
			var old MediaRecord
			if err := json.Unmarshal(before, &old); err != nil {
				return result, err
			}
			if old.Revision == record.Revision {
				result.Record = &old
				result.Snapshot = snapshot
				_, err := s.read(ctx, old)
				result.Verified = err == nil
				return result, err
			}
			if expected != old.Revision {
				return result, &OperationError{Code: "conflict", Message: "media revision changed; supply observed expected revision"}
			}
		} else if expected != "" {
			return result, &OperationError{Code: "conflict", Message: "expected media revision is absent"}
		}
		if root.Epoch == "" {
			root.Epoch, err = randomNugID()
			if err != nil {
				return result, err
			}
		}
		// Content manifest index shares identical bytes across distinct logical IDs.
		manifest, err := blocks.get(ctx, root.Content, record.SHA256)
		if err != nil {
			return result, err
		}
		if manifest != nil {
			if err := json.Unmarshal(manifest, &record.Chunks); err != nil {
				return result, err
			}
		} else {
			record.Chunks, err = writeNugBytes(ctx, s.objects, data)
			if err != nil {
				return result, err
			}
			root.Content, err = blocks.set(ctx, root.Content, record.SHA256, encodeMessageJSON(record.Chunks))
			if err != nil {
				return result, err
			}
		}
		root.Sessions, err = blocks.set(ctx, root.Sessions, id, encodeMessageJSON(record))
		if err != nil {
			return result, err
		}
		root.Generation++
		if err := blocks.flush(ctx, root.Sessions, root.Content); err != nil {
			return result, err
		}
		snapshot, _, err = publishNugRoot(ctx, s.objects, root, version)
		if errors.Is(err, errMessageCAS) {
			continue
		}
		if err != nil {
			return result, err
		}
		result.Snapshot, result.Changed = snapshot, true
		verified, err := s.Read(ctx, id, snapshot, false)
		if err != nil {
			return result, err
		}
		result.Record, result.Verified = verified.Record, true
		return result, nil
	}
	return result, &OperationError{Code: "conflict", Message: "media catalog kept changing; retry"}
}
func (s *MediaStore) read(ctx context.Context, record MediaRecord) ([]byte, error) {
	data, err := readNugBytes(ctx, s.objects, record.Chunks, mediaSizeLimit)
	if err != nil {
		return nil, err
	}
	if len(data) != record.Size || messageHash(data) != record.SHA256 {
		return nil, &OperationError{Code: "corrupt_content", Message: "media content digest/size mismatch"}
	}
	return data, nil
}
func (s *MediaStore) Read(ctx context.Context, id, snapshot string, includeBytes bool) (*MediaResult, error) {
	root, _, snapshot, err := loadNugRoot(ctx, s.objects, s.Workspace, "media", snapshot, false)
	if err != nil {
		return nil, err
	}
	blocks := newNugBlocks(s.objects)
	value, err := blocks.get(ctx, root.Sessions, id)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, &OperationError{Code: "not_found", Message: "media resource not found"}
	}
	var record MediaRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return nil, err
	}
	data, err := s.read(ctx, record)
	if err != nil {
		return nil, err
	}
	result := &MediaResult{Target: s.Target, Snapshot: snapshot, Record: &record, Verified: true}
	if includeBytes {
		result.ContentBase64 = base64.StdEncoding.EncodeToString(data)
	}
	return result, nil
}
func (s *MediaStore) List(ctx context.Context, token string, limit int) (*MediaResult, error) {
	if limit < 1 || limit > 1000 {
		return nil, invalid("media list limit must be 1–1000")
	}
	var cursor struct {
		Snapshot string `json:"snapshot"`
		After    string `json:"after"`
	}
	if token != "" {
		if len(token) > 8192 {
			return nil, invalid("media page token too large")
		}
		data, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, invalid("invalid media page token")
		}
		if err := json.Unmarshal(data, &cursor); err != nil {
			return nil, err
		}
	}
	root, _, pin, err := loadNugRoot(ctx, s.objects, s.Workspace, "media", cursor.Snapshot, false)
	if err != nil {
		return nil, err
	}
	blocks := newNugBlocks(s.objects)
	rows, err := blocks.scan(ctx, root.Sessions, cursor.After, limit+1)
	if err != nil {
		return nil, err
	}
	result := &MediaResult{Target: s.Target, Snapshot: pin, Records: []MediaRecord{}, Verified: true}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	for _, row := range rows {
		var record MediaRecord
		if err := json.Unmarshal(row.Value, &record); err != nil {
			return nil, err
		}
		result.Records = append(result.Records, record)
	}
	if more {
		cursor.Snapshot, cursor.After = pin, rows[len(rows)-1].Key
		result.NextToken = base64.RawURLEncoding.EncodeToString(encodeMessageJSON(cursor))
	}
	return result, nil
}
