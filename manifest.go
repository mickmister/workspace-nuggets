package nuggets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ManifestDocument is a reserved resource in the existing documents namespace.
const ManifestDocument = "workspace/manifest.json"

type RepositoryRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type RepositoryPointer struct {
	Path        string             `json:"path"`
	Branch      string             `json:"branch,omitempty"`
	Commit      string             `json:"commit"`
	Detached    bool               `json:"detached"`
	Dirty       bool               `json:"dirty"`
	Remotes     []RepositoryRemote `json:"remotes"`
	Publication string             `json:"publication"`
}

type Manifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Workspace     string              `json:"workspace"`
	ObservedAt    string              `json:"observed_at"`
	Purpose       string              `json:"purpose,omitempty"`
	Repositories  []RepositoryPointer `json:"repositories,omitempty"`
}

type WorkspaceSnapshot struct {
	Resources       []Resource
	Manifest        *Manifest
	ManifestVersion string
	Commit          string
}

func parseManifest(content []byte, workspace string) (*Manifest, error) {
	bad := func(message string) (*Manifest, error) {
		return nil, &OperationError{Code: "invalid_manifest", Message: message}
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return bad("workspace manifest must be valid JSON: " + err.Error())
	}
	if manifest.SchemaVersion != 1 || manifest.Workspace != workspace {
		return bad("manifest needs schema_version 1 and matching workspace")
	}
	if len(manifest.Repositories) == 0 && strings.TrimSpace(manifest.Purpose) == "" {
		return bad("manifest needs a nonblank purpose explaining why a workspace without repositories exists")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.ObservedAt); err != nil {
		return bad("manifest observed_at must be an RFC3339 timestamp")
	}
	seen := map[string]bool{}
	for _, repo := range manifest.Repositories {
		if repo.Path != "." && validateName(repo.Path) != nil || seen[repo.Path] {
			return bad("repository paths must be unique safe relative paths")
		}
		seen[repo.Path] = true
		if len(repo.Commit) != 40 && len(repo.Commit) != 64 {
			return bad("repository commit must be a full Git object ID")
		}
		for _, c := range repo.Commit {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return bad("repository commit must be hexadecimal")
			}
		}
		if repo.Detached && repo.Branch != "" || !repo.Detached && !strings.HasPrefix(repo.Branch, "refs/heads/") {
			return bad("repository branch must be a full branch ref, or omitted for detached HEAD")
		}
		if repo.Publication != "unverified" && repo.Publication != "no_remote" {
			return bad("repository publication must disclose unverified or no_remote")
		}
		if (len(repo.Remotes) == 0) != (repo.Publication == "no_remote") {
			return bad("repository publication must agree with remote availability")
		}
		for _, remote := range repo.Remotes {
			if remote.Name == "" || remote.URL == "" || portableRemote(remote.URL) != remote.URL {
				return bad("repository remotes need names and credential-free URLs")
			}
		}
	}
	return &manifest, nil
}

func portableRemote(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		if u.Scheme == "http" || u.Scheme == "https" {
			u.User, u.RawQuery, u.Fragment = nil, "", ""
		} else if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword {
				u.User = url.User(u.User.Username())
			}
		}
		return u.String()
	}
	return raw
}

// ObserveManifest reads only explicitly selected local repositories. It does not
// checkout branches, push source code, or assume a local commit is published.
func (s Store) ObserveManifest(ctx context.Context, root string, repositories []string) (*Manifest, error) {
	return s.ObserveManifestWithPurpose(ctx, root, repositories, "")
}

