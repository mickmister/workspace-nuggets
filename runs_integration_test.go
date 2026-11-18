package nuggets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWorkflowRunIdentityPinnedReadsAndIsolation(t *testing.T) {
	g := newGitServer(t)
	s := g.store("run-tests")
	ctx := context.Background()
	run, initial, err := s.CreateRun(ctx, "goals-a1", "Capture user intent", "beads://collection/task-123")
	if err != nil || run.Manifest.ID != "goals-a1" {
		t.Fatalf("create: %+v, %v", run, err)
	}
	again, repeated, err := s.CreateRun(ctx, "goals-a1", "Capture user intent", "beads://collection/task-123")
	if err != nil || repeated.Changed || repeated.Version != initial.Version || again.Manifest.CreatedAt != run.Manifest.CreatedAt {
		t.Fatalf("idempotent create: %+v, %v", again, err)
	}
	if _, _, err := s.CreateRun(ctx, "goals-a1", "Different intent", ""); err == nil {
		t.Fatal("run identity reused for different inputs")
	}
	first, err := s.WriteRun(ctx, "goals-a1", "goals.md", "", []byte("original\n"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.WriteRun(ctx, "goals-a1", "goals.md", first.Version, []byte("new draft\n"))
	if err != nil || second.Version == first.Version {
		t.Fatalf("update: %+v, %v", second, err)
	}
	old, err := s.ReadRun(ctx, "goals-a1", "goals.md", first.Commit)
	if err != nil || string(old.Content) != "original\n" || old.Commit != first.Commit {
		t.Fatalf("pinned read drifted: %+v, %v", old, err)
	}
	_, resources, err := s.InspectRun(ctx, "goals-a1", initial.Commit)
	if err != nil || len(resources) != 1 {
		t.Fatalf("snapshot inspection included newer artifacts: %+v, %v", resources, err)
	}
	if _, _, err := s.CreateRun(ctx, "goals-b2", "Other run", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRun(ctx, "goals-b2", "goals.md", ""); err == nil {
		t.Fatal("another run read the first run's artifact")
	}
	for _, name := range []string{"../escape", "run.json", "run.json/child"} {
		if _, err := s.WriteRun(ctx, "goals-a1", name, "", nil); err == nil {
			t.Fatalf("accepted reserved/escaping resource %q", name)
		}
	}
	for _, snapshot := range []string{"HEAD", "--all", strings.Repeat("0", 40)} {
		if _, err := s.ReadRun(ctx, "goals-a1", "goals.md", snapshot); err == nil {
			t.Fatalf("accepted unpinned/unreachable snapshot %q", snapshot)
		}
	}
}

func TestWorkflowRunStrictConcurrentControlWrites(t *testing.T) {
	for _, independent := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-resource", true: "independent-resource"}[independent], func(t *testing.T) {
			g := newGitServer(t)
			a, b := g.store("run-race"), g.store("run-race")
			ctx := context.Background()
			if _, _, err := a.CreateRun(ctx, "run-1", "Concurrent records", ""); err != nil {
				t.Fatal(err)
			}
			base := "start\n1\n2\n3\n4\n5\n6\n7\n8\n9\nend\n"
			first, err := a.WriteRun(ctx, "run-1", "control.md", "", []byte(base))
			if err != nil {
				t.Fatal(err)
			}
			writer := make(chan error, 1)
			g.mu.Lock()
			g.beforePush = func() {
				name, expected := "control.md", first.Version
				if independent {
					name, expected = "reviews/scope.md", ""
				}
				_, err := b.WriteRun(ctx, "run-1", name, expected, []byte(strings.Replace(base, "end", "remote-end", 1)))
				writer <- err
			}
			g.mu.Unlock()
			_, err = a.WriteRun(ctx, "run-1", "control.md", first.Version, []byte(strings.Replace(base, "start", "local-start", 1)))
			if otherErr := <-writer; otherErr != nil {
				t.Fatal(otherErr)
			}
			var failure *OperationError
			if independent {
				if err != nil {
					t.Fatalf("independent output blocked: %v", err)
				}
			} else if !errors.As(err, &failure) || failure.Code != "conflict" || failure.Conflict == nil {
				t.Fatalf("strict control write content-merged: %v", err)
			}
		})
	}
}
