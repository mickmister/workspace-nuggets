package nuggets

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"unicode/utf8"
)

// Service is the common boundary used directly by CLI and MCP handlers.
type Service struct {
	Remote           string
	CacheDir         string
	DefaultWorkspace string
}

type Request struct {
	Operation       string
	Workspace       string
	Name            string
	ExpectedVersion string
	Content         []byte
	Root            string
	Repositories    []string
}

type DocumentView struct {
	Resource
	ContentBase64 string  `json:"content_base64"`
	Text          *string `json:"text,omitempty"`
}

type Response struct {
	Workspace       string            `json:"workspace"`
	Ref             string            `json:"ref"`
	Resources       []Resource        `json:"resources,omitempty"`
	Document        *DocumentView     `json:"document,omitempty"`
	Receipt         *Receipt          `json:"receipt,omitempty"`
	Error           *OperationError   `json:"error,omitempty"`
	Manifest        *Manifest         `json:"manifest,omitempty"`
	ManifestVersion string            `json:"manifest_version,omitempty"`
	Commit          string            `json:"commit,omitempty"`
	Beads           *BeadsInfo        `json:"beads,omitempty"`
	Storage         *StorageReceipt   `json:"storage,omitempty"`
	Publication     *BeadsPublication `json:"publication,omitempty"`
}

func ResourceURI(workspace, name string) string {
	return "nugs://workspace/" + workspace + "/documents/" + url.PathEscape(name)
}

func (s Service) Execute(ctx context.Context, req Request) Response {
	if req.Workspace == "" {
		req.Workspace = s.DefaultWorkspace
	}
	store := Store{Remote: s.Remote, Workspace: req.Workspace, CacheDir: s.CacheDir}
	res := Response{Workspace: req.Workspace, Ref: store.Ref()}
	var err error
	switch req.Operation {
	case "storage.bootstrap":
		res.Storage, err = store.BootstrapStorage(ctx)
		if res.Storage != nil {
			res.Ref = res.Storage.Ref
			res.Commit = res.Storage.Commit
		}
	case "beads.inspect":
		res.Ref = "refs/dolt/" + req.Workspace
		res.Beads, err = store.InspectBeads(ctx, req.Root)
	case "beads.publish":
		res.Ref = "refs/dolt/" + req.Workspace
		res.Publication, err = store.PublishBeads(ctx, req.Root)
	case "beads.configure":
		res.Ref = "refs/dolt/" + req.Workspace
		res.Beads, err = store.ConfigureBeads(ctx, req.Root)
	case "workspace.inspect":
		var snapshot WorkspaceSnapshot
		snapshot, err = store.Inspect(ctx)
		res.Resources, res.Manifest, res.ManifestVersion, res.Commit = snapshot.Resources, snapshot.Manifest, snapshot.ManifestVersion, snapshot.Commit
	case "workspace.provision":
		var receipt Receipt
		res.Manifest, receipt, err = store.Provision(ctx, req.Root, req.Repositories, req.ExpectedVersion)
		if err == nil {
			res.Receipt, res.ManifestVersion, res.Commit = &receipt, receipt.Version, receipt.Commit
		}
	case "documents.list":
		res.Resources, err = store.List(ctx)
	case "documents.read":
		var doc Document
		doc, err = store.Read(ctx, req.Name)
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
