package nuggets

import (
	"context"
	"strings"
	"testing"
)

func TestDocumentSnapshotReadsRetainHistoryAndRejectFallback(t *testing.T) {
	g := newGitServer(t)
	s := g.store("document-snapshots")
	ctx := context.Background()
	first, err := s.Upsert(ctx, "review/plan.md", "", []byte("original\n"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Upsert(ctx, "review/plan.md", first.Version, []byte("revised\n"))
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.Upsert(ctx, "review/plan.md", second.Version, []byte("original\n"))
	if err != nil || third.Version != first.Version || third.Commit == first.Commit {
		t.Fatalf("revert fixture: %+v, %v", third, err)
	}
	for _, receipt := range []Receipt{first, second, third} {
		doc, err := s.ReadSnapshot(ctx, "review/plan.md", receipt.Commit)
		if err != nil || doc.Version != receipt.Version || doc.Commit != receipt.Commit {
			t.Fatalf("pinned read changed context: %+v, %v", doc, err)
		}
		want := "original\n"
		if receipt.Commit == second.Commit {
			want = "revised\n"
		}
		if string(doc.Content) != want {
			t.Fatalf("pinned bytes: %q, want %q", doc.Content, want)
		}
	}
	if _, err := s.Upsert(ctx, "review/new.md", "", []byte("added later\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSnapshot(ctx, "review/new.md", first.Commit); err == nil {
		t.Fatal("missing historical path fell back to current bytes")
	}
	other, err := g.store("another-workspace").Upsert(ctx, "review/plan.md", "", []byte("unrelated\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []string{"HEAD", "--all", strings.Repeat("0", 40), other.Commit} {
		if _, err := s.ReadSnapshot(ctx, "review/plan.md", snapshot); err == nil {
			t.Fatalf("accepted mutable, missing or foreign snapshot: %q", snapshot)
		}
	}
}
