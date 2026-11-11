package nuggets

import (
	"context"
	"database/sql"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"strings"
)

type MessageVKAssociation struct {
	ProcessID        string `json:"process_id"`
	NativeMessageID  string `json:"native_message_id,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	ResumeEligible   bool   `json:"resume_eligible"`
	Evidence         string `json:"evidence"`
}

// VerifyVKMessageSessions checks the native ownership join, never wrapper text or
// seen flags. Dropped/reset executions remain explicitly historical evidence.
func VerifyVKMessageSessions(ctx context.Context, database, workspaceID string, batch *MessageBatch) error {
	id, err := hex.DecodeString(strings.ReplaceAll(workspaceID, "-", ""))
	if err != nil || len(id) != 16 {
		return invalid("VK verification requires the explicit VK workspace UUID")
	}
	abs, err := filepath.Abs(database)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT cat.agent_session_id, cat.agent_message_id, ep.id, s.agent_working_dir,
 ep.dropped, ep.started_at, reset.started_at
 FROM sessions s JOIN execution_processes ep ON ep.session_id=s.id
 JOIN coding_agent_turns cat ON cat.execution_process_id=ep.id
 LEFT JOIN execution_processes reset ON reset.id=s.context_reset_execution_process_id
 WHERE s.workspace_id=? AND lower(s.executor)='opencode' ORDER BY ep.started_at,ep.id`, id)
	if err != nil {
		return &OperationError{Code: "unsupported_source", Message: "VK SQLite schema does not satisfy workspace/process/turn association adapter"}
	}
	defer rows.Close()
	associations := map[string][]MessageVKAssociation{}
	for rows.Next() {
		var sessionID, messageID, directory, started, reset sql.NullString
		var process []byte
		var dropped bool
		if err := rows.Scan(&sessionID, &messageID, &process, &directory, &dropped, &started, &reset); err != nil {
			return err
		}
		if !sessionID.Valid || sessionID.String == "" {
			continue
		}
		eligible := !dropped && (!reset.Valid || started.String >= reset.String)
		associations["opencode:"+sessionID.String] = append(associations["opencode:"+sessionID.String], MessageVKAssociation{
			ProcessID: hex.EncodeToString(process), NativeMessageID: messageID.String, WorkingDirectory: directory.String, ResumeEligible: eligible,
			Evidence: "vk-workspace-process-turn/v1; explicit workspace UUID, executor, native session ID; dropped/reset eligibility labeled separately",
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range batch.Sessions {
		batch.Sessions[i].VKAssociations = associations[batch.Sessions[i].ID]
		batch.Sessions[i].VKCoverage = "verified_mapping"
		if len(batch.Sessions[i].VKAssociations) == 0 {
			batch.Sessions[i].VKCoverage = "no_mapping_observed;native_directory_evidence_only"
		}
	}
	return nil
}
