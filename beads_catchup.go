package nuggets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type BeadsCatchUp struct {
	CollectionID string `json:"collection_id"`
	Ref          string `json:"ref"`
	BeforeCommit string `json:"before_commit"`
	RemoteCommit string `json:"remote_commit"`
	DoltCommit   string `json:"dolt_commit,omitempty"`
	Verified     bool   `json:"verified"`
}

// CatchUpBeads uses Beads' fetch-only federation status and bare vc merge,
// never its auto-resolving pull. The remote revision is pinned by hash, not a
// movable tracking-branch name. Newer committed local edits merge normally.
func (s Store) CatchUpBeads(ctx context.Context, root, expectedCommit string) (*BeadsCatchUp, error) {
	if expectedCommit == "" {
		return nil, invalid("catch-up requires the exact remote Dolt commit from a publication receipt")
	}
	info, err := s.InspectBeads(ctx, root)
	if err != nil {
		return nil, err
	}
	if !info.Configured {
		return nil, &OperationError{Code: "sync_disabled", Message: "workspace native origin/ref is not configured"}
	}
	if _, err := runBeads(ctx, info.WorkspaceRoot, "dolt", "commit", "-m", "nugs: checkpoint workspace before catch-up"); err != nil {
		return nil, err
	}
	var before beadsVersion
	if err := beadsJSON(ctx, info.WorkspaceRoot, &before, "vc", "status"); err != nil {
		return nil, err
	}
	if before.Commit == "" || before.Branch == "" {
		return nil, fmt.Errorf("Beads did not identify the local checkpoint")
	}
	receipt := &BeadsCatchUp{CollectionID: info.CollectionID, Ref: info.DesiredRef, BeforeCommit: before.Commit, RemoteCommit: expectedCommit}
	t, err := s.begin(ctx)
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(t.dir)
	clone := filepath.Join(t.dir, "remote-checkpoint")
	if _, err := runNativeDolt(ctx, t.dir, "clone", "--ref", info.DesiredRef, info.DesiredURL, clone); err != nil {
		return receipt, err
	}
	if _, err := verifyNativeCheckpoint(ctx, clone, info.CollectionID, expectedCommit); err != nil {
		return receipt, err
	}
	// This supported interface fetches without merging. Its exit status alone
	// does not prove reachability, so validate its structured result as well.
	out, err := runBeads(ctx, info.WorkspaceRoot, "federation", "status", "--peer", "origin", "--json")
	if err != nil {
		return receipt, &OperationError{Code: "fetch_failed", Message: "native fetch failed; local checkpoint retained"}
	}
	var fetched struct {
		Peers []struct {
			Reachable bool
			Status    *struct {
				Peer         string
				HasConflicts bool
			}
		} `json:"peers"`
		Pending *int `json:"pendingChanges"`
	}
	if err := json.Unmarshal(out, &fetched); err != nil {
		return receipt, fmt.Errorf("decode Beads fetch status: %w", err)
	}
	if len(fetched.Peers) != 1 || !fetched.Peers[0].Reachable || fetched.Peers[0].Status == nil || fetched.Peers[0].Status.Peer != "origin" || fetched.Peers[0].Status.HasConflicts || fetched.Pending == nil || *fetched.Pending != 0 {
		return receipt, &OperationError{Code: "fetch_failed", Message: "native fetch did not confirm a reachable origin and clean local working set"}
	}
	var fetchedVersion beadsVersion
	if err := beadsJSON(ctx, info.WorkspaceRoot, &fetchedVersion, "vc", "status"); err != nil {
		return receipt, err
	}
	if fetchedVersion.Branch != before.Branch || fetchedVersion.Commit == "" {
		return receipt, &OperationError{Code: "conflict", Message: "local branch changed during fetch; retry on the intended branch"}
	}
	receipt.BeforeCommit = fetchedVersion.Commit
	out, err = runBeads(ctx, info.WorkspaceRoot, "vc", "merge", expectedCommit, "--json")
	if err != nil {
		return receipt, &OperationError{Code: "merge_failed", Message: "native clean merge refused; inspect the retained local and remote checkpoints: " + err.Error()}
	}
	var merged struct {
		Merged    string          `json:"merged"`
		Conflicts json.RawMessage `json:"conflicts"`
	}
	if err := json.Unmarshal(out, &merged); err != nil {
		return receipt, fmt.Errorf("decode native merge result: %w", err)
	}
	if merged.Merged != expectedCommit || strings.TrimSpace(string(merged.Conflicts)) != "0" {
		return receipt, &OperationError{Code: "conflict", Message: "native merge did not confirm a conflict-free result; publication stopped"}
	}
	var after beadsVersion
	if err := beadsJSON(ctx, info.WorkspaceRoot, &after, "vc", "status"); err != nil {
		return receipt, err
	}
	if after.Commit == "" || after.Branch != before.Branch {
		return receipt, fmt.Errorf("Beads did not identify the merged checkpoint on the original branch")
	}
	receipt.DoltCommit, receipt.Verified = after.Commit, true
	return receipt, nil
}
