package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLocalWorkspaceInitializationAndNoFallback(t *testing.T) {
	g := newGitServer(t)
	repo := sourceRepository(t, g, "local-source")
	t.Chdir(repo)
	cli := func(input string, args ...string) (int, []byte) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		code := RunCLI(context.Background(), args, strings.NewReader(input), &out, &diagnostics)
		return code, append(out.Bytes(), diagnostics.Bytes()...)
	}
	before := g.run(repo, "", "git", "show-ref")
	code, out := cli("", "workspace", "init")
	var selected WorkspaceContext
	if code != 0 || json.Unmarshal(out, &selected) != nil || !validWorkspaceReference(selected.Reference) || selected.Engagement != "local" {
		t.Fatalf("explicit local initialization: %d, %s", code, out)
	}
	registryPath, _ := RegistryPath()
	r, err := readRegistry(registryPath)
	if err != nil || len(r.Bindings) != 1 {
		t.Fatalf("persistent registry: %+v, %v", r, err)
	}
	b := r.Bindings[0]
	if !filepath.IsAbs(b.Remote) || strings.Contains(b.Remote, "://") {
		t.Fatalf("local initialization chose hosted storage: %+v", b)
	}
	info, err := os.Stat(b.Remote)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("local bare repository is not private: %v, %v", info, err)
	}
	g.run(repo, "", "git", "--git-dir", b.Remote, "rev-parse", storageBootstrapRef)
	if code, out := cli("local evidence", "documents", "upsert", "--name", "notes.md"); code != 0 {
		t.Fatalf("omitted storage selector: %d, %s", code, out)
	}
	if got := g.run(repo, "", "git", "show-ref"); got != before {
		t.Fatalf("setup/document operation changed source refs: %s -> %s", before, got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".beads")); !os.IsNotExist(err) {
		t.Fatal("local setup created a source-repository Beads collection")
	}
	configPath, _ := ConfigPath()
	if err := saveConfig(configPath, Config{Engagement: "other", Remote: "https://example.invalid/never-contact.git"}); err != nil {
		t.Fatal(err)
	}
	g.run(repo, "", "git", "branch", "-m", "different-branch")
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	code, out = cli("", "documents", "read", "--name", "notes.md")
	var read Response
	if code != 0 || json.Unmarshal(out, &read) != nil || read.Document == nil || *read.Document.Text != "local evidence" || read.WorkspaceReference != b.Reference || read.Document.URI != ResourceURI(b.Reference, "notes.md") {
		t.Fatalf("member cwd/branch/policy changed identity: %d, %s", code, out)
	}
	code, out = cli("", "workspace", "init", "--root", repo)
	var repeated WorkspaceContext
	if code != 0 || json.Unmarshal(out, &repeated) != nil || repeated != selected {
		t.Fatalf("setup is not idempotent: %d, %s", code, out)
	}
	registryBefore, _ := os.ReadFile(registryPath)
	if code, _ := cli("", "workspace", "attach", "--root", repo, "--remote", g.url); code == 0 {
		t.Fatal("conflicting explicit rebind was accepted")
	}
	if err := os.Rename(b.Remote, b.Remote+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if code, _ := cli("", "documents", "list"); code == 0 {
		t.Fatal("missing bound store silently fell back to mutable policy")
	}
	if _, err := os.Stat(b.Remote); !os.IsNotExist(err) {
		t.Fatal("failed lookup recreated local storage")
	}
	registryAfter, _ := os.ReadFile(registryPath)
	if !bytes.Equal(registryBefore, registryAfter) {
		t.Fatal("failed operation or conflicting rebind changed registry")
	}
	t.Chdir(g.root)
	if code, _ := cli("", "documents", "list"); code == 0 {
		t.Fatal("unbound directory silently attached to global policy")
	}
}

