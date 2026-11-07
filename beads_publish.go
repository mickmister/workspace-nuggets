package nuggets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BeadsPublication is deliberately free of storage URLs and authentication.
// It attests a recovered committed snapshot, not an ongoing synchronization.
type BeadsPublication struct {
	CollectionID string `json:"collection_id"`
	Ref          string `json:"ref"`
	GitCommit    string `json:"git_commit"`
	DoltCommit   string `json:"dolt_commit"`
	Branch       string `json:"branch"`
	Verified     bool   `json:"verified"`
}

type beadsVersion struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

// PublishBeads commits via the supported Beads lifecycle and verifies the
// non-force push using an independent native clone. Local edits may continue:
// the receipt attests the captured checkpoint, not the latest local HEAD.
// Divergence fails without pulling, choosing conflict winners, or force-pushing.
func (s Store) PublishBeads(ctx context.Context, root string) (*BeadsPublication, error) {
	info, err := s.InspectBeads(ctx, root)
	if err != nil {
		return nil, err
	}
	if !info.Configured {
		return nil, &OperationError{Code: "sync_disabled", Message: "workspace native origin/ref is not configured for publication; inspect/configure Beads first"}
	}
	if _, err := runBeads(ctx, info.WorkspaceRoot, "dolt", "commit", "-m", "nugs: checkpoint workspace before publication"); err != nil {
		return nil, fmt.Errorf("checkpoint workspace Beads: %w", err)
	}
	var version beadsVersion
	if err := beadsJSON(ctx, info.WorkspaceRoot, &version, "vc", "status"); err != nil {
		return nil, err
	}
	if version.Commit == "" || version.Branch == "" {
		return nil, fmt.Errorf("Beads did not identify the checkpoint commit and branch")
	}
	if _, err := runBeads(ctx, info.WorkspaceRoot, "dolt", "push", "--remote", "origin"); err != nil {
		message := strings.ReplaceAll(err.Error(), info.DesiredURL, "<storage>")
		message = strings.ReplaceAll(message, s.Remote, "<storage>")
		return nil, &OperationError{Code: "push_failed", Message: "native push failed; local checkpoint retained: " + message}
	}
	t, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(t.dir)
	return s.verifyBeadsPublication(ctx, t, info, version)
}

func (s Store) verifyBeadsPublication(ctx context.Context, t *transaction, info *BeadsInfo, version beadsVersion) (*BeadsPublication, error) {
	// A remote advance during cloning is retried, without stopping local writers.
	// The Git receipt and independent clone must describe one observed remote tip.
	for attempt := 0; attempt < 3; attempt++ {
		refs, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, info.DesiredRef)
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(refs))
		if len(fields) != 2 || fields[1] != info.DesiredRef {
			return nil, fmt.Errorf("native publication ref was not advertised")
		}
		// Never open the live embedded database using a second engine. Recovery is
		// performed in an isolated disposable directory, including its SQL read.
		recovered := filepath.Join(t.dir, fmt.Sprintf("recovered-%d", attempt))
		if _, err := runNativeDolt(ctx, t.dir, "clone", "--ref", info.DesiredRef, info.DesiredURL, recovered); err != nil {
			return nil, err
		}
		if _, err := verifyNativeCheckpoint(ctx, recovered, info.CollectionID, version.Commit); err != nil {
			return nil, err
		}
		// Bind the Git receipt to the same observed remote state, rather than to a
		// ref that advanced while recovery was in progress.
		afterRefs, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, info.DesiredRef)
		if err != nil {
			return nil, err
		}
		if string(afterRefs) != string(refs) {
			continue
		}
		return &BeadsPublication{CollectionID: info.CollectionID, Ref: info.DesiredRef, GitCommit: fields[0], DoltCommit: version.Commit, Branch: version.Branch, Verified: true}, nil
	}
	return nil, &OperationError{Code: "conflict", Message: "remote advanced during all publication verification attempts; checkpoint retained, retry synchronization"}
}
