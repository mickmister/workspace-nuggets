package nuggets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceRepository(t *testing.T, g *gitServer, name string) string {
	t.Helper()
	dir := filepath.Join(g.root, name)
	g.run(g.root, "", "git", "init", "--initial-branch=feature/workspace", dir)
	g.run(dir, "", "git", "commit", "--allow-empty", "-m", "Source fixture")
	return dir
}

func TestWorkspaceManifestOverHTTP(t *testing.T) {
	g := newGitServer(t)
	a := sourceRepository(t, g, "alpha")
	b := sourceRepository(t, g, "beta")
	g.run(a, "", "git", "remote", "add", "origin", "https://user:password@example.invalid/org/code.git?token=secret")
	g.run(b, "", "git", "checkout", "--detach", "HEAD")
	if err := os.WriteFile(filepath.Join(a, "untracked.txt"), []byte("local only"), 0600); err != nil {
		t.Fatal(err)
	}
	store := g.store("manifest-workspace")
	ctx := context.Background()
	before, err := store.Inspect(ctx)
	if err != nil || before.Manifest != nil {
		t.Fatalf("unprovisioned inspection: %+v, %v", before, err)
	}
	manifest, receipt, err := store.Provision(ctx, g.root, []string{"beta", "alpha"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repositories) != 2 {
		t.Fatalf("missing workspace membership: %+v", manifest)
	}
	first, second := manifest.Repositories[0], manifest.Repositories[1]
	if first.Path != "alpha" || first.Branch != "refs/heads/feature/workspace" || first.Commit != g.run(a, "", "git", "rev-parse", "HEAD") || !first.Dirty || first.Detached {
		t.Fatalf("incorrect branch pointer: %+v", first)
	}
	if first.Publication != "unverified" || len(first.Remotes) != 1 || first.Remotes[0].URL != "https://example.invalid/org/code.git" {
		t.Fatalf("remote credentials or publication assertion: %+v", first)
	}
	if second.Path != "beta" || !second.Detached || second.Branch != "" || second.Publication != "no_remote" || second.Dirty {
		t.Fatalf("detached/missing remote not explicit: %+v", second)
	}
	reader := g.store(store.Workspace)
	snapshot, err := reader.Inspect(ctx)
	if err != nil || snapshot.Manifest == nil || snapshot.ManifestVersion != receipt.Version || snapshot.Commit != receipt.Commit {
		t.Fatalf("cross-client manifest inspection: %+v, %v", snapshot, err)
	}
	doc, err := reader.Read(ctx, ManifestDocument)
	if err != nil || strings.Contains(string(doc.Content), "password") || strings.Contains(string(doc.Content), "secret") || strings.Contains(string(doc.Content), g.root) {
		t.Fatalf("manifest exposes credentials or machine paths: %s, %v", doc.Content, err)
	}
	g.run(a, "", "git", "checkout", "-b", "feature/new-pointer")
	updated, next, err := store.Provision(ctx, g.root, []string{"alpha", "beta"}, receipt.Version)
	if err != nil || updated.Repositories[0].Branch != "refs/heads/feature/new-pointer" {
		t.Fatalf("pointer update: %+v, %v", updated, err)
	}
	_, _, err = store.Provision(ctx, g.root, []string{"alpha", "beta"}, receipt.Version)
	var failure *OperationError
	if !errors.As(err, &failure) || failure.Code != "conflict" {
		t.Fatalf("stale manifest update: %v", err)
	}
	if _, err := store.Upsert(ctx, ManifestDocument, next.Version, []byte(`{"schema_version":999}`)); !errors.As(err, &failure) || failure.Code != "invalid_manifest" {
		t.Fatalf("invalid reserved manifest accepted: %v", err)
	}
	snapshot, err = reader.Inspect(ctx)
	if err != nil || snapshot.ManifestVersion != next.Version {
		t.Fatalf("invalid update changed remote manifest: %+v, %v", snapshot, err)
	}
	if _, err := store.ObserveManifest(ctx, g.root, []string{"alpha", "alpha"}); err == nil {
		t.Fatal("duplicate repository memberships accepted")
	}
	if _, err := store.ObserveManifest(ctx, a, []string{b}); err == nil {
		t.Fatal("repository outside workspace accepted")
	}
}
