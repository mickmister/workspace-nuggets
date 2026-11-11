package nuggets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMediaBinarySharingPinnedPagingAndConflicts(t *testing.T) {
	objects := &memoryMessageObjects{data: map[string][]byte{}}
	s := &MediaStore{messageTestReference, "fixture", objects}
	ctx := context.Background()
	data := make([]byte, 2<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	first, err := s.Import(ctx, "a", "clip.bin", "media", "run:fixture", "", data)
	if err != nil || !first.Verified {
		t.Fatal("import", err)
	}
	if again, err := s.Import(ctx, "a", "clip.bin", "media", "run:fixture", "", data); err != nil || again.Changed {
		t.Fatal("same import not idempotent", err)
	}
	if _, err := s.Import(ctx, "b", "other.bin", "media", "run:fixture", "", data); err != nil {
		t.Fatal(err)
	}
	chunks := 0
	for key := range objects.data {
		if strings.HasPrefix(key, "objects/") {
			chunks++
		}
	}
	if chunks != 2 {
		t.Fatal("duplicate binary bytes not shared", chunks)
	}
	page, err := s.List(ctx, "", 1)
	if err != nil || len(page.Records) != 1 || page.NextToken == "" {
		t.Fatal("list", page, err)
	}
	if _, err := s.Import(ctx, "a", "changed.bin", "file", "", "", []byte("different")); err == nil {
		t.Fatal("metadata/content update ignored expected revision")
	}
	if _, err := s.Import(ctx, "a", "changed.bin", "file", "", first.Record.Revision, []byte("different")); err != nil {
		t.Fatal(err)
	}
	replay, err := s.Read(ctx, "a", first.Snapshot, true)
	if err != nil || replay.ContentBase64 != base64.StdEncoding.EncodeToString(data) {
		t.Fatal("pinned binary changed", err)
	}
	next, err := s.List(ctx, page.NextToken, 1)
	if err != nil || len(next.Records) != 1 || next.Records[0].ID != "b" {
		t.Fatal("pinned list changed", err)
	}
	root := objects.data[nugRootKey]
	if len(root) > 4096 || bytes.Contains(root, []byte("clip.bin")) {
		t.Fatal("catalog root embeds records")
	}
	objects.mu.Lock()
	objects.data[nugBinaryKey(first.Record.Chunks[0])] = []byte("corrupt")
	objects.mu.Unlock()
	if _, err := s.Read(ctx, "b", "", false); err == nil {
		t.Fatal("corrupt media verified")
	}
}
func TestMediaScreenshotDimensionsAndType(t *testing.T) {
	objects := &memoryMessageObjects{data: map[string][]byte{}}
	s := &MediaStore{messageTestReference, "fixture", objects}
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(context.Background(), "screen", "screen.png", "screenshot", "fixture", "", out.Bytes())
	if err != nil || result.Record.MIME != "image/png" || result.Record.Width != 3 || result.Record.Height != 2 {
		t.Fatal("screenshot metadata", result, err)
	}
}
func TestMediaActualS3CLIAndMCP(t *testing.T) {
	profile := actualNugProfile(t)
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registryPath, _ := RegistryPath()
	registry := Registry{Version: 1, Bindings: []WorkspaceBinding{{WorkspaceContext: WorkspaceContext{Reference: messageTestReference, Workspace: "fixture", Engagement: "fixture"}, Remote: "https://example.invalid/not-used.git", CacheDir: t.TempDir()}}, Attachments: []workspaceAttachment{{Root: root, Reference: messageTestReference}}}
	if err := saveJSON(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	configPath, _ := ConfigPath()
	if err := saveConfig(configPath, Config{MessageTargets: map[string]MessageTarget{"minio": profile}}); err != nil {
		t.Fatal(err)
	}
	cli := func(input string, args ...string) Response {
		t.Helper()
		var out, diagnostics bytes.Buffer
		if code := RunCLI(context.Background(), args, strings.NewReader(input), &out, &diagnostics); code != 0 {
			t.Fatalf("CLI %v: %d %s %s", args, code, out.String(), diagnostics.String())
		}
		var response Response
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	cli("", "media", "bind", "--target", "minio")
	uploaded := cli("exact binary\x00\xff\n", "media", "import", "--target", "minio", "--name", "sample", "--filename", "sample.bin", "--category", "media", "--file", "-")
	if uploaded.Media == nil || !uploaded.Media.Verified {
		t.Fatal("CLI media import")
	}
	service := Service{RegistryPath: registryPath, ContextDir: root}
	server := NewMCPServer(service)
	a, b := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "media-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "media_read", Arguments: map[string]any{"target": "minio", "name": "sample"}})
	if err != nil || call.IsError {
		t.Fatal("MCP read", call, err)
	}
	encoded, _ := json.Marshal(call.StructuredContent)
	var response Response
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(response.Media.ContentBase64)
	if err != nil || string(raw) != "exact binary\x00\xff\n" {
		t.Fatal("CLI/MCP binary mismatch", err)
	}
	output := filepath.Join(root, "output.bin")
	cli("", "media", "read", "--target", "minio", "--name", "sample", "--file", output)
	types := cli("", "types", "list")
	if !strings.Contains(strings.Join(types.NugTypes, ","), "media") {
		t.Fatal("media undiscoverable")
	}
}
