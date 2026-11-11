package nuggets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// WorkspaceContext is safe to return to ordinary clients. Physical storage
// locations are setup details, not workspace identity or resource selectors.
type WorkspaceContext struct {
	Reference  string `json:"workspace_ref"`
	Engagement string `json:"engagement"`
	Workspace  string `json:"workspace"`
}

type WorkspaceBinding struct {
	WorkspaceContext
	Remote         string                   `json:"remote"`
	CacheDir       string                   `json:"cache_dir"`
	CollectionID   string                   `json:"collection_id,omitempty"`
	MessageTargets map[string]MessageTarget `json:"message_targets,omitempty"`
	MediaTargets   map[string]MessageTarget `json:"media_targets,omitempty"`
}

type workspaceAttachment struct {
	Root      string `json:"root"`
	Reference string `json:"workspace_ref"`
}

type Registry struct {
	Version     int                   `json:"version"`
	Bindings    []WorkspaceBinding    `json:"bindings"`
	Attachments []workspaceAttachment `json:"attachments"`
}

func RegistryPath() (string, error) {
	p, err := ConfigPath()
	return filepath.Join(filepath.Dir(p), "registry.json"), err
}

func readRegistry(filename string) (Registry, error) {
	f, err := os.Open(filename)
	if os.IsNotExist(err) {
		return Registry{Version: 1}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	defer f.Close()
	var r Registry
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, fmt.Errorf("workspace registry: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF || r.Version != 1 {
		return r, invalid("unsupported or malformed workspace registry")
	}
	refs := map[string]bool{}
	for _, b := range r.Bindings {
		if !validWorkspaceReference(b.Reference) || refs[b.Reference] || b.Engagement == "" || !filepath.IsAbs(b.CacheDir) {
			return r, invalid("invalid or duplicate workspace binding")
		}
		if err := (Store{Remote: b.Remote, Workspace: b.Workspace}).validate(); err != nil {
			return r, err
		}
		for name, target := range b.MessageTargets {
			if err := validateMessageLabel(name); err != nil {
				return r, err
			}
			if err := target.validate(); err != nil {
				return r, err
			}
		}
		for name, target := range b.MediaTargets {
			if err := validateMessageLabel(name); err != nil {
				return r, err
			}
			if err := target.validate(); err != nil {
				return r, err
			}
		}
		refs[b.Reference] = true
	}
	roots := map[string]bool{}
	for _, a := range r.Attachments {
		if !filepath.IsAbs(a.Root) || !refs[a.Reference] {
			return r, invalid("invalid or dangling workspace attachment")
		}
		if roots[a.Root] {
			return r, &OperationError{Code: "workspace_ambiguous", Message: "duplicate workspace attachments require explicit repair"}
		}
		roots[a.Root] = true
	}
	return r, nil
}

func validWorkspaceReference(ref string) bool {
	if !strings.HasPrefix(ref, "ws-") || len(ref) != 35 {
		return false
	}
	_, err := hex.DecodeString(ref[3:])
	return err == nil
}

func canonicalDirectory(dir string) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err == nil && !info.IsDir() {
		err = invalid("workspace context must be a directory")
	}
	return resolved, err
}

func (r Registry) binding(ref string) (WorkspaceBinding, error) {
	for _, b := range r.Bindings {
		if b.Reference == ref {
			return b, nil
		}
	}
	return WorkspaceBinding{}, &OperationError{Code: "workspace_unbound", Message: "workspace reference is not registered; explicit attachment is required"}
}

