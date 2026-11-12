package nuggets

import (
	"context"
	"encoding/base64"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mediaInput struct {
	WorkspaceReference string `json:"workspace_ref,omitempty"`
	Target             string `json:"target,omitempty"`
	Name               string `json:"name,omitempty"`
	Filename           string `json:"filename,omitempty"`
	Category           string `json:"category,omitempty"`
	Source             string `json:"source,omitempty"`
	Expected           string `json:"expected_version,omitempty"`
	Snapshot           string `json:"snapshot,omitempty"`
	Token              string `json:"token,omitempty"`
	Limit              *int   `json:"limit,omitempty"`
	ContentBase64      string `json:"content_base64,omitempty"`
}

func addMediaTools(server *mcp.Server, service Service) {
	for _, tool := range []struct{ name, operation, description string }{
		{"media_bind", "media.bind", "Bind configured S3 target to this workspace's media kind; no implicit migration"},
		{"media_import", "media.import", "Import exact media bytes with metadata and expected revision; screenshots use category screenshot; maximum 64 MiB"},
		{"media_list", "media.list", "Page a pinned workspace media catalog; binary payloads are not embedded in catalog records"},
		{"media_read", "media.read", "Read and verify exact binary media as base64 from current or pinned catalog"},
		{"media_verify", "media.verify", "Verify media content digest/size without returning binary bytes"},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in mediaInput) (*mcp.CallToolResult, Response, error) {
			limit := 100
			if in.Limit != nil {
				limit = *in.Limit
			}
			req := Request{Operation: tool.operation, WorkspaceReference: in.WorkspaceReference, Target: in.Target, Name: in.Name, Purpose: in.Filename, Category: in.Category, Source: in.Source, ExpectedVersion: in.Expected, Snapshot: in.Snapshot, Token: in.Token, Limit: limit}
			if tool.operation == "media.import" {
				if len(in.ContentBase64) > base64.StdEncoding.EncodedLen(mediaSizeLimit) {
					return toolResponse(failedResponse(invalid("media exceeds 64 MiB")))
				}
				var err error
				req.Content, err = base64.StdEncoding.DecodeString(in.ContentBase64)
				if err != nil {
					return toolResponse(failedResponse(invalid("invalid media base64")))
				}
			}
			return toolResponse(service.Execute(ctx, req))
		})
	}
}
