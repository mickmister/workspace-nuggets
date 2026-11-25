package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type BeadsRemote struct {
	Name   string            `json:"name"`
	URL    string            `json:"url"`
	Params map[string]string `json:"params,omitempty"`
}

type BeadsInfo struct {
	WorkspaceRoot string        `json:"workspace_root"`
	BeadsDir      string        `json:"beads_dir"`
	DataDir       string        `json:"data_dir"`
	Database      string        `json:"database"`
	Mode          string        `json:"mode"`
	CollectionID  string        `json:"collection_id"`
	DesiredURL    string        `json:"desired_url"`
	DesiredRef    string        `json:"desired_ref"`
	DeclaredURL   string        `json:"declared_url,omitempty"`
	Remotes       []BeadsRemote `json:"remotes"`
	Configured    bool          `json:"configured"`
	Changed       bool          `json:"changed"`
	NoPush        bool          `json:"no_push"`
	AutoPush      bool          `json:"auto_push"`
}

// WorkspaceBeadsRoot selects the outer workspace redirect, ignoring nested
// repository stores/redirects. Explicit root selection is checked exactly.
func WorkspaceBeadsRoot(start, explicit string) (string, error) {
	if explicit != "" {
		root, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(root, ".beads", "redirect")); err != nil {
			return "", invalid("workspace root must contain .beads/redirect; repository database discovery is not used")
		}
		return filepath.EvalSymlinks(root)
	}
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	root := ""
	for {
		_, err := os.Stat(filepath.Join(current, ".beads", "redirect"))
		if err == nil {
			root = current
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if root == "" {
		return "", invalid("no workspace-root .beads/redirect found; nugs does not initialize or choose a repository-local Beads database")
	}
	return root, nil
}

func beadsEnvironment(root string) []string {
	var env []string
	for _, value := range gitEnvironment() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "BEADS_") {
			env = append(env, value)
		}
	}
	return append(env, "BEADS_DIR="+filepath.Join(root, ".beads"), "BEADS_DOLT_SHARED_SERVER=false")
}

func runBeads(ctx context.Context, root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", append([]string{"--sandbox"}, args...)...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = root, beadsEnvironment(root), time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(string(out))
		}
		return out, fmt.Errorf("bd: %s: %w", message, err)
	}
	return out, nil
}

func beadsJSON(ctx context.Context, root string, target any, args ...string) error {
	out, err := runBeads(ctx, root, append(args, "--json", "--readonly")...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, target); err != nil {
		return fmt.Errorf("decode bd response: %w", err)
	}
	return nil
}

func nativeGitURL(remote string) (string, error) {
	if !strings.Contains(remote, ":") || filepath.IsAbs(remote) {
		absolute, err := filepath.Abs(remote)
		if err != nil {
			return "", err
		}
		return (&url.URL{Scheme: "git+file", Path: filepath.ToSlash(absolute)}).String(), nil
	}
	if !strings.Contains(remote, "://") && strings.Contains(remote, "@") {
		host, repo, _ := strings.Cut(remote, ":")
		if !strings.HasPrefix(repo, "/") {
			repo = "/./" + repo
		}
		remote = "ssh://" + host + repo
	}
	u, err := url.Parse(remote)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", invalid("Beads storage must be a Git URL without query parameters or fragments")
	}
	switch u.Scheme {
	case "https", "http", "ssh", "file":
		u.Scheme = "git+" + u.Scheme
	case "git+https", "git+http", "git+ssh", "git+file":
	default:
		return "", invalid("unsupported native Git transport for Beads")
	}
	return u.String(), nil
}

func (s Store) resolveBeads(ctx context.Context, explicitRoot string) (*BeadsInfo, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := WorkspaceBeadsRoot(cwd, explicitRoot)
	if err != nil {
		return nil, err
	}
	var where struct {
		Path           string `json:"path"`
		RedirectedFrom string `json:"redirected_from"`
	}
	if err := beadsJSON(ctx, root, &where, "where"); err != nil {
		return nil, err
	}
	marker := filepath.Join(root, ".beads")
	if filepath.Clean(where.RedirectedFrom) != marker || filepath.Clean(where.Path) == marker {
		return nil, invalid("Beads did not resolve the selected workspace redirect; refusing to configure another database")
	}
	var metadata struct {
		Mode      string `json:"dolt_mode"`
		Database  string `json:"dolt_database"`
		ProjectID string `json:"project_id"`
	}
	content, err := os.ReadFile(filepath.Join(where.Path, "metadata.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &metadata); err != nil {
		return nil, err
	}
	if metadata.Mode != "embedded" {
		return nil, &OperationError{Code: "unsupported_backend", Message: "custom-ref setup currently supports workspace embedded Dolt only; server databases must use their native configuration interface"}
	}
	var show struct {
		Backend  string `json:"backend"`
		DataDir  string `json:"data_dir"`
		Database string `json:"database"`
		Embedded bool   `json:"embedded"`
	}
	if err := beadsJSON(ctx, root, &show, "dolt", "show"); err != nil {
		return nil, err
	}
	if show.Backend != "dolt" || !show.Embedded || show.DataDir != filepath.Join(where.Path, "embeddeddolt") || show.Database != metadata.Database || metadata.ProjectID == "" || show.Database == "." || filepath.Base(show.Database) != show.Database || strings.ContainsAny(show.Database, "/\\") {
		return nil, invalid("unsupported or inconsistent workspace Beads layout")
	}
	remote, err := nativeGitURL(s.Remote)
	if err != nil {
		return nil, err
	}
	info := &BeadsInfo{WorkspaceRoot: root, BeadsDir: where.Path, DataDir: show.DataDir, Database: show.Database, Mode: metadata.Mode, CollectionID: metadata.ProjectID, DesiredURL: remote, DesiredRef: "refs/dolt/" + s.Workspace, Remotes: []BeadsRemote{}}
	var config []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := beadsJSON(ctx, root, &config, "config", "show"); err != nil {
		return nil, err
	}
	for _, setting := range config {
		if setting.Key == "no-push" {
			info.NoPush = setting.Value == "true"
		}
		if setting.Key == "dolt.auto-push" {
			info.AutoPush = setting.Value == "true"
		}
		if setting.Key == "sync.remote" {
			info.DeclaredURL = setting.Value
		}
		if setting.Key == "dolt.local-only" && setting.Value == "true" {
			return nil, &OperationError{Code: "sync_disabled", Message: "workspace Beads has dolt.local-only=true; remove that native setting before adding a remote"}
		}
	}
	return info, nil
}

func (info *BeadsInfo) withLock(fn func() error) error {
	lock := flock.New(filepath.Join(info.DataDir, ".lock"))
	defer lock.Close()
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return &OperationError{Code: "beads_busy", Message: "workspace Beads setup/init lock is held; retry after the other setup finishes"}
	}
	return fn()
}

