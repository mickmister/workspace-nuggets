package nuggets

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

var nativeCommitPattern = regexp.MustCompile(`^[0-9a-v]{32}$`)

// verifyNativeCheckpoint reads only an isolated clone. A receipt pins a commit
// reachable from the advertised history, not a promise that HEAD will stay put.
// Check identity at both the current tip and the requested historical snapshot.
func verifyNativeCheckpoint(ctx context.Context, clone, collectionID, commit string) (string, error) {
	var snapshot struct {
		Rows []struct {
			Commit       string `json:"commit_hash"`
			CollectionID string `json:"collection_id"`
		} `json:"rows"`
	}
	out, err := runNativeDolt(ctx, clone, "sql", "-r", "json", "-q", "SELECT DOLT_HASHOF('HEAD') AS commit_hash, (SELECT value FROM metadata WHERE `key`='_project_id') AS collection_id")
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(out, &snapshot); err != nil {
		return "", fmt.Errorf("decode native checkpoint: %w", err)
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].CollectionID != collectionID {
		return "", &OperationError{Code: "conflict", Message: "remote native collection identity differs; local state retained"}
	}
	head := snapshot.Rows[0].Commit
	if commit == "" || commit == head {
		return head, nil
	}
	if !nativeCommitPattern.MatchString(commit) {
		return "", &OperationError{Code: "conflict", Message: "invalid native checkpoint hash"}
	}
	var ancestry struct {
		Rows []struct {
			Count int `json:"count"`
		} `json:"rows"`
	}
	out, err = runNativeDolt(ctx, clone, "sql", "-r", "json", "-q", "SELECT COUNT(*) AS count FROM dolt_log WHERE commit_hash='"+commit+"'")
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(out, &ancestry); err != nil {
		return "", err
	}
	if len(ancestry.Rows) != 1 || ancestry.Rows[0].Count != 1 {
		return "", &OperationError{Code: "conflict", Message: "requested native checkpoint is not in published history"}
	}
	out, err = runNativeDolt(ctx, clone, "sql", "-r", "json", "-q", "SELECT '"+commit+"' AS commit_hash, value AS collection_id FROM metadata AS OF '"+commit+"' WHERE `key`='_project_id'")
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(out, &snapshot); err != nil {
		return "", err
	}
	if len(snapshot.Rows) != 1 || snapshot.Rows[0].CollectionID != collectionID {
		return "", &OperationError{Code: "conflict", Message: "historical native collection identity differs"}
	}
	return head, nil
}
