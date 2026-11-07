package nuggets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type gitServer struct {
	*rig
	url        string
	repo       string
	mu         sync.Mutex
	beforePush func()
	buildEnv   []string
}

func newGitServer(t *testing.T) *gitServer {
	t.Helper()
	r := newRig(t)
	repo := r.initGitRemote()
	r.run(r.root, "", "git", "--git-dir", repo, "config", "http.receivepack", "true")
	backend := filepath.Join(r.run(r.root, "", "git", "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Fatalf("Git smart HTTP backend: %v", err)
	}
	env := append([]string{}, r.env...)
	env = append(env, "GIT_PROJECT_ROOT="+r.root, "GIT_HTTP_EXPORT_ALL=1")
	cgiHandler := &cgi.Handler{Path: backend, Dir: r.root, Env: env}
	g := &gitServer{rig: r, repo: repo, buildEnv: os.Environ()}
	// Direct Go operations get the same isolated user config as subprocess clients.
	for _, variable := range r.env {
		key, value, _ := strings.Cut(variable, "=")
		if key == "HOME" || key == "XDG_CONFIG_HOME" {
			t.Setenv(key, value)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/git-receive-pack") {
			g.mu.Lock()
			before := g.beforePush
			g.beforePush = nil
			g.mu.Unlock()
			if before != nil {
				before()
			}
		}
		cgiHandler.ServeHTTP(w, req)
	}))
	t.Cleanup(server.Close)
	g.url = server.URL + "/remote.git"
	return g
}

func (g *gitServer) store(workspace string) Store {
	return Store{Remote: g.url, Workspace: workspace, CacheDir: g.t.TempDir()}
}

