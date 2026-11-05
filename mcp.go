package nuggets

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type workspaceInput struct {
	Workspace string `json:"workspace" jsonschema:"Stable workspace identifier"`
}

type readInput struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name" jsonschema:"Relative document resource name"`
}

type provisionInput struct {
	Workspace       string   `json:"workspace"`
	Root            string   `json:"root" jsonschema:"Explicit local workspace root on the server"`
	Repositories    []string `json:"repositories" jsonschema:"Explicit local repository paths relative to root; include every membership"`
	ExpectedVersion string   `json:"expected_version" jsonschema:"Observed manifest version; empty for create only"`
}

type upsertInput struct {
	Workspace       string  `json:"workspace"`
	Name            string  `json:"name"`
	ExpectedVersion string  `json:"expected_version" jsonschema:"Observed blob version; empty string means create only"`
	Text            *string `json:"text,omitempty" jsonschema:"UTF-8 content; supply exactly one of text or content_base64"`
	ContentBase64   *string `json:"content_base64,omitempty" jsonschema:"Base64 for exact binary content"`
}

func toolResponse(response Response) (*mcp.CallToolResult, Response, error) {
	return &mcp.CallToolResult{IsError: response.Error != nil}, response, nil
}

// NewMCPServer calls Service directly; no tool launches the nugs executable.
func NewMCPServer(service Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "nugs", Version: "0.1.0"}, nil)
	for _, tool := range []struct{ name, operation, description string }{
		{"workspace_inspect", "workspace.inspect", "Inspect a workspace manifest with repository branch pointers and document resources"},
		{"documents_list", "documents.list", "Browse documents with versions and resource URIs"},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceInput) (*mcp.CallToolResult, Response, error) {
			return toolResponse(service.Execute(ctx, Request{Operation: tool.operation, Workspace: in.Workspace}))
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_provision", Description: "Observe explicitly selected local repository branches and publish a version-checked workspace manifest; does not push source code"}, func(ctx context.Context, _ *mcp.CallToolRequest, in provisionInput) (*mcp.CallToolResult, Response, error) {
		return toolResponse(service.Execute(ctx, Request{Operation: "workspace.provision", Workspace: in.Workspace, Root: in.Root, Repositories: in.Repositories, ExpectedVersion: in.ExpectedVersion}))
	})
	mcp.AddTool(server, &mcp.Tool{Name: "document_read", Description: "Read exact document content and its observed version"}, func(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, Response, error) {
		return toolResponse(service.Execute(ctx, Request{Operation: "documents.read", Workspace: in.Workspace, Name: in.Name}))
	})
	mcp.AddTool(server, &mcp.Tool{Name: "document_upsert", Description: "Create or version-check an update, reconcile clean changes and publish; real conflicts are returned"}, func(ctx context.Context, _ *mcp.CallToolRequest, in upsertInput) (*mcp.CallToolResult, Response, error) {
		var content []byte
		var err error
		switch {
		case (in.Text == nil) == (in.ContentBase64 == nil):
			err = invalid("supply exactly one of text or content_base64")
		case in.Text != nil:
			content = []byte(*in.Text)
		default:
			content, err = base64.StdEncoding.DecodeString(*in.ContentBase64)
			if err != nil {
				err = invalid("invalid base64 content")
			}
		}
		if err != nil {
			return toolResponse(Response{Workspace: in.Workspace, Error: err.(*OperationError)})
		}
		return toolResponse(service.Execute(ctx, Request{Operation: "documents.upsert", Workspace: in.Workspace, Name: in.Name, ExpectedVersion: in.ExpectedVersion, Content: content}))
	})
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "nugs://workspace/{workspace}/documents/{name}", Name: "Workspace document", Description: "Read a document; obtain names and versions using documents_list"}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		u, err := url.Parse(req.Params.URI)
		if err != nil || u.Scheme != "nugs" || u.Host != "workspace" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid document resource URI")
		}
		parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 3)
		if len(parts) != 3 || parts[1] != "documents" {
			return nil, fmt.Errorf("invalid document resource path")
		}
		name, err := url.PathUnescape(parts[2])
		if err != nil {
			return nil, err
		}
		doc, err := (Store{Remote: service.Remote, CacheDir: service.CacheDir, Workspace: parts[0]}).Read(ctx, name)
		if err != nil {
			return nil, err
		}
		// A non-nil empty blob must still serialize as an explicit empty payload.
		blob := append([]byte{}, doc.Content...)
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: doc.MediaType, Blob: blob}}}, nil
	})
	return server
}