func TestBoundCLIAndStdioMCPEngagementIsolation(t *testing.T) {
	g := newGitServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "nugs")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/nugs")
	build.Env = g.buildEnv
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s, %v", out, err)
	}
	a := sourceRepository(t, g, "client-a")
	b := sourceRepository(t, g, "client-b")
	peer := sourceRepository(t, g, "client-a-peer")
	otherRemote := filepath.Join(g.root, "other.git")
	g.run(g.root, "", "git", "init", "--bare", otherRemote)
	attach := func(root, engagement, remote string) WorkspaceContext {
		t.Helper()
		out := g.run(root, "", binary, "workspace", "attach", "--root", root, "--engagement", engagement, "--remote", remote, "--workspace", "same-handle")
		var selected WorkspaceContext
		if err := json.Unmarshal([]byte(out), &selected); err != nil {
			t.Fatal(err)
		}
		return selected
	}
	selected := attach(a, "client-a", g.url)
	other := attach(b, "client-b", otherRemote)
	beforeSource := g.run(a, "", "git", "show-ref")
	g.run(a, "scoped evidence", binary, "documents", "upsert", "--name", "readiness.md")
	g.run(b, "other engagement", binary, "documents", "upsert", "--name", "readiness.md")
	if selected.Reference == other.Reference {
		t.Fatal("same slug conflated two engagement identities")
	}
	// Same-handle identities within one engagement require an opaque selector.
	peerRemote := filepath.Join(g.root, "peer.git")
	g.run(g.root, "", "git", "init", "--bare", peerRemote)
	peerSelected := attach(peer, "client-a", peerRemote)
	if _, err := g.command(a, "", binary, "documents", "list", "--workspace", "same-handle"); err == nil {
		t.Fatal("ambiguous explicit handle selected a workspace")
	}
	if _, err := g.command(a, "", binary, "documents", "list", "--workspace-ref", other.Reference); err == nil {
		t.Fatal("CLI crossed the engagement boundary using an opaque reference")
	}
	if got := g.run(a, "", "git", "show-ref"); got != beforeSource {
		t.Fatal("attachment or artifact storage changed source refs")
	}
	registryPath, _ := RegistryPath()
	r, err := readRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if r.Bindings[0].CacheDir == r.Bindings[1].CacheDir || r.Bindings[0].CacheDir == r.Bindings[2].CacheDir {
		t.Fatal("workspace caches are not isolated")
	}
	// Changing policy and branch after attachment cannot retarget either client.
	g.run(a, "", binary, "config", "set", "--engagement", "unrelated", "--remote", "https://example.invalid/never-contact.git")
	g.run(a, "", "git", "branch", "-m", "renamed")
	cmd := exec.CommandContext(ctx, binary, "mcp", "--workspace-ref", selected.Reference)
	cmd.Dir, cmd.Env = a, g.env
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	client := mcp.NewClient(&mcp.Implementation{Name: "bound-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("bound stdio MCP: %v, %s", err, diagnostics.String())
	}
	defer session.Close()
	call := func(name string, args map[string]any) (*mcp.CallToolResult, Response) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result.StructuredContent)
		var response Response
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		return result, response
	}
	result, current := call("workspace_context", map[string]any{})
	if result.IsError || current.WorkspaceReference != selected.Reference {
		t.Fatalf("MCP lost contextual identity: %+v", current)
	}
	result, found := call("workspaces_list", map[string]any{})
	if result.IsError || len(found.Workspaces) != 2 {
		t.Fatalf("engagement discovery: %+v", found)
	}
	for _, item := range found.Workspaces {
		if item.Engagement != "client-a" {
			t.Fatalf("discovery leaked another engagement: %+v", item)
		}
	}
	result, read := call("document_read", map[string]any{"name": "readiness.md"})
	if result.IsError || read.Document == nil || *read.Document.Text != "scoped evidence" {
		t.Fatalf("MCP omitted remote/workspace: %+v", read)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: read.Document.URI})
	if err != nil || string(resource.Contents[0].Blob) != "scoped evidence" {
		t.Fatalf("stable-reference resource: %+v, %v", resource, err)
	}
	result, rejected := call("document_read", map[string]any{"workspace_ref": other.Reference, "name": "readiness.md"})
	if !result.IsError || rejected.Error == nil || rejected.Error.Code != "workspace_unbound" {
		t.Fatalf("MCP cross-engagement traversal: %+v", rejected)
	}
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: ResourceURI(other.Reference, "readiness.md")}); err == nil {
		t.Fatal("resource reader bypassed engagement resolution")
	}
	result, rejected = call("documents_list", map[string]any{"workspace": "same-handle"})
	if !result.IsError || rejected.Error.Code != "workspace_ambiguous" {
		t.Fatalf("MCP ambiguous handle: %+v", rejected)
	}
	result, switched := call("documents_list", map[string]any{"workspace_ref": peerSelected.Reference})
	if result.IsError || switched.WorkspaceReference != peerSelected.Reference {
		t.Fatalf("explicit same-engagement selection: %+v", switched)
	}
	// A conflicting nested attachment is rejected, not a new contextual default.
	nested := filepath.Join(a, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := g.command(nested, "", binary, "workspace", "attach", "--root", nested, "--workspace-ref", other.Reference); err == nil {
		t.Fatal("conflicting nested attachment installed")
	}
	// Corrupt/ambiguous existing state must fail even on an already open client.
	r.Attachments = append(r.Attachments, r.Attachments[0])
	if err := saveJSON(registryPath, r); err != nil {
		t.Fatal(err)
	}
	result, rejected = call("documents_list", map[string]any{})
	if !result.IsError || rejected.Error.Code != "workspace_ambiguous" {
		t.Fatalf("ambiguous existing attachment fell back: %+v", rejected)
	}
	if err := os.Rename(registryPath, registryPath+".unavailable"); err != nil {
		t.Fatal(err)
	}
	result, rejected = call("documents_list", map[string]any{})
	if !result.IsError || rejected.Error.Code != "workspace_unbound" {
		t.Fatalf("missing registry fell back: %+v", rejected)
	}
	if _, err := os.Stat(registryPath); !os.IsNotExist(err) {
		t.Fatal("missing registry was implicitly recreated")
	}
}
