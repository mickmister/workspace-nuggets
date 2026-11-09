package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Compare the supported native lifecycle and nugs against the same shared-local
// writer workload. Only the independent remote peer has another database.
func TestBeadsSharedLocalWriters(t *testing.T) {
	if os.Getenv("NUGS_TEST_BEADS") != "1" {
		t.Skip("set NUGS_TEST_BEADS=1 for shared-local native Beads concurrency")
	}
	for _, tool := range []string{"bd", "dolt"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("NUGS_TEST_BEADS=1 requires %s", tool)
		}
	}
	for _, mode := range []string{"native", "nugs"} {
		t.Run(mode, func(t *testing.T) {
			g := newGitServer(t)
			t.Logf("native tools: %s; %s", g.run(g.root, "", "bd", "--version"), g.run(g.root, "", "dolt", "version"))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			root := filepath.Join(g.root, "workspace")
			persistent := filepath.Join(g.root, "collection")
			for _, dir := range []string{filepath.Join(root, ".beads"), filepath.Join(persistent, ".beads")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Match the supported embedded setup used by the pinned native suite.
			// Explicit auto-commit makes completed bd writes native checkpoints.
			config := "no-git-ops: true\nno-push: true\ndolt:\n  shared-server: false\n  auto-commit: on\n  auto-push: false\n"
			if err := os.WriteFile(filepath.Join(persistent, ".beads", "config.yaml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := runBeads(ctx, persistent, "init", "--prefix", "cw", "--skip-agents", "--skip-hooks", "--non-interactive"); err != nil {
				t.Fatalf("initialize disposable collection: %s: %v", out, err)
			}
			if err := os.WriteFile(filepath.Join(root, ".beads", "redirect"), []byte(filepath.Join(persistent, ".beads")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Direct bd deliberately does not use the nugs command wrapper or its
			// --sandbox flag. Routing/environment are isolated to the fixture.
			native := func(root string, args ...string) ([]byte, error) {
				cmd := exec.CommandContext(ctx, "bd", args...)
				cmd.Dir, cmd.Env, cmd.WaitDelay = root, beadsEnvironment(root), time.Second
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					return out, fmt.Errorf("bd %v: %w: %s %s", args, err, out, stderr.String())
				}
				return out, nil
			}
			ids := make([]string, 3)
			for i := range ids {
				out, err := native(root, "create", fmt.Sprintf("Writer %d", i), "--description", "Concurrency fixture", "--json")
				var issue struct{ ID string }
				if err != nil || json.Unmarshal(out, &issue) != nil || issue.ID == "" {
					t.Fatalf("create issue: %s, %v", out, err)
				}
				ids[i] = issue.ID
			}
			store := g.store("shared-writers")
			info, err := store.ConfigureBeads(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			initial, err := store.PublishBeads(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			peer := filepath.Join(g.root, "peer")
			if err := os.Mkdir(peer, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RestoreBeads(ctx, peer, filepath.Join(g.root, "peer-data"), info.CollectionID, initial.DoltCommit); err != nil {
				t.Fatal(err)
			}
			if _, err := native(peer, "update", ids[2], "--description", "Independent remote edit", "--dolt-auto-commit", "on"); err != nil {
				t.Fatal(err)
			}
			if _, err := native(peer, "dolt", "push", "--remote", "origin"); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 3)
			for writer := range 2 {
				go func() {
					<-start
					for revision := range 3 {
						_, err := native(root, "update", ids[writer], "--description", fmt.Sprintf("Writer %d revision %d", writer, revision), "--dolt-auto-commit", "on")
						if err != nil {
							results <- err
							return
						}
					}
					results <- nil
				}()
			}
			sync := func() error {
				if mode == "nugs" {
					_, err := store.SyncBeads(ctx, root)
					return err
				}
				for _, args := range [][]string{{"dolt", "commit"}, {"dolt", "pull", "--remote", "origin"}, {"dolt", "push", "--remote", "origin"}} {
					if _, err := native(root, args...); err != nil {
						return err
					}
				}
				return nil
			}
			go func() {
				<-start
				results <- sync()
			}()
			close(start)
			// Record busy/interleaving outcomes, then retry after writers finish.
			// No retry is added to product code to make this baseline pass.
			for range 3 {
				if err := <-results; err != nil {
					t.Logf("overlapping native operation: %v", err)
				}
			}
			if err := sync(); err != nil {
				t.Fatalf("quiescent recovery: %v", err)
			}
			for i, id := range ids {
				want := "Independent remote edit"
				if i < 2 {
					want = fmt.Sprintf("Writer %d revision 2", i)
				}
				out, err := native(root, "show", id, "--json", "--readonly")
				if err != nil || !strings.Contains(string(out), want) {
					t.Errorf("shared collection lost %s: %s, %v", id, out, err)
				}
			}
			published, err := store.PublishBeads(ctx, root)
			if err != nil || published == nil || !published.Verified {
				t.Fatalf("independent native publication verification: %+v, %v", published, err)
			}
			cold := filepath.Join(g.root, "cold-replacement")
			if err := os.Mkdir(cold, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RestoreBeads(ctx, cold, filepath.Join(g.root, "cold-data"), info.CollectionID, published.DoltCommit); err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				want := "Independent remote edit"
				if i < 2 {
					want = fmt.Sprintf("Writer %d revision 2", i)
				}
				out, err := native(cold, "show", id, "--json", "--readonly")
				if err != nil || !strings.Contains(string(out), want) {
					t.Errorf("cold recovery lost %s: %s, %v", id, out, err)
				}
			}
			if mode == "nugs" {
				t.Run("post-push outage retains checkpoint", func(t *testing.T) {
					if _, err := native(root, "update", ids[0], "--description", "Published before verification outage", "--dolt-auto-commit", "on"); err != nil {
						t.Fatal(err)
					}
					// Wrap, rather than mock, the real bd executable: only after its
					// complete native push succeeds does the server become unavailable.
					// A Dolt push can perform several Git pushes internally, so a
					// receive-pack callback alone is not this boundary.
					actualBD, err := exec.LookPath("bd")
					if err != nil {
						t.Fatal(err)
					}
					shimDir := t.TempDir()
					marker := filepath.Join(shimDir, "push-finished")
					quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
					shim := "#!/bin/sh\n" + quote(actualBD) + " \"$@\"\nstatus=$?\nif [ \"$status\" -eq 0 ]; then\ncase \" $* \" in *\" dolt push \"*) : > " + quote(marker) + ";; esac\nfi\nexit \"$status\"\n"
					if err := os.WriteFile(filepath.Join(shimDir, "bd"), []byte(shim), 0700); err != nil {
						t.Fatal(err)
					}
					originalPath := os.Getenv("PATH")
					t.Setenv("PATH", shimDir+string(os.PathListSeparator)+originalPath)
					g.mu.Lock()
					g.failureMarker = marker
					g.mu.Unlock()
					response := (Service{Remote: store.Remote, CacheDir: store.CacheDir}).Execute(ctx, Request{Operation: "beads.sync", Workspace: store.Workspace, Root: root})
					g.mu.Lock()
					g.failureMarker = ""
					g.mu.Unlock()
					t.Setenv("PATH", originalPath)
					if response.Error == nil || response.Publication == nil || response.Publication.DoltCommit == "" || !response.Publication.PushAcknowledged || response.Publication.Verified || response.Handoff != nil {
						t.Fatalf("post-push failure lost captured checkpoint or reported success: %+v", response)
					}
					t.Logf("retained publication: %+v", response.Publication)
					// Once connectivity returns, independently prove that the reported
					// checkpoint had already reached native remote history.
					tx, err := store.begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer os.RemoveAll(tx.dir)
					if _, err := store.verifyBeadsPublication(ctx, tx, info, beadsVersion{Branch: response.Publication.Branch, Commit: response.Publication.DoltCommit}); err != nil {
						t.Fatalf("reported partial checkpoint was not actually published: %v", err)
					}
					retried, err := store.SyncBeads(ctx, root)
					if err != nil || retried.Publication == nil || !retried.Publication.Verified || retried.Publication.DoltCommit != response.Publication.DoltCommit {
						t.Fatalf("retry did not verify the already-published checkpoint: %+v, %v", retried, err)
					}
				})
			}
		})
	}
}
