package nuggets

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

func TestWorkspaceBeadsDiscovery(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "repo", "nested")
	for _, dir := range []string{filepath.Join(root, ".beads"), filepath.Join(root, "repo", ".beads"), inner} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{root, filepath.Join(root, "repo")} {
		if err := os.WriteFile(filepath.Join(dir, ".beads", "redirect"), []byte("external-collection\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := WorkspaceBeadsRoot(inner, "")
	if err != nil || got != root {
		t.Fatalf("nested repo shadowed workspace collection: %q, %v", got, err)
	}
	if _, err := WorkspaceBeadsRoot(inner, inner); err == nil {
		t.Fatal("explicit root without workspace marker was accepted")
	}
	if _, err := WorkspaceBeadsRoot(t.TempDir(), ""); err == nil {
		t.Fatal("missing workspace triggered repository/global fallback")
	}
	if got, err := nativeGitURL("https://example.invalid/storage.git"); err != nil || got != "git+https://example.invalid/storage.git" {
		t.Fatalf("native Git URL: %s, %v", got, err)
	}
	if _, err := nativeGitURL("git+https://example.invalid/storage.git?ref=refs/dolt/not-honored"); err == nil {
		t.Fatal("ineffective ref query parameter accepted")
	}
}

func TestWorkspaceBeadsNativeConfiguration(t *testing.T) {
	if os.Getenv("NUGS_TEST_BEADS") != "1" {
		t.Skip("set NUGS_TEST_BEADS=1 for actual embedded Beads configuration/push tests")
	}
	for _, tool := range []string{"bd", "dolt"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("NUGS_TEST_BEADS=1 requires %s", tool)
		}
	}
	g := newGitServer(t)
	// Exercise product bootstrap, not a fixture's pre-seeded main branch.
	g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", "refs/heads/main")
	bootstrap, err := g.store("workspace-beads").BootstrapStorage(context.Background())
	if err != nil || !bootstrap.Changed {
		t.Fatalf("data repository bootstrap: %+v, %v", bootstrap, err)
	}
	ctx := context.Background()
	persistent := filepath.Join(g.root, "persistent")
	root := filepath.Join(g.root, "workspace")
	for _, dir := range []string{filepath.Join(persistent, ".beads"), filepath.Join(root, ".beads")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(persistent, ".beads", "config.yaml"), []byte("no-git-ops: true\nno-push: true\ndolt:\n  shared-server: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := runBeads(ctx, persistent, "init", "--prefix", "fx", "--skip-agents", "--skip-hooks", "--non-interactive"); err != nil {
		t.Fatalf("initialize isolated workspace collection: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, ".beads", "redirect"), []byte(filepath.Join(persistent, ".beads")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(source, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	// A nested repo marker and ambient routing must not select another collection.
	if err := os.WriteFile(filepath.Join(source, ".beads", "metadata.json"), []byte(`{"backend":"sqlite"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(source)
	t.Setenv("BEADS_DIR", filepath.Join(source, ".beads"))
	createdJSON, err := runBeads(ctx, root, "create", "Synthetic workspace issue", "--description", "Native configuration fixture only", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdJSON, &issue); err != nil || issue.ID == "" {
		t.Fatalf("synthetic issue: %s, %v", createdJSON, err)
	}
	var beforeStatus map[string]any
	if err := beadsJSON(ctx, root, &beforeStatus, "vc", "status"); err != nil {
		t.Fatal(err)
	}
	beforeIssue, err := runBeads(ctx, root, "show", issue.ID, "--json", "--readonly")
	if err != nil {
		t.Fatal(err)
	}
	store := g.store("workspace-beads")
	info, err := store.ConfigureBeads(ctx, "")
	if err != nil {
		t.Fatalf("native configure: %+v, %v", info, err)
	}
	if info.WorkspaceRoot != root || info.BeadsDir != filepath.Join(persistent, ".beads") || info.CollectionID == "" || !info.Configured || !info.Changed || info.NoPush || info.AutoPush {
		t.Fatalf("wrong workspace binding: %+v", info)
	}
	if info.DesiredRef != "refs/dolt/workspace-beads" || len(info.Remotes) != 1 || info.Remotes[0].Params["git_ref"] != info.DesiredRef {
		t.Fatalf("custom ref not persisted: %+v", info)
	}
	again, err := store.ConfigureBeads(ctx, root)
	if err != nil || again.Changed || !again.Configured || again.CollectionID != info.CollectionID {
		t.Fatalf("idempotent configuration: %+v, %v", again, err)
	}
	var afterStatus map[string]any
	if err := beadsJSON(ctx, root, &afterStatus, "vc", "status"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeStatus, afterStatus) {
		t.Fatalf("remote configuration changed task history/working set: before %+v, after %+v", beforeStatus, afterStatus)
	}
	afterIssue, err := runBeads(ctx, root, "show", issue.ID, "--json", "--readonly")
	if err != nil || string(beforeIssue) != string(afterIssue) {
		t.Fatalf("configuration changed issue data: %v", err)
	}
	other := store
	other.Workspace = "different-workspace"
	_, err = other.ConfigureBeads(ctx, root)
	var failure *OperationError
	if !errors.As(err, &failure) || failure.Code != "conflict" {
		t.Fatalf("collection silently repointed to another workspace: %v", err)
	}
	lock := flock.New(filepath.Join(info.DataDir, ".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	busyErr := info.withLock(info.readRemotes)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if busyErr == nil {
		t.Fatal("concurrent setup was not serialized")
	}
	// Only this synthetic fixture is pushed. Verify bd itself honors the custom ref.
	if _, err := runBeads(ctx, root, "update", issue.ID, "--description", "Uncommitted working-set change", "--dolt-auto-commit", "off"); err != nil {
		t.Fatal(err)
	}
	publication, err := store.PublishBeads(ctx, root)
	if err != nil || publication == nil || !publication.Verified || publication.CollectionID != info.CollectionID || publication.DoltCommit == beforeStatus["commit"] {
		t.Fatalf("native fixture publication: %+v, %v", publication, err)
	}
	refs := g.run(g.root, "", "git", "ls-remote", g.url, "refs/dolt/*")
	if !strings.Contains(refs, "refs/dolt/workspace-beads") || strings.Contains(refs, "refs/dolt/data") {
		t.Fatalf("native Beads used wrong Git namespace: %s", refs)
	}
	clone := filepath.Join(g.root, "recovered")
	g.run(g.root, "", "dolt", "clone", "--ref", info.DesiredRef, info.DesiredURL, clone)
	rows := g.run(clone, "", "dolt", "sql", "-r", "csv", "-q", "SELECT id,title,description FROM issues")
	if !strings.Contains(rows, issue.ID) || !strings.Contains(rows, "Synthetic workspace issue") || !strings.Contains(rows, "Uncommitted working-set change") {
		t.Fatalf("native fixture recovery lost issue: %s", rows)
	}
	if _, err := runBeads(ctx, root, "config", "set", "no-push", "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishBeads(ctx, root); !errors.As(err, &failure) || failure.Code != "sync_disabled" {
		t.Fatalf("disabled native push reported publication: %v", err)
	}
	if _, err := runBeads(ctx, root, "config", "set", "no-push", "false"); err != nil {
		t.Fatal(err)
	}
	// A different writer publishes a new commit. A stale publisher must not
	// overwrite it or silently pull/choose winners during publication.
	g.run(clone, "", "dolt", "sql", "-q", "UPDATE issues SET title='Concurrent writer' WHERE id='"+issue.ID+"'")
	g.run(clone, "", "dolt", "config", "--local", "--add", "user.name", "Nugs Fixture")
	g.run(clone, "", "dolt", "config", "--local", "--add", "user.email", "fixture@example.invalid")
	g.run(clone, "", "dolt", "add", ".")
	g.run(clone, "", "dolt", "commit", "-m", "Concurrent fixture update")
	g.run(clone, "", "dolt", "push", "origin", "main")
	remoteBefore := g.run(g.root, "", "git", "ls-remote", g.url, info.DesiredRef)
	if _, err := runBeads(ctx, root, "update", issue.ID, "--title", "Local checkpoint retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishBeads(ctx, root); !errors.As(err, &failure) || failure.Code != "push_failed" {
		t.Fatalf("stale publication did not stop: %v", err)
	}
	if remoteAfter := g.run(g.root, "", "git", "ls-remote", g.url, info.DesiredRef); remoteAfter != remoteBefore {
		t.Fatal("stale publication changed concurrent remote")
	}
	local, err := runBeads(ctx, root, "show", issue.ID, "--json", "--readonly")
	if err != nil || !strings.Contains(string(local), "Local checkpoint retained") {
		t.Fatalf("failed publication lost local checkpoint: %s, %v", local, err)
	}
}
