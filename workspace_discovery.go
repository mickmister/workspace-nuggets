package nuggets

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// WorkspaceStore is operator configuration, not an agent-supplied transport.
type WorkspaceStore struct {
	Remote     string `json:"remote"`
	Engagement string `json:"engagement"`
}

type DiscoveredWorkspace struct {
	Target          string          `json:"target"`
	Workspace       string          `json:"workspace"`
	Engagement      string          `json:"engagement"`
	Commit          string          `json:"commit"`
	Manifest        *Manifest       `json:"manifest,omitempty"`
	ManifestVersion string          `json:"manifest_version,omitempty"`
	Error           *OperationError `json:"error,omitempty"`
}

type WorkspaceDiscovery struct {
	Workspaces []DiscoveredWorkspace `json:"workspaces"`
	NextToken  string                `json:"next_token,omitempty"`
}

type workspaceDiscoveryCursor struct {
	Scope string `json:"scope"`
	After string `json:"after"`
}

func configuredWorkspaceStores(config Config, filename string) map[string]WorkspaceStore {
	stores := map[string]WorkspaceStore{}
	for name, store := range config.WorkspaceStores {
		if store.Remote != "" && !strings.Contains(store.Remote, ":") {
			store.Remote = configRelative(filename, store.Remote)
		}
		stores[name] = store
	}
	// Existing explicit onboarding policy is also an authorized discovery store.
	if config.Remote != "" && config.Engagement != "" {
		if _, exists := stores["default"]; !exists {
			remote := config.Remote
			if !strings.Contains(remote, ":") {
				remote = configRelative(filename, remote)
			}
			stores["default"] = WorkspaceStore{Remote: remote, Engagement: config.Engagement}
		}
	}
	return stores
}

