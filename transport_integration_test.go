package nuggets

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests deliberately exercise real executables against disposable remotes.
// They do not mutate a user's source repository or production Beads database.
type rig struct {
	t    *testing.T
	root string
	env  []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	// Isolate Git/Dolt config and ambient repository routing from the caller.
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "DOLT_") || key == "HOME" || key == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, v)
	}
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+home, "DOLT_ROOT_PATH="+home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, "no-git-config"),
		"GIT_AUTHOR_NAME=Nugs Test", "GIT_AUTHOR_EMAIL=nugs@example.invalid",
		"GIT_COMMITTER_NAME=Nugs Test", "GIT_COMMITTER_EMAIL=nugs@example.invalid",
		"GIT_TERMINAL_PROMPT=0", "DOLT_CLI_NO_PAGER=1")
	return &rig{t: t, root: root, env: env}
}

func (r *rig) command(dir, input, name string, args ...string) (string, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, r.env, strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}

func (r *rig) run(dir, input, name string, args ...string) string {
	r.t.Helper()
	out, err := r.command(dir, input, name, args...)
	if err != nil {
		r.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return out
}

func (r *rig) initGitRemote() string {
	r.t.Helper()
	remote := filepath.Join(r.root, "remote.git")
	r.run(r.root, "", "git", "init", "--bare", remote)
	// Native Dolt's Git transport requires an ordinary branch, not only custom refs.
	seed := r.documentCommit(remote, "Synthetic Git transport fixture\n", "")
	r.run(r.root, "", "git", "--git-dir", remote, "update-ref", "refs/heads/main", seed)
	r.run(r.root, "", "git", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return remote
}

func (r *rig) documentCommit(repo, content, parent string) string {
	r.t.Helper()
	blob := r.run(r.root, content, "git", "--git-dir", repo, "hash-object", "-w", "--stdin")
	tree := r.run(r.root, "100644 blob "+blob+"\treview.md\n", "git", "--git-dir", repo, "mktree")
	args := []string{"--git-dir", repo, "commit-tree", tree, "-m", "Review document"}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return r.run(r.root, "", "git", args...)
}

func TestCustomDocumentRefRejectsLostUpdate(t *testing.T) {
	r := newRig(t)
	remote := r.initGitRemote()
	ref := "refs/documents/feature-123"
	base := r.documentCommit(remote, "Initial review\n", "")
	r.run(r.root, "", "git", "--git-dir", remote, "update-ref", ref, base)
	local := r.documentCommit(remote, "Local review\n", base)
	other := r.documentCommit(remote, "Other review\n", base)
	r.run(r.root, "", "git", "--git-dir", remote, "update-ref", ref, other, base)
	if out, err := r.command(r.root, "", "git", "--git-dir", remote, "update-ref", ref, local, base); err == nil {
		t.Fatalf("stale compare-and-swap unexpectedly succeeded: %s", out)
	}
	if got := r.run(r.root, "", "git", "--git-dir", remote, "rev-parse", ref); got != other {
		t.Fatalf("lost concurrent document update: got %s, want %s", got, other)
	}
	if got := r.run(r.root, "", "git", "--git-dir", remote, "show", local+":review.md"); got != "Local review" {
		t.Fatalf("local evidence lost: %q", got)
	}
}

func TestNativeDoltGitRefs(t *testing.T) {
	if os.Getenv("NUGS_TEST_DOLT") != "1" {
		t.Skip("set NUGS_TEST_DOLT=1 for the native Dolt transport experiment")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Fatal("NUGS_TEST_DOLT=1 requires dolt")
	}
	r := newRig(t)
	remote := r.initGitRemote()
	ref := "refs/dolt/feature-123"
	u := (&url.URL{Scheme: "git+file", Path: remote}).String()
	a := filepath.Join(r.root, "a")
	if err := os.Mkdir(a, 0700); err != nil {
		t.Fatal(err)
	}
	r.run(a, "", "dolt", "init", "--name", "Nugs Test", "--email", "nugs@example.invalid")
	configure := func(dir string) {
		r.run(dir, "", "dolt", "config", "--local", "--add", "user.name", "Nugs Test")
		r.run(dir, "", "dolt", "config", "--local", "--add", "user.email", "nugs@example.invalid")
	}
	configure(a)
	r.run(a, "", "dolt", "sql", "-q", "CREATE TABLE issues (id INT PRIMARY KEY, title TEXT); INSERT INTO issues VALUES (1, 'base');")
	commit := func(dir, message string) {
		r.run(dir, "", "dolt", "commit", "-A", "-m", message)
	}
	commit(a, "Initial issue fixture")
	r.run(a, "", "dolt", "remote", "add", "--ref", ref, "nugs", u)
	r.run(a, "", "dolt", "push", "-u", "nugs", "main")
	clone := func(name string) string {
		dir := filepath.Join(r.root, name)
		r.run(r.root, "", "dolt", "clone", "--ref", ref, u, dir)
		configure(dir)
		return dir
	}
	b := clone("b")
	// The Git namespace is shared, but documents and native Dolt have independent heads.
	doc := r.documentCommit(remote, "Review evidence\n", "")
	r.run(r.root, "", "git", "--git-dir", remote, "update-ref", "refs/documents/feature-123", doc)
	remoteHead := func() string {
		return r.run(r.root, "", "git", "--git-dir", remote, "rev-parse", ref)
	}
	r.run(a, "", "dolt", "sql", "-q", "INSERT INTO issues VALUES (2, 'client-a');")
	commit(a, "Client A independent issue")
	r.run(a, "", "dolt", "push", "nugs", "main")
	aHead := remoteHead()
	r.run(b, "", "dolt", "sql", "-q", "INSERT INTO issues VALUES (3, 'client-b');")
	commit(b, "Client B independent issue")
	if out, err := r.command(b, "", "dolt", "push", "origin", "main"); err == nil {
		t.Fatalf("divergent native push unexpectedly succeeded: %s", out)
	}
	if remoteHead() != aHead {
		t.Fatal("rejected push changed the Dolt remote")
	}
	r.run(b, "", "dolt", "pull", "--no-edit", "origin", "main")
	r.run(b, "", "dolt", "push", "origin", "main")
	check := clone("check")
	rows := r.run(check, "", "dolt", "sql", "-r", "csv", "-q", "SELECT id,title FROM issues ORDER BY id")
	for _, want := range []string{"1,base", "2,client-a", "3,client-b"} {
		if !strings.Contains(rows, want) {
			t.Fatalf("native merge lost %q: %s", want, rows)
		}
	}
	// Both sides now edit the same issue. Native merge must surface the conflict.
	r.run(a, "", "dolt", "pull", "--no-edit", "nugs", "main")
	r.run(a, "", "dolt", "sql", "-q", "UPDATE issues SET title='remote-edit' WHERE id=1")
	commit(a, "Remote same-row edit")
	r.run(a, "", "dolt", "push", "nugs", "main")
	pushed := remoteHead()
	r.run(check, "", "dolt", "sql", "-q", "UPDATE issues SET title='local-edit' WHERE id=1")
	commit(check, "Local same-row edit")
	if out, err := r.command(check, "", "dolt", "push", "origin", "main"); err == nil {
		t.Fatalf("conflicting native push unexpectedly succeeded: %s", out)
	}
	// Some engine versions report a conflicted pull using output/state, not only exit code.
	pullOutput, pullErr := r.command(check, "", "dolt", "pull", "--no-edit", "origin", "main")
	conflicts := r.run(check, "", "dolt", "sql", "-r", "csv", "-q", "SELECT * FROM dolt_conflicts")
	if !strings.Contains(conflicts, "issues") {
		t.Fatalf("expected explicit native row conflict, got %s (pull: %v, %s)", conflicts, pullErr, pullOutput)
	}
	if remoteHead() != pushed {
		t.Fatal("conflicted pull changed remote history")
	}
	if got := r.run(r.root, "", "git", "--git-dir", remote, "rev-parse", "refs/documents/feature-123"); got != doc {
		t.Fatal("Dolt sync changed the document ref")
	}
	t.Logf("native custom-ref round trip, clean reconciliation and conflict detection verified: %s", fmt.Sprint(strings.TrimSpace(conflicts)))
}