// resolve never infers identity from a branch or a mutable machine default.
// Conflicting ancestor attachments are ambiguous, including nested repositories.
func (r Registry) resolve(dir, ref, handle string) (WorkspaceBinding, string, error) {
	dir, err := canonicalDirectory(dir)
	if err != nil {
		return WorkspaceBinding{}, "", err
	}
	var current WorkspaceBinding
	root := ""
	for _, a := range r.Attachments {
		rel, err := filepath.Rel(a.Root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if root != "" && current.Reference != a.Reference {
			return current, "", &OperationError{Code: "workspace_ambiguous", Message: "conflicting workspace attachments; resolve the attachment explicitly"}
		}
		current, err = r.binding(a.Reference)
		if err != nil {
			return current, "", err
		}
		if len(a.Root) > len(root) {
			root = a.Root
		}
	}
	if root == "" {
		return current, "", &OperationError{Code: "workspace_unbound", Message: "no workspace attachment for this directory; use workspace init or workspace attach"}
	}
	if ref == "" && handle == "" {
		return current, root, nil
	}
	var selected *WorkspaceBinding
	for _, b := range r.Bindings {
		if b.Engagement != current.Engagement || ref != "" && b.Reference != ref || ref == "" && b.Workspace != handle {
			continue
		}
		if selected != nil {
			return current, "", &OperationError{Code: "workspace_ambiguous", Message: "workspace handle is ambiguous; select a returned workspace_ref"}
		}
		copy := b
		selected = &copy
	}
	if selected == nil || handle != "" && selected.Workspace != handle {
		return current, "", &OperationError{Code: "workspace_unbound", Message: "workspace is not registered in the current engagement; no cross-engagement fallback"}
	}
	return *selected, root, nil
}

type BindingSetup struct {
	Root, Reference, Engagement, Workspace, Remote string
	Local                                          bool
	handoff                                        *WorkspaceHandoff
	dataDir                                        string
	restored                                       **BeadsRestoration
}

// SetupWorkspace persists explicit onboarding only. Attaching does not fetch,
// migrate, publish, or modify source/Beads state. Existing bindings are immutable.
func SetupWorkspace(ctx context.Context, filename string, setup BindingSetup) (WorkspaceContext, error) {
	root, err := canonicalDirectory(setup.Root)
	if err != nil {
		return WorkspaceContext{}, err
	}
	if outer, err := WorkspaceBeadsRoot(root, ""); err == nil && outer != root {
		return WorkspaceContext{}, invalid("attach the outer workspace Beads root, not a member repository")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return WorkspaceContext{}, err
	}
	lock := flock.New(filename + ".lock")
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil {
		return WorkspaceContext{}, err
	}
	if !locked {
		return WorkspaceContext{}, ctx.Err()
	}
	r, err := readRegistry(filename)
	if err != nil {
		return WorkspaceContext{}, err
	}
	// Idempotent setup is independent of any subsequently edited policy.
	for _, a := range r.Attachments {
		if a.Root != root {
			continue
		}
		if setup.handoff != nil {
			return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "context restore requires a fresh unattached root"}
		}
		b, err := r.binding(a.Reference)
		if err != nil {
			return WorkspaceContext{}, err
		}
		if setup.Reference != "" && setup.Reference != b.Reference || setup.Remote != "" && setup.Remote != b.Remote || setup.Workspace != "" && setup.Workspace != b.Workspace || setup.Engagement != "" && setup.Engagement != b.Engagement {
			return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "workspace is already bound; rebind/migration requires a separate explicit operation"}
		}
		return b.WorkspaceContext, nil
	}
	// Do not install a conflicting nested/ancestor attachment.
	for _, a := range r.Attachments {
		if withinDirectory(a.Root, root) || withinDirectory(root, a.Root) {
			if setup.Reference != a.Reference {
				return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "overlapping workspace attachment would make context ambiguous"}
			}
		}
	}
	var b WorkspaceBinding
	newBinding := setup.Reference == ""
	if setup.handoff != nil {
		if err := setup.handoff.validate(); err != nil {
			return WorkspaceContext{}, err
		}
		if setup.dataDir == "" || setup.Remote == "" || setup.Local {
			return WorkspaceContext{}, invalid("context restore requires explicit storage and persistent data directory")
		}
		remote := setup.Remote
		if !strings.Contains(remote, ":") {
			remote, err = filepath.Abs(remote)
			if err != nil {
				return WorkspaceContext{}, err
			}
		}
		newBinding = true
		for _, existing := range r.Bindings {
			if existing.Reference == setup.Reference {
				if existing.WorkspaceContext != setup.handoff.WorkspaceContext || existing.Remote != remote ||
					(existing.CollectionID != "" && existing.CollectionID != setup.handoff.CollectionID) {
					return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "handoff disagrees with registered identity, storage or collection"}
				}
				b, newBinding = existing, false
			} else if existing.Remote == remote && existing.Workspace == setup.Workspace {
				return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "handoff would alias an existing storage namespace"}
			}
		}
		if newBinding {
			b = WorkspaceBinding{WorkspaceContext: setup.handoff.WorkspaceContext, Remote: remote}
			b.CacheDir, err = bindingCache(b.WorkspaceContext)
			if err != nil {
				return WorkspaceContext{}, err
			}
		}
		b.CollectionID = setup.handoff.CollectionID
	} else if setup.Reference != "" {
		if setup.Remote != "" || setup.Engagement != "" || setup.Workspace != "" || setup.Local {
			return WorkspaceContext{}, invalid("attach by workspace_ref without another storage selector")
		}
		b, err = r.binding(setup.Reference)
		if err != nil {
			return WorkspaceContext{}, err
		}
	} else {
		if setup.Engagement == "" {
			return WorkspaceContext{}, invalid("setup requires an engagement policy or explicit engagement")
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return WorkspaceContext{}, err
		}
		b.WorkspaceContext = WorkspaceContext{Reference: "ws-" + hex.EncodeToString(id), Engagement: setup.Engagement, Workspace: setup.Workspace}
		b.Remote = setup.Remote
		if b.Remote != "" && !strings.Contains(b.Remote, ":") {
			b.Remote, err = filepath.Abs(b.Remote)
			if err != nil {
				return WorkspaceContext{}, err
			}
		}
		b.CacheDir, err = bindingCache(b.WorkspaceContext)
		if err != nil {
			return WorkspaceContext{}, err
		}
		if setup.Local {
			b.Remote = filepath.Join(filepath.Dir(filename), "stores", b.Reference+".git")
		}
		if err := (Store{Remote: b.Remote, Workspace: b.Workspace}).validate(); err != nil {
			return WorkspaceContext{}, err
		}
		for _, existing := range r.Bindings {
			if existing.Remote == b.Remote && existing.Workspace == b.Workspace {
				return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "storage namespace is already bound; attach its workspace_ref rather than creating another identity or engagement alias"}
			}
		}
	}
	// An already configured native collection remains authoritative. Inspect only;
	// neither attachment nor local initialization configures a Beads remote.
	if _, err := os.Stat(filepath.Join(root, ".beads", "redirect")); err == nil {
		info, err := (Store{Remote: b.Remote, Workspace: b.Workspace}).InspectBeads(ctx, root)
		if err != nil {
			return WorkspaceContext{}, err
		}
		if info.DeclaredURL != "" && info.DeclaredURL != info.DesiredURL || b.CollectionID != "" && b.CollectionID != info.CollectionID {
			return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "existing Beads collection binding disagrees with attachment"}
		}
		for _, remote := range info.Remotes {
			if remote.Name == "origin" && (remote.URL != info.DesiredURL || remote.Params["git_ref"] != info.DesiredRef) {
				return WorkspaceContext{}, &OperationError{Code: "conflict", Message: "existing native Beads origin/ref is authoritative"}
			}
		}
		b.CollectionID = info.CollectionID
	} else if !os.IsNotExist(err) {
		return WorkspaceContext{}, err
	}
	if setup.Local {
		if err := os.MkdirAll(filepath.Dir(b.Remote), 0700); err != nil {
			return WorkspaceContext{}, err
		}
		if err := os.Mkdir(b.Remote, 0700); err != nil {
			return WorkspaceContext{}, err
		}
		t := transaction{dir: b.Remote, env: gitEnvironment()}
		if _, err := t.git(ctx, nil, "init", "--bare", "."); err != nil {
			return WorkspaceContext{}, err
		}
		if _, err := (Store{Remote: b.Remote, Workspace: b.Workspace, CacheDir: b.CacheDir}).BootstrapStorage(ctx); err != nil {
			return WorkspaceContext{}, err
		}
	}
	if setup.handoff != nil {
		restored, err := (Store{Remote: b.Remote, Workspace: b.Workspace, CacheDir: b.CacheDir}).RestoreBeads(ctx, root, setup.dataDir, b.CollectionID, setup.handoff.DoltCommit)
		if setup.restored != nil {
			*setup.restored = restored
		}
		if err != nil {
			return WorkspaceContext{}, err
		}
	}
	if newBinding {
		r.Bindings = append(r.Bindings, b)
	} else {
		for i := range r.Bindings {
			if r.Bindings[i].Reference == b.Reference {
				r.Bindings[i] = b
			}
		}
	}
	r.Attachments = append(r.Attachments, workspaceAttachment{Root: root, Reference: b.Reference})
	if err := saveJSON(filename, r); err != nil {
		return WorkspaceContext{}, err
	}
	return b.WorkspaceContext, nil
}

