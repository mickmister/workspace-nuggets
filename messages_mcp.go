package nuggets

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type messageInput struct {
	OtherTarget        string        `json:"other_target,omitempty"`
	WorkspaceReference string        `json:"workspace_ref,omitempty"`
	Target             string        `json:"target,omitempty" jsonschema:"Explicit bound target; omitted only when one target is bound"`
	Database           string        `json:"database,omitempty"`
	NativeVersion      string        `json:"native_version,omitempty"`
	VKDatabase         string        `json:"vk_database,omitempty"`
	VKWorkspace        string        `json:"vk_workspace,omitempty"`
	Session            string        `json:"session,omitempty"`
	Role               string        `json:"role,omitempty"`
	Reader             string        `json:"reader,omitempty" jsonschema:"Stable logical reader name; never a native fork ID or CI run ID"`
	Token              string        `json:"token,omitempty" jsonschema:"Opaque issued page token; only explicit ACK advances acknowledged position"`
	Limit              *int          `json:"limit,omitempty"`
	Members            []string      `json:"members,omitempty"`
	Aliases            []string      `json:"aliases,omitempty"`
	Name               string        `json:"name,omitempty"`
	Snapshot           string        `json:"snapshot,omitempty"`
	Batch              *MessageBatch `json:"batch,omitempty"`
}

func addMessageTools(server *mcp.Server, service Service) {
	for _, tool := range []struct{ name, operation, description string }{
		{"messages_compare", "messages.compare", "Verify and compare two explicitly bound targets by canonical source/message/coverage/lineage digest; never copy or select a winner"},
		{"messages_bind", "messages.bind", "Explicitly bind a configured message target to this workspace; existing bindings never retarget"},
		{"messages_scan", "messages.scan", "Read a version-gated native OpenCode SQLite snapshot for exact workspace/member directories; no publication"},
		{"messages_sync", "messages.sync", "Capture authored native messages and publish sealed chunks/catalog to one selected target; no automatic mirrors"},
		{"messages_import", "messages.import", "Publish a normalized producer-neutral authored message batch; changed sessions require base_revision"},
		{"messages_sessions", "messages.sessions", "List captured session descriptors, coverage and confirmed/inferred/unresolved lineage"},
		{"messages_read", "messages.read", "Read latest or pinned authored messages with exact text/attachment descriptors and source identities"},
		{"messages_verify", "messages.verify", "Verify all selected sealed chunk/message digests and return canonical semantic digest"},
		{"messages_prepare", "messages.prepare", "Prepare an opaque replayable pinned page; neither preparation nor fetching acknowledges it"},
		{"messages_read_page", "messages.read-page", "Fetch and verify an issued page; replay is allowed, explicit ACK still required"},
		{"messages_catch_up", "messages.catch-up", "Prepare and read one bounded catch-up page for a stable named reader; does not ACK"},
		{"messages_ack", "messages.ack", "Acknowledge only a successfully delivered issued page with generation CAS"},
		{"messages_reader", "messages.reader", "Inspect a named reader's acknowledged position, generation and pending pages"},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description}, func(ctx context.Context, _ *mcp.CallToolRequest, in messageInput) (*mcp.CallToolResult, Response, error) {
			if in.Database == "" {
				in.Database = defaultOpenCodeDatabase()
			}
			limit := 100
			if in.Limit != nil {
				limit = *in.Limit
			}
			req := Request{Operation: tool.operation, WorkspaceReference: in.WorkspaceReference, Target: in.Target, Database: in.Database, NativeVersion: in.NativeVersion,
				OtherTarget: in.OtherTarget,
				VKDatabase:  in.VKDatabase, VKWorkspace: in.VKWorkspace,
				Session: in.Session, Role: in.Role, Reader: in.Reader, Token: in.Token, Limit: limit, Repositories: in.Members, Aliases: in.Aliases, Name: in.Name, Snapshot: in.Snapshot}
			if in.Batch != nil {
				req.Content = encodeMessageJSON(in.Batch)
			}
			return toolResponse(service.Execute(ctx, req))
		})
	}
}
