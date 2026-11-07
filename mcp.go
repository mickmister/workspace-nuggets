package nuggets

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentArtifactInstructions = "Store intermediate artifacts (for example Beads form JSON, research captures and generated reports) as versioned workspace resources using documents upsert in the CLI or document_upsert in MCP. Do not commit intermediate artifacts to the source-code branch. Use logical resource names such as forms/workspace-composition.json, record the resource URI and version in the relevant bead, and use expected_version for updates. Maintained product code, documentation, schemas and reusable test fixtures remain source-controlled."

type workspaceInput struct {
	Workspace string `json:"workspace,omitempty" jsonschema:"Workspace slug; defaults to the server's branch workspace"`
}

type readInput struct {
	Workspace string `json:"workspace,omitempty"`
	Name      string `json:"name" jsonschema:"Relative document resource name"`
}

type beadsInput struct {
	Workspace string `json:"workspace,omitempty"`
	Root      string `json:"root,omitempty" jsonschema:"Workspace root with .beads/redirect; omitted discovers the outer workspace from server cwd"`
}

type provisionInput struct {
	Workspace       string   `json:"workspace,omitempty"`
	Root            string   `json:"root" jsonschema:"Explicit local workspace root on the server"`
	Repositories    []string `json:"repositories" jsonschema:"Explicit local repository paths relative to root; include every membership"`
	ExpectedVersion string   `json:"expected_version" jsonschema:"Observed manifest version; empty for create only"`
}

type restoreInput struct {
	Workspace    string `json:"workspace,omitempty"`
	Root         string `json:"root" jsonschema:"Explicit fresh workspace root; must have no .beads marker"`
	DataDir      string `json:"data_dir" jsonschema:"Absent persistent collection directory, with an existing parent"`
	CollectionID string `json:"collection_id" jsonschema:"Expected collection identity from the publication receipt"`
	DoltCommit   string `json:"dolt_commit" jsonschema:"Expected exact Dolt checkpoint from the publication receipt"`
}

type upsertInput struct {
	Workspace       string  `json:"workspace,omitempty"`
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
	server := mcp.NewServer(&mcp.Implementation{Name: "nugs", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: agentArtifactInstructions})
	for _, tool := range []struct{ name, operation, description string }{
		{"storage_bootstrap", "storage.bootstrap", "Explicitly bootstrap the selected data repository with an empty ordinary branch for native Dolt; preserves all existing branches and custom refs"},
		{"workspace_inspect", "workspace.inspect", "Inspect a workspace manifest with repository branch pointers and document resources"},
		{"documents_list", "documents.list", "Browse documents with versions and resource URIs"},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceInput) (*mcp.CallToolResult, Response, error) {
			return toolResponse(service.Execute(ctx, Request{Operation: tool.operation, Workspace: in.Workspace}))
		})
	}
	for _, tool := range []struct{ name, operation, description string }{
		{"beads_inspect", "beads.inspect", "Inspect workspace-owned Beads collection identity and native Dolt remote/ref configuration"},
		{"beads_configure", "beads.configure", "Configure the workspace Beads native origin and custom Git ref; does not initialize, pull or push task data"},
		{"beads_publish", "beads.publish", "Checkpoint workspace Beads, push without force, and verify an independent native recovery; quiesce workers before handoff; divergence stops publication"},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in beadsInput) (*mcp.CallToolResult, Response, error) {
			return toolResponse(service.Execute(ctx, Request{Operation: tool.operation, Workspace: in.Workspace, Root: in.Root}))
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "workspace_provision", Description: "Observe explicitly selected local repository branches and publish a version-checked workspace manifest; does not push source code"}, func(ctx context.Context, _ *mcp.CallToolRequest, in provisionInput) (*mcp.CallToolResult, Response, error) {
		return toolResponse(service.Execute(ctx, Request{Operation: "workspace.provision", Workspace: in.Workspace, Root: in.Root, Repositories: in.Repositories, ExpectedVersion: in.ExpectedVersion}))
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beads_restore", Description: "Restore an exact native checkpoint and collection identity into a fresh workspace; refuses existing markers and data directories"}, func(ctx context.Context, _ *mcp.CallToolRequest, in restoreInput) (*mcp.CallToolResult, Response, error) {
		return toolResponse(service.Execute(ctx, Request{Operation: "beads.restore", Workspace: in.Workspace, Root: in.Root, DataDir: in.DataDir, CollectionID: in.CollectionID, DoltCommit: in.DoltCommit}))
	})
	mcp.AddTool(server, &mcp.Tool{Name: "document_read", Description: "Read exact document content and its observed version"}, func(ctx context.Context, _ *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, Response, error) {
		return toolResponse(service.Execute(ctx, Request{Operation: "documents.read", Workspace: in.Workspace, Name: in.Name}))
	})
	mcp.AddTool(server, &mcp.Tool{Name: "document_upsert", Description: "Store documents and intermediate artifacts such as form JSON outside source refs; create or version-check updates, reconcile clean changes and publish"}, func(ctx context.Context, _ *mcp.CallToolRequest, in upsertInput) (*mcp.CallToolResult, Response, error) {
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
