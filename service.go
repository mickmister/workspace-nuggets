package nuggets

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Service is the common boundary used directly by CLI and MCP handlers.
type Service struct {
	Remote             string
	CacheDir           string
	DefaultWorkspace   string
	RegistryPath       string
	ContextDir         string
	WorkspaceReference string
}

type Request struct {
	Operation          string
	Workspace          string
	WorkspaceReference string
	Name               string
	ExpectedVersion    string
	Content            []byte
	Root               string
	Repositories       []string
	Purpose            string
	DataDir            string
	CollectionID       string
	DoltCommit         string
	RunID              string
	Snapshot           string
	Task               string
}

type DocumentView struct {
	Resource
	ContentBase64 string  `json:"content_base64"`
	Text          *string `json:"text,omitempty"`
}

type Response struct {
	Workspace          string                `json:"workspace"`
	WorkspaceReference string                `json:"workspace_ref,omitempty"`
	Engagement         string                `json:"engagement,omitempty"`
	Workspaces         []WorkspaceContext    `json:"workspaces,omitempty"`
	Ref                string                `json:"ref"`
	Resources          []Resource            `json:"resources,omitempty"`
	Document           *DocumentView         `json:"document,omitempty"`
	Receipt            *Receipt              `json:"receipt,omitempty"`
	Error              *OperationError       `json:"error,omitempty"`
	Manifest           *Manifest             `json:"manifest,omitempty"`
	ManifestVersion    string                `json:"manifest_version,omitempty"`
	Commit             string                `json:"commit,omitempty"`
	Beads              *BeadsInfo            `json:"beads,omitempty"`
	Storage            *StorageReceipt       `json:"storage,omitempty"`
	Publication        *BeadsPublication     `json:"publication,omitempty"`
	Restoration        *BeadsRestoration     `json:"restoration,omitempty"`
	CatchUp            *BeadsCatchUp         `json:"catch_up,omitempty"`
	Handoff            *WorkspaceHandoff     `json:"handoff,omitempty"`
	Synchronization    *BeadsSynchronization `json:"synchronization,omitempty"`
	Run                *WorkflowRun          `json:"run,omitempty"`
}

func ResourceURI(workspace, name string) string {
	return "nugs://workspace/" + workspace + "/documents/" + url.PathEscape(name)
}

