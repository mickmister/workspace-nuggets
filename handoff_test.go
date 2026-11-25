package nuggets

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceHandoffValidation(t *testing.T) {
	h := WorkspaceHandoff{SchemaVersion: 1,
		WorkspaceContext: WorkspaceContext{Reference: "ws-0123456789abcdef0123456789abcdef", Engagement: "fixture", Workspace: "existing-namespace"},
		CollectionID:     "native-collection", DoltCommit: strings.Repeat("a", 32)}
	body, _ := json.Marshal(h)
	if got, err := ParseWorkspaceHandoff(body); err != nil || *got != h {
		t.Fatalf("valid handoff: %+v, %v", got, err)
	}
	for _, content := range [][]byte{
		append(body, []byte("{}")...), []byte("null"),
		[]byte(strings.Replace(string(body), `"schema_version":1`, `"schema_version":2`, 1)),
		[]byte(strings.Replace(string(body), `"workspace_ref":"ws-0123456789abcdef0123456789abcdef"`, `"workspace_ref":"branch-slug"`, 1)),
		[]byte(strings.Replace(string(body), `"engagement":"fixture"`, `"engagement":""`, 1)),
		[]byte(strings.Replace(string(body), `"dolt_commit":"`+strings.Repeat("a", 32)+`"`, `"dolt_commit":"latest"`, 1)),
		[]byte(strings.Replace(string(body), `"schema_version":1`, `"remote":"https://not-a-setup-selector.invalid","schema_version":1`, 1)),
		[]byte(strings.Repeat(" ", 8193)),
	} {
		if _, err := ParseWorkspaceHandoff(content); err == nil {
			t.Fatalf("invalid/unpinned handoff accepted: %s", content)
		}
	}
}
