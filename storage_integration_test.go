package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStorageBootstrapPreservesCustomRefsAndIsIdempotent(t *testing.T) {
	g := newGitServer(t)
	g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", "refs/heads/main")
	s := g.store("bootstrap")
	doc := requireUpsert(t, s, "notes.md", "", []byte("private artifact"))
	ctx := context.Background()
	got, err := s.BootstrapStorage(ctx)
	if err != nil || got == nil || !got.Changed || got.Ref != storageBootstrapRef {
		t.Fatalf("bootstrap: %+v %v", got, err)
	}
	if tree := g.run(g.root, "", "git", "--git-dir", g.repo, "ls-tree", "-r", got.Commit); tree != "" {
		t.Fatalf("bootstrap commit must not copy artifact bytes: %s", tree)
	}
	if head := g.run(g.root, "", "git", "--git-dir", g.repo, "rev-parse", s.Ref()); head != doc.Commit {
		t.Fatal("bootstrap changed document ref")
	}
	again, err := s.BootstrapStorage(ctx)
	if err != nil || again.Changed || again.Commit != got.Commit {
		t.Fatalf("repeat: %+v %v", again, err)
	}
}

func TestStorageBootstrapExistingAndRacedBranches(t *testing.T) {
	t.Run("existing source branch untouched", func(t *testing.T) {
		g := newGitServer(t)
		before := g.run(g.root, "", "git", "--git-dir", g.repo, "show-ref")
		got, err := g.store("existing").BootstrapStorage(context.Background())
		if err != nil || got.Changed || got.Ref != "refs/heads/main" {
			t.Fatalf("existing: %+v %v", got, err)
		}
		if after := g.run(g.root, "", "git", "--git-dir", g.repo, "show-ref"); before != after {
			t.Fatal("existing branches changed")
		}
	})
	t.Run("concurrent bootstrap", func(t *testing.T) {
		g := newGitServer(t)
		g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", "refs/heads/main")
		var winner *StorageReceipt
		g.beforePush = func() {
			var err error
			winner, err = g.store("second-client").BootstrapStorage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := g.store("first-client").BootstrapStorage(context.Background())
		if err != nil || winner == nil || got.Commit != winner.Commit {
			t.Fatalf("concurrent bootstrap did not converge: %+v %+v %v", got, winner, err)
		}
	})
}

func TestStorageBootstrapCLIAndRejectedPush(t *testing.T) {
	g := newGitServer(t)
	g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", "refs/heads/main")
	var stdout, stderr bytes.Buffer
	code := RunCLI(context.Background(), []string{"storage", "bootstrap", "--remote", g.url, "--workspace", "cli-bootstrap"}, strings.NewReader(""), &stdout, &stderr)
	var res Response
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil || code != 0 || res.Storage == nil || !res.Storage.Changed {
		t.Fatalf("CLI: %d %s %s %v", code, stdout.String(), stderr.String(), err)
	}
	g.run(g.root, "", "git", "--git-dir", g.repo, "update-ref", "-d", storageBootstrapRef)
	g.run(g.root, "", "git", "--git-dir", g.repo, "config", "http.receivepack", "false")
	if _, err := g.store("denied").BootstrapStorage(context.Background()); err == nil {
		t.Fatal("rejected push reported success")
	}
}