// ObserveManifestWithPurpose supports standalone research/planning workspaces;
// source pointers remain explicit observations when repositories are supplied.
func (s Store) ObserveManifestWithPurpose(ctx context.Context, root string, repositories []string, purpose string) (*Manifest, error) {
	if len(repositories) == 0 && strings.TrimSpace(purpose) == "" {
		return nil, invalid("supply --repo or a nonblank --purpose explaining why this workspace exists")
	}
	t, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(t.dir)
	manifest := &Manifest{SchemaVersion: 1, Workspace: s.Workspace, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Purpose: strings.TrimSpace(purpose)}
	if len(repositories) == 0 {
		// No Git repository, branch, or even local source root is required.
		return manifest, nil
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	for _, repoPath := range repositories {
		if !filepath.IsAbs(repoPath) {
			repoPath = filepath.Join(root, repoPath)
		}
		repoPath, err = filepath.EvalSymlinks(repoPath)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(root, repoPath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, invalid("repository must be within the selected workspace root")
		}
		reader := &transaction{dir: repoPath, env: t.env}
		read := func(args ...string) (string, error) {
			out, err := reader.git(ctx, nil, args...)
			return strings.TrimSpace(string(out)), err
		}
		top, err := read("rev-parse", "--show-toplevel")
		if err != nil {
			return nil, fmt.Errorf("repository %s: %w", relative, err)
		}
		top, err = filepath.EvalSymlinks(top)
		if err != nil || top != repoPath {
			return nil, invalid("select repository roots, not subdirectories: " + relative)
		}
		commit, err := read("rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return nil, fmt.Errorf("repository %s needs a committed HEAD: %w", relative, err)
		}
		branch, err := read("symbolic-ref", "--quiet", "HEAD")
		if err != nil && !exitCode(err, 1) {
			return nil, err
		}
		status, err := read("--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=normal")
		if err != nil {
			return nil, err
		}
		pointer := RepositoryPointer{Path: filepath.ToSlash(relative), Branch: branch, Commit: commit, Detached: branch == "", Dirty: status != "", Remotes: []RepositoryRemote{}, Publication: "no_remote"}
		remotes, err := read("remote")
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Fields(remotes) {
			urls, err := read("remote", "get-url", "--all", name)
			if err != nil {
				return nil, err
			}
			for _, remoteURL := range strings.Split(urls, "\n") {
				pointer.Remotes = append(pointer.Remotes, RepositoryRemote{Name: name, URL: portableRemote(remoteURL)})
			}
			pointer.Publication = "unverified"
		}
		confirmedCommit, err := read("rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || confirmedCommit != commit {
			return nil, invalid("repository HEAD changed during observation; retry: " + relative)
		}
		confirmedBranch, err := read("symbolic-ref", "--quiet", "HEAD")
		if err != nil && !exitCode(err, 1) || confirmedBranch != branch {
			return nil, invalid("repository branch changed during observation; retry: " + relative)
		}
		manifest.Repositories = append(manifest.Repositories, pointer)
	}
	sort.Slice(manifest.Repositories, func(i, j int) bool { return manifest.Repositories[i].Path < manifest.Repositories[j].Path })
	content, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return parseManifest(content, s.Workspace)
}

func (s Store) Provision(ctx context.Context, root string, repositories []string, expected string) (*Manifest, Receipt, error) {
	return s.ProvisionWithPurpose(ctx, root, repositories, "", expected)
}

func (s Store) ProvisionWithPurpose(ctx context.Context, root string, repositories []string, purpose, expected string) (*Manifest, Receipt, error) {
	manifest, err := s.ObserveManifestWithPurpose(ctx, root, repositories, purpose)
	if err != nil {
		return nil, Receipt{}, err
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, Receipt{}, err
	}
	receipt, err := s.Upsert(ctx, ManifestDocument, expected, append(content, '\n'))
	if err == nil {
		manifest, err = parseManifest(receipt.content, s.Workspace)
	}
	return manifest, receipt, err
}

func (s Store) Inspect(ctx context.Context) (WorkspaceSnapshot, error) {
	t, err := s.begin(ctx)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	defer os.RemoveAll(t.dir)
	head, err := s.fetch(ctx, t)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	resources, err := listAt(ctx, t, head)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	snapshot := WorkspaceSnapshot{Resources: resources, Commit: head}
	doc, err := readAt(ctx, t, head, ManifestDocument)
	if err != nil {
		var operationError *OperationError
		if errors.As(err, &operationError) && operationError.Code == "not_found" {
			return snapshot, &OperationError{Code: "manifest_required", Message: "workspace/manifest.json is required; provision the workspace with repositories or a purpose"}
		}
		return snapshot, err
	}
	snapshot.Manifest, err = parseManifest(doc.Content, s.Workspace)
	snapshot.ManifestVersion = doc.Version
	return snapshot, err
}

func validateManifestAt(ctx context.Context, t *transaction, commit, workspace string) error {
	version, err := versionAt(ctx, t, commit, ManifestDocument)
	if err != nil || version == "" {
		return err
	}
	doc, err := readAt(ctx, t, commit, ManifestDocument)
	if err != nil {
		return err
	}
	_, err = parseManifest(doc.Content, workspace)
	return err
}