func bindingCache(selected WorkspaceContext) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(selected.Engagement))
	return filepath.Join(cache, "nugs", "engagements", hex.EncodeToString(hash[:]), selected.Reference), nil
}

func withinDirectory(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func setupFromPolicy(ctx context.Context, registryPath, configPath, operation string, setup BindingSetup, config Config, repositories []string) (WorkspaceContext, error) {
	r, err := readRegistry(registryPath)
	if err != nil {
		return WorkspaceContext{}, err
	}
	root, err := canonicalDirectory(setup.Root)
	if err != nil {
		return WorkspaceContext{}, err
	}
	for _, a := range r.Attachments {
		if a.Root == root {
			return SetupWorkspace(ctx, registryPath, setup)
		}
	}
	if setup.Reference != "" {
		if operation != "workspace.attach" {
			return WorkspaceContext{}, invalid("use workspace attach for an existing workspace_ref")
		}
		return SetupWorkspace(ctx, registryPath, setup)
	}
	if setup.Workspace == "" {
		setup.Workspace, err = InferWorkspace(ctx, root, root, repositories)
		if err != nil {
			return WorkspaceContext{}, err
		}
	}
	if operation == "workspace.init" {
		if setup.Remote != "" || setup.Engagement != "" {
			return WorkspaceContext{}, invalid("workspace init inherits policy; use workspace attach for explicit storage setup")
		}
		if config.Remote == "" && config.Engagement == "" {
			setup.Local, setup.Engagement = true, "local"
		} else if config.Remote == "" || config.Engagement == "" {
			return WorkspaceContext{}, invalid("incomplete engagement policy; configure engagement and remote or explicitly attach")
		}
	}
	if setup.Engagement == "" {
		setup.Engagement = config.Engagement
	}
	if setup.Remote == "" && !setup.Local {
		setup.Remote = config.Remote
		if setup.Remote != "" && !strings.Contains(setup.Remote, ":") {
			setup.Remote = configRelative(configPath, setup.Remote)
		}
	}
	return SetupWorkspace(ctx, registryPath, setup)
}
