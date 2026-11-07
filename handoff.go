package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
)

// WorkspaceHandoff is an endpoint-free capability description supplied by a
// trusted coordinator, never by repository content or an agent response. It is
// emitted only after a bound native publication has been independently verified.
// Authentication/authorization of the dispatch remains the coordinator's job.
type WorkspaceHandoff struct {
	SchemaVersion int `json:"schema_version"`
	WorkspaceContext
	CollectionID string `json:"collection_id"`
	DoltCommit   string `json:"dolt_commit"`
}

func ParseWorkspaceHandoff(content []byte) (*WorkspaceHandoff, error) {
	if len(content) > 8192 {
		return nil, invalid("workspace handoff exceeds the setup input limit")
	}
	d := json.NewDecoder(bytes.NewReader(content))
	d.DisallowUnknownFields()
	var h WorkspaceHandoff
	if err := d.Decode(&h); err != nil {
		return nil, invalid("invalid workspace handoff object")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, invalid("workspace handoff must contain exactly one object")
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	return &h, nil
}

func (h WorkspaceHandoff) validate() error {
	if h.SchemaVersion != 1 || !validWorkspaceReference(h.Reference) || h.Engagement == "" || len(h.Engagement) > 200 ||
		!regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`).MatchString(h.Workspace) ||
		!regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`).MatchString(h.CollectionID) ||
		!regexp.MustCompile(`^[0-9a-v]{32}$`).MatchString(h.DoltCommit) {
		return invalid("workspace handoff requires schema, stable identity, engagement, namespace and exact native pins")
	}
	return nil
}

// RestoreWorkspaceContext is a trusted CLI setup operation. Storage is supplied
// separately from the handoff; ordinary CLI/MCP operations never accept it as an
// authorization shortcut. Failed native verification never publishes a binding.
func RestoreWorkspaceContext(ctx context.Context, registry, root, dataDir, remote string, h WorkspaceHandoff) Response {
	if err := h.validate(); err != nil {
		return failedResponse(err)
	}
	var restored *BeadsRestoration
	selected, err := SetupWorkspace(ctx, registry, BindingSetup{
		Root: root, Reference: h.Reference, Engagement: h.Engagement,
		Workspace: h.Workspace, Remote: remote, handoff: &h,
		dataDir: dataDir, restored: &restored,
	})
	if err != nil {
		res := failedResponse(err)
		// A verified native clone may be retained if installation/registry save
		// fails. Surface its recovery receipt without claiming context attached.
		res.Restoration = restored
		return res
	}
	return Response{Workspace: selected.Workspace, WorkspaceReference: selected.Reference,
		Engagement: selected.Engagement, Ref: "refs/dolt/" + selected.Workspace,
		Restoration: restored, Handoff: &h}
}
