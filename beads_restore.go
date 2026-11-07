package nuggets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runNativeDolt(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(bounded, "dolt", args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, gitEnvironment(), time.Second
	out, err := cmd.Output()
	if err != nil {
		if bounded.Err() != nil {
			return nil, bounded.Err()
		}
		// Native transport diagnostics can contain private storage URLs.
		return nil, fmt.Errorf("native Dolt operation failed: %w", err)
	}
	return out, nil
}

type BeadsRestoration struct {
	CollectionID  string `json:"collection_id"`
	DoltCommit    string `json:"dolt_commit"`
	Ref           string `json:"ref"`
	WorkspaceRoot string `json:"workspace_root"`
	BeadsDir      string `json:"beads_dir"`
	Verified      bool   `json:"verified"`
}

// RestoreBeads attaches a verified native clone to an explicitly selected fresh
// workspace. It never initializes a database, replaces an existing collection,
// or opens a live database using a second Dolt engine.
func (s Store) RestoreBeads(ctx context.Context, root, dataDir, collectionID, commit string) (*BeadsRestoration, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if root == "" || dataDir == "" || collectionID == "" || commit == "" {
		return nil, invalid("restore requires explicit root, data-dir, collection-id and Dolt commit from a publication receipt")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(root)
	if err != nil || !stat.IsDir() {
		return nil, invalid("restore root must be an existing directory")
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dataDir))
	if err != nil {
		return nil, err
	}
	dataDir = filepath.Join(parent, filepath.Base(dataDir))
	marker := filepath.Join(root, ".beads")
	for _, path := range []string{marker, dataDir} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			if err != nil {
				return nil, err
			}
			return nil, &OperationError{Code: "conflict", Message: "restore requires an absent workspace .beads and an absent persistent data directory"}
		}
	}
	// The collection must not sit underneath the marker we are going to create.
	relative, err := filepath.Rel(marker, dataDir)
	if err != nil {
		return nil, err
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, invalid("persistent collection must be outside the workspace .beads marker")
	}
	stage, err := os.MkdirTemp(parent, ".nugs-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	beadsDir := filepath.Join(stage, ".beads")
	engineDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(engineDir, 0700); err != nil {
		return nil, err
	}
	remote, err := nativeGitURL(s.Remote)
	if err != nil {
		return nil, err
	}
	ref := "refs/dolt/" + s.Workspace
	database := "nugs"
	clone := filepath.Join(engineDir, database)
	if _, err := runNativeDolt(ctx, stage, "clone", "--ref", ref, remote, clone); err != nil {
		return nil, err
	}
	// A checkpoint remains restorable after other replicas publish descendants.
	// Materialize it only in this fresh, unattached clone. Preserve the newer tip
	// on another local branch; never reset or open an existing live collection.
	head, err := verifyNativeCheckpoint(ctx, clone, collectionID, commit)
	if err != nil {
		return nil, err
	}
	if head != commit {
		branch, err := runNativeDolt(ctx, clone, "branch", "--show-current")
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(string(branch))
		if name == "" {
			return nil, fmt.Errorf("recovered native branch is missing")
		}
		if _, err := runNativeDolt(ctx, clone, "branch", "-m", name, "nugs-tip-"+filepath.Base(stage)); err != nil {
			return nil, err
		}
		if _, err := runNativeDolt(ctx, clone, "checkout", "-b", name, commit); err != nil {
			return nil, err
		}
	}
	metadata, err := json.Marshal(map[string]string{"database": "dolt", "backend": "dolt", "dolt_mode": "embedded", "dolt_database": database, "project_id": collectionID})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("no-git-ops: true\nno-push: true\ndolt:\n  shared-server: false\n  auto-push: false\n  auto-commit: on\n"), 0600); err != nil {
		return nil, err
	}
	// Exercise the supported Beads reader/configuration interface before making
	// the destination visible. Do not run bd init or reconstruct native tables.
	checkRoot := filepath.Join(stage, "check")
	if err := os.MkdirAll(filepath.Join(checkRoot, ".beads"), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(checkRoot, ".beads", "redirect"), []byte(beadsDir+"\n"), 0600); err != nil {
		return nil, err
	}
	info, err := s.ConfigureBeads(ctx, checkRoot)
	if err != nil {
		return nil, err
	}
	var version beadsVersion
	if err := beadsJSON(ctx, checkRoot, &version, "vc", "status"); err != nil {
		return nil, err
	}
	if info.CollectionID != collectionID || !info.Configured || version.Commit != commit {
		return nil, &OperationError{Code: "conflict", Message: "Beads could not adopt the exact recovered collection and checkpoint"}
	}
	if err := os.Mkdir(dataDir, 0700); err != nil {
		return nil, err
	}
	installed := filepath.Join(dataDir, ".beads")
	if err := os.Rename(beadsDir, installed); err != nil {
		_ = os.Remove(dataDir)
		return nil, err
	}
	// Exclusive creation prevents replacement of an existing marker even if
	// another restorer won while the clone was being validated. Keep the
	// independently verified data directory for recovery on attachment failure.
	if err := os.Mkdir(marker, 0700); err != nil {
		return nil, err
	}
	redirect, err := os.OpenFile(filepath.Join(marker, "redirect"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := redirect.WriteString(installed + "\n")
	closeErr := redirect.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return &BeadsRestoration{CollectionID: collectionID, DoltCommit: commit, Ref: ref, WorkspaceRoot: root, BeadsDir: installed, Verified: true}, nil
}
