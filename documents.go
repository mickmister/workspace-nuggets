package nuggets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Store binds operations to one remote and workspace. Each operation uses its own
// bare repository, so concurrent CLI/MCP processes never share an index or HEAD.
type Store struct {
	Remote    string
	Workspace string
	CacheDir  string
}

type Resource struct {
	URI       string `json:"uri"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
	SHA256    string `json:"sha256,omitempty"`
}

type Document struct {
	Resource
	Content []byte `json:"-"`
}

type Receipt struct {
	Resource
	Changed bool `json:"changed"`
	Merged  bool `json:"merged"`
	content []byte
}

// Conflict includes immutable commits for inspection with Git. For
// failed publication, LocalRepository pins the intended commit on disk.
type Conflict struct {
	Name            string `json:"name"`
	ExpectedVersion string `json:"expected_version"`
	ActualVersion   string `json:"actual_version"`
	BaseCommit      string `json:"base_commit,omitempty"`
	LocalCommit     string `json:"local_commit,omitempty"`
	RemoteCommit    string `json:"remote_commit,omitempty"`
	LocalRepository string `json:"local_repository,omitempty"`
	Details         string `json:"details,omitempty"`
}

type OperationError struct {
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	Conflict *Conflict `json:"conflict,omitempty"`
}

func (e *OperationError) Error() string { return e.Message }

func invalid(message string) error {
	return &OperationError{Code: "invalid_argument", Message: message}
}

func (s Store) Ref() string { return "refs/documents/" + s.Workspace }

func (s Store) validate() error {
	if s.Remote == "" || strings.HasPrefix(s.Remote, "-") || strings.ContainsAny(s.Remote, "\x00\r\n") {
		return invalid("an explicit Git remote URL or path is required")
	}
	if s.Workspace == "" || len(s.Workspace) > 200 {
		return invalid("workspace must be a stable identifier of at most 200 characters")
	}
	for _, c := range s.Workspace {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return invalid("workspace may contain only letters, digits, hyphens and underscores")
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" || name == "." || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00\r\n\t") {
		return invalid("document name must be a safe relative slash-separated path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return invalid("document name contains a reserved path component")
		}
	}
	return nil
}

type transaction struct {
	dir       string
	env       []string
	workspace string
	remote    string
}

func (s Store) begin(ctx context.Context) (*transaction, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	remote := s.Remote
	// Relative local paths are relative to the caller, not the transaction repo.
	if !strings.Contains(remote, ":") {
		var err error
		remote, err = filepath.Abs(remote)
		if err != nil {
			return nil, err
		}
	}
	cache := s.CacheDir
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		cache = filepath.Join(base, "nugs", "transactions")
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(cache, "document-")
	if err != nil {
		return nil, err
	}
	t := &transaction{dir: dir, workspace: s.Workspace, remote: remote}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(key, "GIT_") {
			t.env = append(t.env, v)
		}
	}
	t.env = append(t.env, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Workspace nuggets", "GIT_AUTHOR_EMAIL=nugs@example.invalid",
		"GIT_COMMITTER_NAME=Workspace nuggets", "GIT_COMMITTER_EMAIL=nugs@example.invalid")
	if _, err := t.git(ctx, nil, "init", "--bare", "."); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return t, nil
}

func (t *transaction) git(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = time.Second
	cmd.Dir, cmd.Env, cmd.Stdin = t.dir, t.env, bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("git %s: %w", args[4], ctx.Err())
		}
		return out, fmt.Errorf("git: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return out, nil
}

func exitCode(err error, code int) bool {
	var e *exec.ExitError
	return errors.As(err, &e) && e.ExitCode() == code
}

func (s Store) fetch(ctx context.Context, t *transaction) (string, error) {
	_, err := t.git(ctx, nil, "ls-remote", "--exit-code", "--refs", t.remote, s.Ref())
	if exitCode(err, 2) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := t.git(ctx, nil, "fetch", "--no-tags", t.remote, s.Ref()); err != nil {
		return "", err
	}
	out, err := t.git(ctx, nil, "rev-parse", "FETCH_HEAD^{commit}")
	return strings.TrimSpace(string(out)), err
}

func versionAt(ctx context.Context, t *transaction, commit, name string) (string, error) {
	if commit == "" {
		return "", nil
	}
	out, err := t.git(ctx, nil, "ls-tree", "-z", commit, "--", ":(literal)documents/"+name)
	if err != nil || len(out) == 0 {
		return "", err
	}
	fields := strings.Fields(strings.SplitN(string(out), "\t", 2)[0])
	if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
		return "", invalid("resource is not a regular document")
	}
	return fields[2], nil
}

func readAt(ctx context.Context, t *transaction, commit, name string) (Document, error) {
	version, err := versionAt(ctx, t, commit, name)
	if err != nil {
		return Document{}, err
	}
	if version == "" {
		return Document{}, &OperationError{Code: "not_found", Message: "document not found: " + name}
	}
	content, err := t.git(ctx, nil, "cat-file", "blob", version)
	if err != nil {
		return Document{}, err
	}
	digest := sha256.Sum256(content)
	media := mime.TypeByExtension(path.Ext(name))
	if media == "" {
		media = "application/octet-stream"
	}
	return Document{Resource: Resource{URI: ResourceURI(t.workspace, name), Name: name, Version: version, Commit: commit, Size: int64(len(content)), MediaType: media, SHA256: hex.EncodeToString(digest[:])}, Content: content}, nil
}

func (s Store) List(ctx context.Context) ([]Resource, error) {
	t, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(t.dir)
	head, err := s.fetch(ctx, t)
	if err != nil {
		return nil, err
	}
	return listAt(ctx, t, head)
}

func listAt(ctx context.Context, t *transaction, head string) ([]Resource, error) {
	resources := []Resource{}
	if head == "" {
		return resources, nil
	}
	out, err := t.git(ctx, nil, "ls-tree", "-rz", "--name-only", head, "--", "documents/")
	if err != nil {
		return nil, err
	}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		name := strings.TrimPrefix(string(entry), "documents/")
		if err := validateName(name); err != nil {
			return nil, err
		}
		doc, err := readAt(ctx, t, head, name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, doc.Resource)
	}
	return resources, nil
}

func (s Store) Read(ctx context.Context, name string) (Document, error) {
	if err := validateName(name); err != nil {
		return Document{}, err
	}
	t, err := s.begin(ctx)
	if err != nil {
		return Document{}, err
	}
	defer os.RemoveAll(t.dir)
	head, err := s.fetch(ctx, t)
	if err != nil {
		return Document{}, err
	}
	return readAt(ctx, t, head, name)
}

func (t *transaction) candidate(ctx context.Context, base, name, blob string) (string, error) {
	readTree := "--empty"
	if base != "" {
		readTree = base
	}
	if _, err := t.git(ctx, nil, "read-tree", readTree); err != nil {
		return "", err
	}
	entry := "100644 " + blob + "\tdocuments/" + name + "\x00"
	if _, err := t.git(ctx, []byte(entry), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := t.git(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree)), "-m", "Upsert document " + name}
	if base != "" {
		args = append(args, "-p", base)
	}
	commit, err := t.git(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	local := strings.TrimSpace(string(commit))
	if _, err := t.git(ctx, nil, "update-ref", "refs/nugs/intent", local); err != nil {
		return "", err
	}
	return local, nil
}

// Upsert uses a blob OID as the per-resource expected version. Empty means create
// only. An intent ref is durable before network activity and retained on failure.
func (s Store) Upsert(ctx context.Context, name, expected string, content []byte) (Receipt, error) {
	if err := validateName(name); err != nil {
		return Receipt{}, err
	}
	t, err := s.begin(ctx)
	if err != nil {
		return Receipt{}, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(t.dir)
		}
	}()
	blob, err := t.git(ctx, content, "hash-object", "-w", "--stdin")
	if err != nil {
		return Receipt{}, err
	}
	local, err := t.candidate(ctx, "", name, strings.TrimSpace(string(blob)))
	if err != nil {
		return Receipt{}, err
	}
	keep = true
	merged := false
	base, lastRemote := "", ""
	failure := func(code, message, remote, details string) error {
		if remote == "" {
			remote = lastRemote
		}
		if remote != "" {
			// Pin both sides for offline inspection, including conflicted merges.
			if _, err := t.git(ctx, nil, "update-ref", "refs/nugs/remote", remote); err != nil {
				details += "; cannot pin remote evidence: " + err.Error()
			}
		}
		version, _ := versionAt(ctx, t, remote, name)
		return &OperationError{Code: code, Message: message, Conflict: &Conflict{Name: name, ExpectedVersion: expected, ActualVersion: version, BaseCommit: base, LocalCommit: local, RemoteCommit: remote, LocalRepository: t.dir, Details: details}}
	}
	base, err = s.fetch(ctx, t)
	if err != nil {
		return Receipt{}, failure("fetch_failed", "cannot fetch workspace; local intent retained", "", err.Error())
	}
	lastRemote = base
	actual, err := versionAt(ctx, t, base, name)
	if err != nil {
		return Receipt{}, failure("operation_failed", "cannot inspect remote resource; local intent retained", base, err.Error())
	}
	if actual == expected && actual != "" {
		doc, err := readAt(ctx, t, base, name)
		if err != nil {
			return Receipt{}, failure("operation_failed", "cannot read remote resource; local intent retained", base, err.Error())
		}
		if bytes.Equal(doc.Content, content) {
			keep = false
			return Receipt{Resource: doc.Resource, content: doc.Content}, nil
		}
	}
	if base != "" {
		candidate, err := t.candidate(ctx, base, name, strings.TrimSpace(string(blob)))
		if err != nil {
			return Receipt{}, failure("operation_failed", "cannot prepare workspace update; local intent retained", base, err.Error())
		}
		local = candidate
	}
	if actual != expected {
		return Receipt{}, failure("conflict", "document version changed; read before updating; local intent retained", base, "")
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := validateManifestAt(ctx, t, local, s.Workspace); err != nil {
			return Receipt{}, failure("invalid_manifest", "workspace manifest validation failed; local intent retained", lastRemote, err.Error())
		}
		_, pushErr := t.git(ctx, nil, "push", t.remote, local+":"+s.Ref())
		if pushErr == nil {
			doc, err := readAt(ctx, t, local, name)
			if err != nil {
				return Receipt{}, err
			}
			keep = false
			return Receipt{Resource: doc.Resource, Changed: true, Merged: merged, content: doc.Content}, nil
		}
		remote, err := s.fetch(ctx, t)
		if err != nil {
			return Receipt{}, failure("publish_failed", "push/fetch failed; local intent retained", "", pushErr.Error()+"; "+err.Error())
		}
		lastRemote = remote
		// A lost response is success if the remote already contains our commit.
		if remote != "" {
			if _, err := t.git(ctx, nil, "merge-base", "--is-ancestor", local, remote); err == nil {
				doc, err := readAt(ctx, t, local, name)
				if err != nil {
					return Receipt{}, err
				}
				keep = false
				return Receipt{Resource: doc.Resource, Changed: true, Merged: merged, content: doc.Content}, nil
			}
		}
		if remote == "" || remote == base {
			return Receipt{}, failure("publish_failed", "push rejected; local intent retained", remote, pushErr.Error())
		}
		out, err := t.git(ctx, nil, "merge-tree", "--write-tree", "--allow-unrelated-histories", local, remote)
		if err != nil {
			code := "publish_failed"
			if exitCode(err, 1) {
				code = "conflict"
			}
			return Receipt{}, failure(code, "automatic reconciliation stopped; local intent retained", remote, string(out)+err.Error())
		}
		mergeTree := strings.TrimSpace(string(out))
		commit, err := t.git(ctx, nil, "commit-tree", mergeTree, "-p", local, "-p", remote, "-m", "Reconcile workspace documents")
		if err != nil {
			return Receipt{}, failure("publish_failed", "cannot commit reconciliation", remote, err.Error())
		}
		local = strings.TrimSpace(string(commit))
		if _, err := t.git(ctx, nil, "update-ref", "refs/nugs/intent", local); err != nil {
			return Receipt{}, failure("publish_failed", "cannot pin reconciliation", remote, err.Error())
		}
		merged = true
	}
	return Receipt{}, failure("retry_exhausted", "remote kept advancing; local intent retained", "", "three push attempts exhausted")
}