func (info *BeadsInfo) readRemotes() error {
	data, err := os.ReadFile(filepath.Join(info.DataDir, info.Database, ".dolt", "repo_state.json"))
	if err != nil {
		return err
	}
	var state struct {
		Remotes map[string]BeadsRemote `json:"remotes"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	info.Remotes = []BeadsRemote{}
	info.Configured = false
	for name, remote := range state.Remotes {
		remote.Name = name
		info.Remotes = append(info.Remotes, remote)
		if name == "origin" && remote.URL == info.DesiredURL && remote.Params["git_ref"] == info.DesiredRef && info.DeclaredURL == info.DesiredURL && !info.NoPush {
			info.Configured = true
		}
	}
	sort.Slice(info.Remotes, func(i, j int) bool { return info.Remotes[i].Name < info.Remotes[j].Name })
	return nil
}

func (s Store) InspectBeads(ctx context.Context, root string) (*BeadsInfo, error) {
	info, err := s.resolveBeads(ctx, root)
	if err != nil {
		return nil, err
	}
	err = info.withLock(info.readRemotes)
	return info, err
}

// ConfigureBeads changes only native remote configuration. It never initializes
// a repository database, commits task changes, pulls, pushes or enables auto-push.
func (s Store) ConfigureBeads(ctx context.Context, root string) (*BeadsInfo, error) {
	info, err := s.resolveBeads(ctx, root)
	if err != nil {
		return nil, err
	}
	if info.DeclaredURL != "" && info.DeclaredURL != info.DesiredURL {
		return info, &OperationError{Code: "conflict", Message: "workspace already declares a different sync.remote; existing collection binding preserved"}
	}
	err = info.withLock(func() error {
		if err := info.readRemotes(); err != nil {
			return err
		}
		for _, remote := range info.Remotes {
			if remote.Name == "origin" {
				if remote.URL != info.DesiredURL || remote.Params["git_ref"] != info.DesiredRef {
					return &OperationError{Code: "conflict", Message: "workspace origin has a different URL or Git ref; refusing to repoint this collection"}
				}
				return nil
			}
		}
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "dolt", "remote", "add", "--ref", info.DesiredRef, "origin", info.DesiredURL)
		cmd.Dir, cmd.Env, cmd.WaitDelay = filepath.Join(info.DataDir, info.Database), gitEnvironment(), time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("dolt remote add --ref: %s: %w", strings.TrimSpace(string(out)), err)
		}
		info.Changed = true
		if err := info.readRemotes(); err != nil {
			return err
		}
		for _, remote := range info.Remotes {
			if remote.Name == "origin" && remote.URL == info.DesiredURL && remote.Params["git_ref"] == info.DesiredRef {
				return nil
			}
		}
		return fmt.Errorf("Dolt did not persist the requested workspace remote/ref")
	})
	if err != nil {
		return info, err
	}
	if info.DeclaredURL != info.DesiredURL {
		if _, err := runBeads(ctx, info.WorkspaceRoot, "config", "set", "sync.remote", info.DesiredURL); err != nil {
			return info, fmt.Errorf("remote added but native sync.remote declaration failed: %w", err)
		}
		info.DeclaredURL, info.Changed = info.DesiredURL, true
	}
	if info.NoPush {
		if _, err := runBeads(ctx, info.WorkspaceRoot, "config", "set", "no-push", "false"); err != nil {
			return info, fmt.Errorf("remote configured but explicit pushes remain disabled: %w", err)
		}
		info.NoPush, info.Changed = false, true
	}
	// Verify that Beads itself sees the native remote after the lock is released.
	var remotes []BeadsRemote
	if err := beadsJSON(ctx, info.WorkspaceRoot, &remotes, "dolt", "remote", "list"); err != nil {
		return info, err
	}
	visible := false
	for _, remote := range remotes {
		if remote.Name == "origin" && remote.URL == info.DesiredURL {
			visible = true
		}
	}
	if !visible {
		return info, fmt.Errorf("Beads does not see the configured workspace remote")
	}
	err = info.withLock(info.readRemotes)
	return info, err
}
