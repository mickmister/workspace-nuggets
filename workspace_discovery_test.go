package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRemoteWorkspaceDiscoveryFreshRunnerAndIsolation(t *testing.T) {
	g := newGitServer(t)
	other := newGitServer(t)
	ctx := context.Background()
	for _, workspace := range []string{"alpha", "beta"} {
		if _, _, err := g.store(workspace).ProvisionWithPurpose(ctx, "", nil, workspace+" research", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := other.store("private").ProvisionWithPurpose(ctx, "", nil, "not configured", ""); err != nil {
		t.Fatal(err)
	}
	// A malformed/unprovisioned namespace is evidence, not a reason to hide it
	// or incorrectly declare that the authorized store is empty.
	if _, err := g.store("broken").Upsert(ctx, "threads/task/note.md", "", []byte("no manifest")); err != nil {
		t.Fatal(err)
	}
	service := Service{RegistryPath: filepath.Join(t.TempDir(), "registry.json"), ContextDir: t.TempDir(), WorkspaceStores: map[string]WorkspaceStore{"team": {Remote: g.url, Engagement: "team"}}}
	first := service.Execute(ctx, Request{Operation: "workspace.discover", Limit: 1})
	if first.Error != nil || first.Discovery == nil || len(first.Discovery.Workspaces) != 1 || first.Discovery.Workspaces[0].Workspace != "alpha" || first.Discovery.Workspaces[0].Manifest.Purpose != "alpha research" || first.Discovery.NextToken == "" {
		t.Fatalf("first page: %+v", first)
	}
	second := service.Execute(ctx, Request{Operation: "workspace.discover", Token: first.Discovery.NextToken, Limit: 1})
	if second.Error != nil || second.Discovery.Workspaces[0].Workspace != "beta" {
		t.Fatalf("second page: %+v", second)
	}
	last := service.Execute(ctx, Request{Operation: "workspace.discover", Token: second.Discovery.NextToken})
	if last.Error != nil || len(last.Discovery.Workspaces) != 1 || last.Discovery.Workspaces[0].Workspace != "broken" || last.Discovery.Workspaces[0].Error == nil || last.Discovery.NextToken != "" {
		t.Fatalf("invalid manifest visibility: %+v", last)
	}
	for _, req := range []Request{
		{Operation: "workspace.discover", Target: "unknown"},
		{Operation: "workspace.discover", Limit: 101},
		{Operation: "workspace.discover", Token: "broken"},
		{Operation: "workspace.discover", Token: first.Discovery.NextToken, Prefix: "beta"},
		{Operation: "workspace.discover", WorkspaceReference: "ws-00000000000000000000000000000000"},
	} {
		if response := service.Execute(ctx, req); response.Error == nil {
			t.Fatalf("invalid request accepted: %+v", req)
		}
	}
	if _, err := readRegistry(service.RegistryPath); err != nil {
		t.Fatal(err)
	}
	if response := service.Execute(ctx, Request{Operation: "workspace.context"}); response.Error == nil || response.Error.Code != "workspace_unbound" {
		t.Fatalf("discovery installed attachment: %+v", response)
	}
	service.WorkspaceStores["team"] = WorkspaceStore{Remote: other.url, Engagement: "team"}
	if response := service.Execute(ctx, Request{Operation: "workspace.discover", Token: first.Discovery.NextToken}); response.Error == nil {
		t.Fatal("cursor crossed configured storage boundary")
	}
}

func TestRemoteWorkspaceCreationAndNativeSelection(t *testing.T) {
	g := newGitServer(t)
	ctx := context.Background()
	registry := filepath.Join(t.TempDir(), "registry.json")
	service := Service{RegistryPath: registry, ContextDir: t.TempDir(), WorkspaceStores: map[string]WorkspaceStore{"team": {Remote: g.url, Engagement: "team"}}}
	create := Request{Operation: "workspace.create", Workspace: "new-topic", Purpose: "Investigate a new topic"}
	created := service.Execute(ctx, create)
	if created.Error != nil || created.Receipt == nil || created.Manifest.Purpose != create.Purpose {
		t.Fatalf("create: %+v", created)
	}
	create.Purpose = "do not overwrite"
	if response := service.Execute(ctx, create); response.Error == nil || response.Error.Code != "conflict" {
		t.Fatalf("existing workspace overwritten: %+v", response)
	}
	root := t.TempDir()
	selected := service.Execute(ctx, Request{Operation: "workspace.select", Workspace: "new-topic", Root: root})
	if selected.Error != nil || !validWorkspaceReference(selected.WorkspaceReference) {
		t.Fatalf("select: %+v", selected)
	}
	service.ContextDir = root
	if response := service.Execute(ctx, Request{Operation: "workspace.inspect"}); response.Error != nil || response.Manifest.Purpose != "Investigate a new topic" {
		t.Fatalf("native inspection: %+v", response)
	}
	selectedAgain := service.Execute(ctx, Request{Operation: "workspace.select", Workspace: "new-topic", Root: t.TempDir()})
	if selectedAgain.Error != nil || selectedAgain.WorkspaceReference != selected.WorkspaceReference {
		t.Fatalf("identity changed: %+v", selectedAgain)
	}
	if response := service.Execute(ctx, Request{Operation: "workspace.select", Workspace: "absent", Root: t.TempDir()}); response.Error == nil {
		t.Fatal("selected absent namespace")
	}
	service.WorkspaceStores["another"] = WorkspaceStore{Remote: g.url, Engagement: "team"}
	if response := service.Execute(ctx, Request{Operation: "workspace.create", Workspace: "ambiguous", Purpose: "test"}); response.Error == nil {
		t.Fatal("ambiguous destination silently selected")
	}
	if response := service.Execute(ctx, Request{Operation: "workspace.create", Target: "team", Workspace: "explicit", Purpose: "explicit destination"}); response.Error != nil {
		t.Fatalf("explicit configured destination: %+v", response)
	}
}

func TestUnboundDiscoveryCLIAndMCP(t *testing.T) {
	g := newGitServer(t)
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "config.json")
	cli := func(args ...string) Response {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args = append(args, "--config", filename)
		if code := RunCLI(ctx, args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("CLI %v: %d %s %s", args, code, &stdout, &stderr)
		}
		if stdout.Len() == 0 {
			return Response{}
		}
		var response Response
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	cli("config", "workspace-store", "--target", "team", "--engagement", "team", "--remote", g.url)
	cli("workspace", "create", "--workspace", "cli-topic", "--purpose", "CLI intake")
	if response := cli("workspace", "discover"); response.Discovery == nil || len(response.Discovery.Workspaces) != 1 {
		t.Fatalf("CLI discovery: %+v", response)
	}
	config, err := loadConfig(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{RegistryPath: filepath.Join(t.TempDir(), "registry.json"), ContextDir: t.TempDir(), WorkspaceStores: configuredWorkspaceStores(config, filename)}
	server := NewMCPServer(service)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "discovery-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"workspaces_discover", map[string]any{}},
		{"workspace_create", map[string]any{"workspace": "mcp-topic", "purpose": "MCP intake"}},
		{"workspace_select", map[string]any{"workspace": "mcp-topic", "root": t.TempDir()}},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil || result.IsError {
			t.Fatalf("MCP %s: %+v %v", call.name, result, err)
		}
	}
}

func TestConfiguredWorkspaceStoresAndFailures(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "config.json")
	stores := configuredWorkspaceStores(Config{Remote: "default.git", Engagement: "default-engagement", WorkspaceStores: map[string]WorkspaceStore{"team": {Remote: "team.git", Engagement: "team"}}}, filename)
	if stores["default"].Remote != filepath.Join(filepath.Dir(filename), "default.git") || stores["team"].Remote != filepath.Join(filepath.Dir(filename), "team.git") {
		t.Fatalf("relative config resolution: %+v", stores)
	}
	service := Service{Remote: "not-an-authorized-store", WorkspaceStores: stores}
	if response := service.Execute(context.Background(), Request{Operation: "workspace.discover"}); response.Error == nil || response.Discovery != nil {
		t.Fatalf("unavailable store appeared empty: %+v", response)
	}
	if response := (Service{Remote: "not-an-authorized-store"}).Execute(context.Background(), Request{Operation: "workspace.discover"}); response.Error == nil {
		t.Fatal("legacy transport became implicit discovery authority")
	}
}
