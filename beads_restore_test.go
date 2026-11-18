package nuggets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreBeadsProtectsExistingDestinations(t *testing.T) {
	s := Store{Remote: "https://example.invalid/storage.git", Workspace: "restore"}
	ctx := context.Background()
	for _, kind := range []string{"marker", "data", "marker-symlink", "data-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, parent := t.TempDir(), t.TempDir()
			data := filepath.Join(parent, "collection")
			target := filepath.Join(root, ".beads")
			if kind == "data" || kind == "data-symlink" {
				target = data
			}
			if kind == "marker-symlink" || kind == "data-symlink" {
				if err := os.Symlink(filepath.Join(parent, "absent"), target); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RestoreBeads(ctx, root, data, "collection", "commit"); err == nil {
				t.Fatal("existing destination accepted")
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("existing destination removed: %v", err)
			}
		})
	}
	root, parent := t.TempDir(), t.TempDir()
	data := filepath.Join(parent, "collection")
	if _, err := s.RestoreBeads(ctx, root, data, "", "commit"); err == nil {
		t.Fatal("missing collection identity accepted")
	}
	if _, err := s.RestoreBeads(ctx, root, data, "collection", ""); err == nil {
		t.Fatal("missing checkpoint accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, ".beads")); !os.IsNotExist(err) {
		t.Fatal("invalid request attached a workspace")
	}
}
