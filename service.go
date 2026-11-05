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
	Remote   string
	CacheDir string
}

type Request struct {
	Operation       string
	Workspace       string
	Name            string
	ExpectedVersion string
	Content         []byte
}

type DocumentView struct {
	Resource
	ContentBase64 string  `json:"content_base64"`
	Text          *string `json:"text,omitempty"`
}

type Response struct {
	Workspace string          `json:"workspace"`
	Ref       string          `json:"ref"`
	Resources []Resource      `json:"resources,omitempty"`
	Document  *DocumentView   `json:"document,omitempty"`
	Receipt   *Receipt        `json:"receipt,omitempty"`
	Error     *OperationError `json:"error,omitempty"`
}

func ResourceURI(workspace, name string) string {
	return "nugs://workspace/" + workspace + "/documents/" + url.PathEscape(name)
}

func (s Service) Execute(ctx context.Context, req Request) Response {
	store := Store{Remote: s.Remote, Workspace: req.Workspace, CacheDir: s.CacheDir}
	res := Response{Workspace: req.Workspace, Ref: store.Ref()}
	var err error
	switch req.Operation {
	case "workspace.inspect", "documents.list":
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