func (s Service) Execute(ctx context.Context, req Request) Response {
	var binding *WorkspaceBinding
	var registry Registry
	if s.RegistryPath != "" {
		var err error
		registry, err = readRegistry(s.RegistryPath)
		if req.WorkspaceReference == "" && req.Workspace == "" {
			req.WorkspaceReference = s.WorkspaceReference
		}
		var b WorkspaceBinding
		var root string
		if err == nil {
			b, root, err = registry.resolve(s.ContextDir, req.WorkspaceReference, req.Workspace)
		}
		if err != nil {
			return failedResponse(err)
		}
		binding = &b
		s.Remote, s.CacheDir, req.Workspace = b.Remote, b.CacheDir, b.Workspace
		if strings.HasPrefix(req.Operation, "beads.") || req.Operation == "workspace.provision" {
			if req.Root == "" {
				req.Root = root
			}
			actual, _, err := registry.resolve(req.Root, "", "")
			if err != nil {
				return failedResponse(err)
			}
			if actual.Reference != b.Reference {
				return failedResponse(invalid("native/provision root does not match selected workspace attachment"))
			}
			if b.CollectionID != "" && strings.HasPrefix(req.Operation, "beads.") {
				if req.Operation == "beads.restore" {
					if req.CollectionID != b.CollectionID {
						return failedResponse(invalid("restore collection does not match persistent binding"))
					}
				} else {
					info, err := (Store{Remote: b.Remote, Workspace: b.Workspace}).InspectBeads(ctx, req.Root)
					if err != nil {
						return failedResponse(err)
					}
					if info.CollectionID != b.CollectionID {
						return failedResponse(invalid("native collection identity no longer matches attachment"))
					}
				}
			}
		}
	}
	if req.Workspace == "" {
		req.Workspace = s.DefaultWorkspace
	}
	store := Store{Remote: s.Remote, Workspace: req.Workspace, CacheDir: s.CacheDir}
	res := Response{Workspace: req.Workspace, Ref: store.Ref()}
	if binding != nil {
		res.WorkspaceReference, res.Engagement = binding.Reference, binding.Engagement
	}
	var err error
	switch req.Operation {
	case "workspace.context":
		if binding == nil {
			err = invalid("workspace context requires a persistent attachment")
		}
	case "workspace.list":
		if binding == nil {
			err = invalid("workspace discovery requires a persistent attachment")
		} else {
			res.Workspaces = []WorkspaceContext{}
			for _, b := range registry.Bindings {
				if b.Engagement == binding.Engagement {
					res.Workspaces = append(res.Workspaces, b.WorkspaceContext)
				}
			}
		}
	case "storage.bootstrap":
		res.Storage, err = store.BootstrapStorage(ctx)
		if res.Storage != nil {
			res.Ref = res.Storage.Ref
			res.Commit = res.Storage.Commit
		}
	case "beads.inspect":
		res.Ref = "refs/dolt/" + req.Workspace
		res.Beads, err = store.InspectBeads(ctx, req.Root)
	case "beads.publish", "beads.sync":
		res.Ref = "refs/dolt/" + req.Workspace
		if req.Operation == "beads.sync" {
			res.Synchronization, err = store.SyncBeads(ctx, req.Root)
			if res.Synchronization != nil {
				res.Publication = res.Synchronization.Publication
			}
		} else {
			res.Publication, err = store.PublishBeads(ctx, req.Root)
		}
		if err == nil && binding != nil && res.Publication != nil && res.Publication.Verified {
			res.Handoff = &WorkspaceHandoff{SchemaVersion: 1, WorkspaceContext: binding.WorkspaceContext,
				CollectionID: res.Publication.CollectionID, DoltCommit: res.Publication.DoltCommit}
		}
	case "beads.restore":
		res.Ref = "refs/dolt/" + req.Workspace
		res.Restoration, err = store.RestoreBeads(ctx, req.Root, req.DataDir, req.CollectionID, req.DoltCommit)
	case "beads.catch-up":
		res.Ref = "refs/dolt/" + req.Workspace
		res.CatchUp, err = store.CatchUpBeads(ctx, req.Root, req.DoltCommit)
	case "beads.configure":
		res.Ref = "refs/dolt/" + req.Workspace
		res.Beads, err = store.ConfigureBeads(ctx, req.Root)
	case "workspace.inspect":
		var snapshot WorkspaceSnapshot
		snapshot, err = store.Inspect(ctx)
		res.Resources, res.Manifest, res.ManifestVersion, res.Commit = snapshot.Resources, snapshot.Manifest, snapshot.ManifestVersion, snapshot.Commit
	case "workspace.provision":
		var receipt Receipt
		res.Manifest, receipt, err = store.ProvisionWithPurpose(ctx, req.Root, req.Repositories, req.Purpose, req.ExpectedVersion)
		if err == nil {
			res.Receipt, res.ManifestVersion, res.Commit = &receipt, receipt.Version, receipt.Commit
		}
	case "documents.list":
		res.Resources, err = store.List(ctx)
	case "runs.create":
		var receipt Receipt
		res.Run, receipt, err = store.CreateRun(ctx, req.RunID, req.Purpose, req.Task)
		if err == nil {
			res.Receipt = &receipt
		}
	case "runs.inspect":
		res.Run, res.Resources, err = store.InspectRun(ctx, req.RunID, req.Snapshot)
	case "runs.write":
		var receipt Receipt
		receipt, err = store.WriteRun(ctx, req.RunID, req.Name, req.ExpectedVersion, req.Content)
		if err == nil {
			res.Receipt = &receipt
		}
	case "documents.read", "runs.read":
		var doc Document
		if req.Operation == "runs.read" {
			doc, err = store.ReadRun(ctx, req.RunID, req.Name, req.Snapshot)
		} else {
			doc, err = store.Read(ctx, req.Name)
		}
		if err == nil {
			view := &DocumentView{Resource: doc.Resource, ContentBase64: base64.StdEncoding.EncodeToString(doc.Content)}
			if utf8.Valid(doc.Content) {
				text := string(doc.Content)
				view.Text = &text
			}
			res.Document = view
		}
	case "documents.upsert":
		var receipt Receipt
		receipt, err = store.Upsert(ctx, req.Name, req.ExpectedVersion, req.Content)
		if err == nil {
			res.Receipt = &receipt
		}
	default:
		err = invalid("unknown operation")
	}
	if binding != nil {
		for i := range res.Resources {
			res.Resources[i].URI = ResourceURI(binding.Reference, res.Resources[i].Name)
		}
		if res.Document != nil {
			res.Document.URI = ResourceURI(binding.Reference, res.Document.Name)
		}
		if res.Receipt != nil {
			res.Receipt.URI = ResourceURI(binding.Reference, res.Receipt.Name)
		}
		if res.Run != nil {
			res.Run.Resource.URI = ResourceURI(binding.Reference, res.Run.Resource.Name)
		}
	}
	if err != nil {
		res.Error = failedResponse(err).Error
	}
	return res
}

func failedResponse(err error) Response {
	res := Response{}
	if err != nil {
		var operationError *OperationError
		if errors.As(err, &operationError) {
			res.Error = operationError
		} else {
			code := "operation_failed"
			if errors.Is(err, context.Canceled) {
				code = "canceled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = "deadline_exceeded"
			}
			res.Error = &OperationError{Code: code, Message: err.Error()}
		}
	}
	return res
}