func requireUpsert(t *testing.T, s Store, name, version string, content []byte) Receipt {
	t.Helper()
	receipt, err := s.Upsert(context.Background(), name, version, content)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestDocumentsOverEphemeralGitServer(t *testing.T) {
	g := newGitServer(t)
	a, b := g.store("workspace-one"), g.store("workspace-one")
	sourceHead := g.run(g.root, "", "git", "--git-dir", g.repo, "rev-parse", "refs/heads/main")
	ctx := context.Background()
	resources, err := a.List(ctx)
	if err != nil || len(resources) != 0 {
		t.Fatalf("empty workspace: %v, %v", resources, err)
	}
	content := []byte{0, 255, '\n', 10, 42}
	created := requireUpsert(t, a, "reviews/日本語.bin", "", content)
	doc, err := b.Read(ctx, created.Name)
	if err != nil || !bytes.Equal(doc.Content, content) || doc.Version != created.Version || doc.SHA256 != created.SHA256 {
		t.Fatalf("cross-client exact binary read: %+v, %v", doc, err)
	}
	resources, err = b.List(ctx)
	if err != nil || len(resources) != 1 || resources[0].URI != ResourceURI(a.Workspace, created.Name) {
		t.Fatalf("browse: %+v, %v", resources, err)
	}
	unchanged := requireUpsert(t, b, created.Name, created.Version, content)
	if unchanged.Changed || unchanged.Commit != created.Commit {
		t.Fatalf("unchanged upsert created a revision: %+v", unchanged)
	}
	updated := requireUpsert(t, b, created.Name, created.Version, []byte("new content\n"))
	_, err = a.Upsert(ctx, created.Name, created.Version, []byte("stale writer"))
	var conflict *OperationError
	if !errors.As(err, &conflict) || conflict.Code != "conflict" || conflict.Conflict.ActualVersion != updated.Version {
		t.Fatalf("stale writer must return current version: %v", err)
	}
	if got := g.run(g.root, "", "git", "--git-dir", conflict.Conflict.LocalRepository, "show", "refs/nugs/intent:documents/"+created.Name); got != "stale writer" {
		t.Fatalf("stale input not preserved: %q", got)
	}
	_, err = a.Upsert(ctx, created.Name, "", []byte("duplicate creation"))
	if !errors.As(err, &conflict) || conflict.Code != "conflict" {
		t.Fatalf("create-only must reject existing resource: %v", err)
	}
	isolated := g.store("workspace-two")
	other := requireUpsert(t, isolated, created.Name, "", []byte{})
	if other.Size != 0 {
		t.Fatal("empty content was not preserved")
	}
	doc, err = a.Read(ctx, created.Name)
	if err != nil || string(doc.Content) != "new content\n" {
		t.Fatalf("workspace isolation: %+v, %v", doc, err)
	}
	_, err = a.Read(ctx, "missing.md")
	if !errors.As(err, &conflict) || conflict.Code != "not_found" {
		t.Fatalf("missing resource: %v", err)
	}
	for _, name := range []string{"../escape", "/absolute", "a/../b", ".git/config", "a\x00b"} {
		if _, err := a.Upsert(ctx, name, "", nil); err == nil {
			t.Fatalf("accepted unsafe resource name %q", name)
		}
	}
	if got := g.run(g.root, "", "git", "--git-dir", g.repo, "rev-parse", "refs/heads/main"); got != sourceHead {
		t.Fatal("document operations modified the source branch")
	}
}

func TestDocumentsOnEmptyGitServer(t *testing.T) {
	g := newGitServer(t)
	g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", "refs/heads/main")
	store := g.store("empty")
	created := requireUpsert(t, store, "review.md", "", []byte("no source branch required"))
	if _, err := store.Read(context.Background(), created.Name); err != nil {
		t.Fatal(err)
	}
	if got := g.run(g.root, "", "git", "--git-dir", g.repo, "for-each-ref", "--format=%(refname)", "refs/heads/"); got != "" {
		t.Fatalf("document backend bootstrapped a source branch: %s", got)
	}
}

func TestLocalPathAndCancellation(t *testing.T) {
	g := newGitServer(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, g.repo)
	if err != nil {
		t.Fatal(err)
	}
	store := g.store("local")
	store.Remote = relative
	receipt := requireUpsert(t, store, "review.md", "", []byte("relative local remote"))
	if doc, err := store.Read(context.Background(), receipt.Name); err != nil || doc.Version != receipt.Version {
		t.Fatalf("relative remote was resolved against transaction directory: %+v, %v", doc, err)
	}
	cache := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := (Service{Remote: g.url, CacheDir: cache}).Execute(ctx, Request{Operation: "documents.list", Workspace: "canceled"})
	if response.Error == nil || response.Error.Code != "canceled" {
		t.Fatalf("cancellation not propagated: %+v", response)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled transaction leaked: %v, %v", entries, err)
	}
}

func TestDocumentReconciliationOverHTTP(t *testing.T) {
	base := "start\n1\n2\n3\n4\n5\n6\n7\n8\n9\nend\n"
	for _, scenario := range []string{"independent-documents", "independent-initial-documents", "clean-text-merge", "conflicting-text", "concurrent-create"} {
		t.Run(scenario, func(t *testing.T) {
			g := newGitServer(t)
			a, b := g.store("race"), g.store("race")
			ctx := context.Background()
			var version string
			if scenario != "concurrent-create" && scenario != "independent-initial-documents" {
				version = requireUpsert(t, a, "review.md", "", []byte(base)).Version
			}
			localContent := strings.Replace(base, "start", "local-start", 1)
			remoteContent := strings.Replace(base, "end", "remote-end", 1)
			if scenario == "conflicting-text" || scenario == "concurrent-create" {
				remoteContent = strings.Replace(base, "start", "remote-start", 1)
			}
			writerDone := make(chan error, 1)
			g.mu.Lock()
			g.beforePush = func() {
				name, expected := "review.md", version
				if strings.HasPrefix(scenario, "independent-") {
					name, expected = "other.md", ""
				}
				_, writerErr := b.Upsert(ctx, name, expected, []byte(remoteContent))
				writerDone <- writerErr
			}
			g.mu.Unlock()
			receipt, err := a.Upsert(ctx, "review.md", version, []byte(localContent))
			if writerErr := <-writerDone; writerErr != nil {
				t.Fatalf("competing real operation failed: %v", writerErr)
			}
			if scenario == "conflicting-text" || scenario == "concurrent-create" {
				var operationError *OperationError
				if !errors.As(err, &operationError) || operationError.Code != "conflict" {
					t.Fatalf("expected genuine conflict, got %+v, %v", receipt, err)
				}
				evidence := operationError.Conflict
				if evidence.LocalRepository == "" || evidence.LocalCommit == "" || evidence.RemoteCommit == "" {
					t.Fatalf("missing recovery evidence: %+v", evidence)
				}
				local := g.run(g.root, "", "git", "--git-dir", evidence.LocalRepository, "show", "refs/nugs/intent:documents/review.md")
				if local != strings.TrimSpace(localContent) {
					t.Fatalf("local intent not pinned: %q", local)
				}
				remote, err := b.Read(ctx, "review.md")
				if err != nil || string(remote.Content) != remoteContent {
					t.Fatalf("conflict overwrote remote: %+v, %v", remote, err)
				}
				return
			}
			if err != nil || !receipt.Merged {
				t.Fatalf("clean reconciliation: %+v, %v", receipt, err)
			}
			doc, err := b.Read(ctx, "review.md")
			if err != nil || !strings.Contains(string(doc.Content), "local-start") {
				t.Fatalf("local edit lost: %+v, %v", doc, err)
			}
			if scenario == "clean-text-merge" && !strings.Contains(string(doc.Content), "remote-end") {
				t.Fatal("remote edit lost during clean merge")
			}
			if strings.HasPrefix(scenario, "independent-") {
				if _, err := a.Read(ctx, "other.md"); err != nil {
					t.Fatalf("independent remote document lost: %v", err)
				}
			}
		})
	}
}

func TestDocumentReconciliationRetryBudget(t *testing.T) {
	g := newGitServer(t)
	a, b := g.store("busy"), g.store("busy")
	ctx := context.Background()
	writerErrors := make(chan error, 3)
	var arm func(int)
	arm = func(attempt int) {
		g.mu.Lock()
		g.beforePush = func() {
			_, err := b.Upsert(ctx, fmt.Sprintf("other-%d.md", attempt), "", []byte("competing update"))
			writerErrors <- err
			if attempt < 3 {
				arm(attempt + 1)
			}
		}
		g.mu.Unlock()
	}
	arm(1)
	_, err := a.Upsert(ctx, "local.md", "", []byte("local intent"))
	var failure *OperationError
	if !errors.As(err, &failure) || failure.Code != "retry_exhausted" {
		t.Fatalf("retry budget was not enforced: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := <-writerErrors; err != nil {
			t.Fatalf("competing update: %v", err)
		}
	}
	if got := g.run(g.root, "", "git", "--git-dir", failure.Conflict.LocalRepository, "show", "refs/nugs/intent:documents/local.md"); got != "local intent" {
		t.Fatalf("retry exhaustion lost intent: %q", got)
	}
	resources, err := b.List(ctx)
	if err != nil || len(resources) != 3 {
		t.Fatalf("retry exhaustion damaged remote: %+v, %v", resources, err)
	}
}

func TestRejectedPublicationRetainsIntent(t *testing.T) {
	g := newGitServer(t)
	g.run(g.root, "", "git", "--git-dir", g.repo, "config", "http.receivepack", "false")
	_, err := g.store("rejected").Upsert(context.Background(), "review.md", "", []byte("retain me"))
	var operationError *OperationError
	if !errors.As(err, &operationError) || operationError.Code != "publish_failed" {
		t.Fatalf("publication failure: %v", err)
	}
	evidence := operationError.Conflict
	if got := g.run(g.root, "", "git", "--git-dir", evidence.LocalRepository, "show", "refs/nugs/intent:documents/review.md"); got != "retain me" {
		t.Fatalf("failed push lost intent: %q", got)
	}
}

func TestFailedFetchRetainsIntent(t *testing.T) {
	g := newGitServer(t)
	g.run(g.root, "", "git", "--git-dir", g.repo, "config", "http.uploadpack", "false")
	_, err := g.store("unreadable").Upsert(context.Background(), "review.md", "", []byte("saved before network"))
	var failure *OperationError
	if !errors.As(err, &failure) || failure.Code != "fetch_failed" {
		t.Fatalf("fetch failure: %v", err)
	}
	if got := g.run(g.root, "", "git", "--git-dir", failure.Conflict.LocalRepository, "show", "refs/nugs/intent:documents/review.md"); got != "saved before network" {
		t.Fatalf("initial fetch lost input: %q", got)
	}
}

func TestCLIAndStdioMCPOverHTTP(t *testing.T) {
	g := newGitServer(t)
	g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", "refs/heads/main")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "nugs")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/nugs")
	build.Env = g.buildEnv
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build nugs: %v\n%s", err, out)
	}
	cli := func(command, input string, extra ...string) (Response, error) {
		t.Helper()
		args := append(strings.Fields(command), "--remote", g.url, "--workspace", "cli-mcp", "--cache", t.TempDir())
		args = append(args, extra...)
		out, err := g.command(g.root, input, binary, args...)
		var response Response
		if decodeErr := json.Unmarshal([]byte(out), &response); decodeErr != nil {
			t.Fatalf("CLI output is not JSON: %s (%v, %v)", out, err, decodeErr)
		}
		return response, err
	}
	created, err := cli("documents upsert", "from CLI\n", "--name", "notes/review.md")
	if err != nil || created.Error != nil {
		t.Fatalf("CLI create: %+v, %v", created, err)
	}
	centralConfig, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(centralConfig, Config{Remote: g.url}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "mcp", "--remote", g.url, "--workspace", "cli-mcp", "--cache", t.TempDir())
	cmd.Env = g.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "nugs-integration", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("stdio MCP connect: %v, %s", err, stderr.String())
	}
	defer session.Close()
	if instructions := session.InitializeResult().Instructions; !strings.Contains(instructions, "intermediate artifacts") || !strings.Contains(instructions, "document_upsert") || !strings.Contains(instructions, "source-code branch") {
		t.Fatalf("MCP clients did not receive artifact workflow instructions: %q", instructions)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 13 {
		t.Fatalf("MCP tool discovery: %+v, %v", tools, err)
	}
	call := func(name string, args map[string]any) (*mcp.CallToolResult, Response) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("MCP %s: %v", name, err)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var response Response
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatalf("MCP structured response: %v", err)
		}
		return result, response
	}
	result, bootstrapped := call("storage_bootstrap", map[string]any{})
	if result.IsError || bootstrapped.Storage == nil || !bootstrapped.Storage.Changed || bootstrapped.Ref != storageBootstrapRef {
		t.Fatalf("MCP storage bootstrap: %+v", bootstrapped)
	}
	bootstrapCLI, err := cli("storage bootstrap", "")
	if err != nil || bootstrapCLI.Storage == nil || bootstrapCLI.Storage.Changed || bootstrapCLI.Commit != bootstrapped.Commit {
		t.Fatalf("CLI cannot observe MCP bootstrap: %+v, %v", bootstrapCLI, err)
	}
	_, listed := call("documents_list", map[string]any{"workspace": "cli-mcp"})
	if len(listed.Resources) != 1 || listed.Resources[0].Name != "notes/review.md" {
		t.Fatalf("MCP cannot browse CLI resource: %+v", listed)
	}
	_, defaultListed := call("documents_list", map[string]any{})
	if defaultListed.Workspace != "cli-mcp" || len(defaultListed.Resources) != 1 {
		t.Fatalf("MCP omitted workspace did not use server default: %+v", defaultListed)
	}
	_, read := call("document_read", map[string]any{"workspace": "cli-mcp", "name": "notes/review.md"})
	if read.Document == nil || read.Document.Text == nil || *read.Document.Text != "from CLI\n" {
		t.Fatalf("MCP read: %+v", read)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: ResourceURI("cli-mcp", "notes/review.md")})
	if err != nil || len(resource.Contents) != 1 || string(resource.Contents[0].Blob) != "from CLI\n" {
		t.Fatalf("MCP resource read: %+v, %v", resource, err)
	}
	result, updated := call("document_upsert", map[string]any{"workspace": "cli-mcp", "name": "notes/review.md", "expected_version": read.Document.Version, "text": "from MCP\n"})
	if result.IsError || updated.Receipt == nil {
		t.Fatalf("MCP update: %+v", updated)
	}
	cliRead, err := cli("documents read", "", "--name", "notes/review.md")
	if err != nil || cliRead.Document == nil || *cliRead.Document.Text != "from MCP\n" {
		t.Fatalf("CLI cannot read MCP update: %+v, %v", cliRead, err)
	}
	stale, err := cli("documents upsert", "stale", "--name", "notes/review.md", "--expected", created.Receipt.Version)
	if err == nil || stale.Error == nil || stale.Error.Code != "conflict" {
		t.Fatalf("CLI stale update: %+v, %v", stale, err)
	}
	result, staleMCP := call("document_upsert", map[string]any{"workspace": "cli-mcp", "name": "notes/review.md", "expected_version": created.Receipt.Version, "text": "stale MCP"})
	if !result.IsError || staleMCP.Error == nil || staleMCP.Error.Conflict == nil {
		t.Fatalf("MCP conflict lost structure: %+v", staleMCP)
	}
	for _, content := range [][]byte{{}, {0, 255, 42}} {
		name := fmt.Sprintf("binary-%d.bin", len(content))
		encoded := base64.StdEncoding.EncodeToString(content)
		result, response := call("document_upsert", map[string]any{"workspace": "cli-mcp", "name": name, "expected_version": "", "content_base64": encoded})
		if result.IsError || response.Receipt == nil {
			t.Fatalf("MCP binary create: %+v", response)
		}
		resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: ResourceURI("cli-mcp", name)})
		if err != nil || len(resource.Contents) != 1 || !bytes.Equal(resource.Contents[0].Blob, content) {
			t.Fatalf("MCP binary resource: %+v, %v", resource, err)
		}
	}
	sourceRepository(t, g, "source")
	provisioned, err := cli("workspace provision", "", "--root", g.root, "--repo", "source")
	if err != nil || provisioned.Manifest == nil || provisioned.Receipt == nil {
		t.Fatalf("CLI workspace provision: %+v, %v", provisioned, err)
	}
	result, inspected := call("workspace_inspect", map[string]any{"workspace": "cli-mcp"})
	if result.IsError || inspected.Manifest == nil || inspected.ManifestVersion != provisioned.Receipt.Version {
		t.Fatalf("MCP cannot inspect CLI manifest: %+v", inspected)
	}
	result, provisionedMCP := call("workspace_provision", map[string]any{"workspace": "cli-mcp", "root": g.root, "repositories": []string{"source"}, "expected_version": inspected.ManifestVersion})
	if result.IsError || provisionedMCP.Manifest == nil || provisionedMCP.Receipt == nil {
		t.Fatalf("MCP manifest upsert: %+v", provisionedMCP)
	}
	inspectedCLI, err := cli("workspace inspect", "")
	if err != nil || inspectedCLI.ManifestVersion != provisionedMCP.Receipt.Version {
		t.Fatalf("CLI cannot inspect MCP manifest: %+v, %v", inspectedCLI, err)
	}
}
