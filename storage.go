package nuggets

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const storageBootstrapRef = "refs/heads/nugs-storage"

type StorageReceipt struct {
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Changed bool   `json:"changed"`
}

func storageHead(ctx context.Context, t *transaction) (*StorageReceipt, error) {
	out, err := t.git(ctx, nil, "ls-remote", "--heads", t.remote)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasPrefix(fields[1], "refs/heads/") {
			return &StorageReceipt{Ref: fields[1], Commit: fields[0]}, nil
		}
	}
	return nil, nil
}

// BootstrapStorage explicitly initializes the selected DATA repository for native
// Dolt transport. Existing ordinary branches and every custom ref are preserved.
func (s Store) BootstrapStorage(ctx context.Context) (*StorageReceipt, error) {
	t, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(t.dir)
	if existing, err := storageHead(ctx, t); err != nil || existing != nil {
		return existing, err
	}
	tree, err := t.git(ctx, nil, "mktree")
	if err != nil {
		return nil, err
	}
	// A stable empty root makes simultaneous bootstrap requests converge without
	// choosing a winner or rewriting another publisher's history.
	t.env = append(t.env, "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	out, err := t.git(ctx, nil, "commit-tree", strings.TrimSpace(string(tree)), "-m", "Initialize workspace nuggets data storage")
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(out))
	_, pushErr := t.git(ctx, nil, "push", t.remote, commit+":"+storageBootstrapRef)
	if pushErr != nil {
		// A different bootstrap may have completed since our observation. Any
		// ordinary branch satisfies Dolt's prerequisite; never overwrite it.
		existing, readErr := storageHead(ctx, t)
		if readErr == nil && existing != nil {
			return existing, nil
		}
		return nil, pushErr
	}
	verified, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, storageBootstrapRef)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(verified))
	if len(fields) != 2 || fields[0] != commit || fields[1] != storageBootstrapRef {
		return nil, fmt.Errorf("storage bootstrap ref changed before publication could be verified")
	}
	return &StorageReceipt{Ref: storageBootstrapRef, Commit: commit, Changed: true}, nil
}