func (s Service) executeWorkspaceDiscovery(ctx context.Context, req Request) Response {
	if req.WorkspaceReference != "" {
		return failedResponse(invalid("remote workspace operations use target and workspace, not a machine workspace_ref"))
	}
	names := []string{}
	for name, store := range s.WorkspaceStores {
		if req.Target != "" && name != req.Target {
			continue
		}
		if err := validateMessageLabel(name); err != nil {
			return failedResponse(err)
		}
		if strings.TrimSpace(store.Engagement) == "" {
			return failedResponse(invalid("workspace store requires an engagement"))
		}
		if err := (Store{Remote: store.Remote, Workspace: "discovery"}).validate(); err != nil {
			return failedResponse(err)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return failedResponse(invalid("no matching configured workspace store"))
	}
	if req.Operation != "workspace.discover" {
		if len(names) != 1 {
			return failedResponse(invalid("select a configured workspace store with target"))
		}
		name := names[0]
		configured := s.WorkspaceStores[name]
		store := Store{Remote: configured.Remote, Workspace: req.Workspace}
		if err := store.validate(); err != nil {
			return failedResponse(err)
		}
		if req.Operation == "workspace.create" {
			// Creation publishes only an authored workspace purpose/membership
			// manifest. It does not create a repository or initialize Beads.
			manifest, receipt, err := store.ProvisionWithPurpose(ctx, req.Root, req.Repositories, req.Purpose, "")
			if err != nil {
				return failedResponse(err)
			}
			return Response{Workspace: req.Workspace, Engagement: configured.Engagement, Ref: store.Ref(), Manifest: manifest, ManifestVersion: receipt.Version, Commit: receipt.Commit, Receipt: &receipt}
		}
		if s.RegistryPath == "" {
			return failedResponse(invalid("workspace select requires a registry"))
		}
		// Verify existence before persisting a local attachment. Reuse a native
		// registered identity where present; never alias it in another engagement.
		doc, err := store.Read(ctx, ManifestDocument)
		if err != nil {
			return failedResponse(err)
		}
		manifest, err := parseManifest(doc.Content, req.Workspace)
		if err != nil {
			return failedResponse(err)
		}
		registry, err := readRegistry(s.RegistryPath)
		if err != nil {
			return failedResponse(err)
		}
		setup := BindingSetup{Root: req.Root, Remote: configured.Remote, Engagement: configured.Engagement, Workspace: req.Workspace}
		for _, b := range registry.Bindings {
			if b.Remote == configured.Remote && b.Workspace == req.Workspace {
				if b.Engagement != configured.Engagement {
					return failedResponse(invalid("configured engagement disagrees with registered workspace"))
				}
				setup = BindingSetup{Root: req.Root, Reference: b.Reference}
			}
		}
		selected, err := SetupWorkspace(ctx, s.RegistryPath, setup)
		if err != nil {
			return failedResponse(err)
		}
		return Response{Workspace: selected.Workspace, WorkspaceReference: selected.Reference, Engagement: selected.Engagement, Ref: store.Ref(), Manifest: manifest, ManifestVersion: doc.Version, Commit: doc.Commit}
	}
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return failedResponse(invalid("workspace discovery limit must be 1–100"))
	}
	// Bind continuation to configuration and filter. It deliberately observes
	// current remote refs on each page, rather than promising a global snapshot.
	scopeBytes, _ := json.Marshal(struct {
		Stores         map[string]WorkspaceStore
		Target, Prefix string
	}{s.WorkspaceStores, req.Target, req.Prefix})
	hash := sha256.Sum256(scopeBytes)
	scope := hex.EncodeToString(hash[:])
	after := ""
	if req.Token != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.Token)
		var cursor workspaceDiscoveryCursor
		if err != nil || len(data) > 4096 || json.Unmarshal(data, &cursor) != nil || cursor.Scope != scope || cursor.After == "" {
			return failedResponse(invalid("invalid workspace discovery token or changed configuration/filter"))
		}
		after = cursor.After
	}
	candidates := []DiscoveredWorkspace{}
	for _, name := range names {
		configured := s.WorkspaceStores[name]
		store := Store{Remote: configured.Remote, Workspace: "discovery"}
		t, err := store.begin(ctx)
		if err != nil {
			return failedResponse(err)
		}
		out, err := t.git(ctx, nil, "ls-remote", "--refs", t.remote, "refs/documents/*")
		os.RemoveAll(t.dir)
		if err != nil {
			return failedResponse(err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/documents/") {
				continue
			}
			workspace := strings.TrimPrefix(fields[1], "refs/documents/")
			if (Store{Remote: configured.Remote, Workspace: workspace}).validate() != nil || !strings.HasPrefix(workspace, req.Prefix) || name+"/"+workspace <= after {
				continue
			}
			candidates = append(candidates, DiscoveredWorkspace{Target: name, Workspace: workspace, Engagement: configured.Engagement, Commit: fields[0]})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Target+"/"+candidates[i].Workspace < candidates[j].Target+"/"+candidates[j].Workspace
	})
	page := &WorkspaceDiscovery{Workspaces: []DiscoveredWorkspace{}}
	if len(candidates) > limit {
		last := candidates[limit-1]
		data, _ := json.Marshal(workspaceDiscoveryCursor{Scope: scope, After: last.Target + "/" + last.Workspace})
		page.NextToken = base64.RawURLEncoding.EncodeToString(data)
		candidates = candidates[:limit]
	}
	for _, candidate := range candidates {
		store := Store{Remote: s.WorkspaceStores[candidate.Target].Remote, Workspace: candidate.Workspace}
		doc, err := store.ReadSnapshot(ctx, ManifestDocument, candidate.Commit)
		if err == nil {
			candidate.Manifest, err = parseManifest(doc.Content, candidate.Workspace)
			candidate.ManifestVersion = doc.Version
		}
		candidate.Error = failedResponse(err).Error
		page.Workspaces = append(page.Workspaces, candidate)
	}
	return Response{Discovery: page}
}
