package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCentralConfigAndBranchDefaults(t *testing.T) {
	g := newGitServer(t)
	repo := sourceRepository(t, g, "source")
	t.Chdir(repo)
	cli := func(input string, args ...string) (int, []byte) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := RunCLI(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
		return code, append(stdout.Bytes(), stderr.Bytes()...)
	}
	if code, out := cli("", "config", "set", "--remote", g.url); code != 0 {
		t.Fatalf("configure machine remote: %d, %s", code, out)
	}
	filename, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if filename != filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nugs", "config.json") {
		t.Fatalf("config is not in central user directory: %s", filename)
	}
	if code, out := cli("stored with defaults", "documents", "upsert", "--name", "notes.md"); code != 0 {
		t.Fatalf("upload using implied config and branch slug: %d, %s", code, out)
	}
	if doc, err := g.store("feature-workspace").Read(context.Background(), "notes.md"); err != nil || string(doc.Content) != "stored with defaults" {
		t.Fatalf("default workspace content: %+v, %v", doc, err)
	}
	code, out := cli("", "documents", "read", "--name", "notes.md")
	var response Response
	if code != 0 || json.Unmarshal(out, &response) != nil || response.Workspace != "feature-workspace" {
		t.Fatalf("inspect with defaults: %d, %s", code, out)
	}
	if code, out := cli("", "documents", "list", "--workspace", "explicit-workspace"); code != 0 || !bytes.Contains(out, []byte(`"workspace":"explicit-workspace"`)) {
		t.Fatalf("workspace flag precedence: %d, %s", code, out)
	}
	if code, out := cli("", "config", "set", "--cache", filepath.Join(g.root, "cache")); code != 0 {
		t.Fatalf("cache update: %d, %s", code, out)
	}
	config, err := loadConfig(filename, false)
	if err != nil || config.Remote != g.url || config.CacheDir == "" {
		t.Fatalf("config set failed to preserve existing remote: %+v, %v", config, err)
	}
	if err := saveConfig(filename, Config{Remote: "https://example.invalid/not-used.git"}); err != nil {
		t.Fatal(err)
	}
	if code, out := cli("", "documents", "read", "--remote", g.url, "--name", "notes.md"); code != 0 {
		t.Fatalf("remote flag precedence: %d, %s", code, out)
	}
	profile := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(profile, Config{Remote: g.url, CacheDir: "cache"}); err != nil {
		t.Fatal(err)
	}
	if code, out := cli("", "documents", "read", "--config", profile, "--name", "notes.md"); code != 0 {
		t.Fatalf("explicit config precedence: %d, %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(profile), "cache")); err != nil {
		t.Fatalf("relative config cache path: %v", err)
	}
	if code, _ := cli("", "documents", "list", "--config", filepath.Join(t.TempDir(), "missing.json"), "--remote", g.url); code != 2 {
		t.Fatal("explicit missing config was ignored")
	}
	if err := os.WriteFile(profile, []byte(`{"remote":"x","unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := cli("", "documents", "list", "--config", profile); code != 2 {
		t.Fatal("malformed/unknown configuration accepted")
	}
}

func TestWorkspaceBranchInference(t *testing.T) {
	g := newGitServer(t)
	a := sourceRepository(t, g, "alpha")
	b := sourceRepository(t, g, "beta")
	ctx := context.Background()
	slug, err := InferWorkspace(ctx, g.root, g.root, []string{"alpha", "beta"})
	if err != nil || slug != "feature-workspace" {
		t.Fatalf("multi-repository root inference: %q, %v", slug, err)
	}
	g.run(b, "", "git", "checkout", "-b", "different-branch")
	if _, err := InferWorkspace(ctx, g.root, g.root, []string{"alpha", "beta"}); err == nil {
		t.Fatal("ambiguous multi-repo branch was silently selected")
	}
	g.run(a, "", "git", "checkout", "--detach", "HEAD")
	if _, err := InferWorkspace(ctx, a, g.root, nil); err == nil {
		t.Fatal("detached HEAD inferred a workspace")
	}
	for branch, want := range map[string]string{
		"vk/46e5-vd-multi-instanc":      "vk-46e5-vd-multi-instanc",
		"refs/heads/Feature/An_Example": "feature-an-example",
		"---":                           "",
	} {
		if got := BranchSlug(branch); got != want {
			t.Fatalf("branch %q: got %q, want %q", branch, got, want)
		}
	}
	service := Service{Remote: g.url, CacheDir: t.TempDir(), DefaultWorkspace: "feature-workspace"}
	response := service.Execute(ctx, Request{Operation: "documents.list"})
	if response.Error != nil || response.Workspace != "feature-workspace" {
		t.Fatalf("shared service default workspace: %+v", response)
	}
}
