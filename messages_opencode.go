package nuggets

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type OpenCodeMessageSource struct {
	Database string
	Version  string
	Root     string
	Members  []string
	Aliases  []string
	Session  string
	Limit    int
}

// ScanOpenCodeMessages takes a read-only WAL-aware transaction over the native
// database. No copy/export/import, live-file mutation or newest-session picker.
// Exact root/member/explicit historical aliases establish ownership; '/' never
// becomes an implicit ownership wildcard and unknown versions fail closed.
func ScanOpenCodeMessages(ctx context.Context, reference string, source OpenCodeMessageSource) (MessageBatch, error) {
	batch := MessageBatch{Schema: 1, WorkspaceReference: reference, Sessions: []MessageSession{}}
	if source.Version == "" {
		versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(versionCtx, "opencode", "--version").Output()
		if err != nil {
			return batch, err
		}
		source.Version = strings.TrimSpace(string(out))
	}
	if source.Version != "1.19.4" {
		return batch, &OperationError{Code: "unsupported_source", Message: "OpenCode native adapter currently validates version 1.19.4 only"}
	}
	root, err := canonicalDirectory(source.Root)
	if err != nil {
		return batch, err
	}
	directories := map[string]bool{root: true}
	for _, member := range source.Members {
		if !filepath.IsAbs(member) {
			member = filepath.Join(root, member)
		}
		dir, err := canonicalDirectory(member)
		if err != nil {
			return batch, err
		}
		if !withinDirectory(root, dir) {
			return batch, invalid("native scan member must belong to workspace root")
		}
		directories[dir] = true
	}
	for _, alias := range source.Aliases {
		if !filepath.IsAbs(alias) || filepath.Clean(alias) != alias || alias == "/" {
			return batch, invalid("historical directory aliases must be explicit absolute non-root paths")
		}
		directories[alias] = true
	}
	if directories["/"] {
		return batch, invalid("filesystem root cannot establish native session ownership")
	}
	if source.Limit < 0 {
		return batch, invalid("session limit cannot be negative")
	}
	abs, err := filepath.Abs(source.Database)
	if err != nil {
		return batch, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return batch, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return batch, err
	}
	defer tx.Rollback()
	for table, required := range map[string][]string{
		"session": {"id", "parent_id", "directory", "version", "time_updated"},
		"message": {"id", "session_id", "data", "time_created", "time_updated"},
		"part":    {"id", "message_id", "session_id", "data", "time_created", "time_updated"},
	} {
		rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return batch, err
		}
		columns := map[string]bool{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
				rows.Close()
				return batch, err
			}
			columns[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return batch, err
		}
		for _, name := range required {
			if !columns[name] {
				return batch, &OperationError{Code: "unsupported_source", Message: "OpenCode SQLite schema differs from validated adapter"}
			}
		}
	}
	// Restrict in SQL before reading any message/part bodies from other workspaces.
	dirs := make([]string, 0, len(directories))
	for dir := range directories {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	args := []any{}
	placeholders := []string{}
	for _, dir := range dirs {
		args = append(args, dir)
		placeholders = append(placeholders, "?")
	}
	query := "SELECT id, parent_id, directory, version, time_updated FROM session WHERE directory IN (" + strings.Join(placeholders, ",") + ")"
	if source.Session != "" {
		ids := strings.Split(source.Session, ",")
		marks := make([]string, len(ids))
		for i, id := range ids {
			marks[i] = "?"
			args = append(args, strings.TrimPrefix(id, "opencode:"))
		}
		query += " AND id IN (" + strings.Join(marks, ",") + ")"
	}
	query += " ORDER BY id"
	if source.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, source.Limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return batch, err
	}
	for rows.Next() {
		var id string
		var parent sql.NullString
		session := MessageSession{Producer: "opencode", Coverage: "native_snapshot_authored_text_only", Messages: []AuthoredMessage{}}
		if err := rows.Scan(&id, &parent, &session.Directory, &session.Version, &session.Updated); err != nil {
			rows.Close()
			return batch, err
		}
		session.ID = "opencode:" + id
		session.Lineage = MessageLineage{Status: "unresolved", Evidence: "native parent_id absent; copied text or timestamp proximity alone is not confirmed ancestry"}
		if parent.Valid && parent.String != "" {
			session.Lineage = MessageLineage{Parent: "opencode:" + parent.String, Status: "confirmed", Evidence: "native session.parent_id"}
		}
		batch.Sessions = append(batch.Sessions, session)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return batch, err
	}
	if source.Session != "" && len(batch.Sessions) == 0 {
		return batch, &OperationError{Code: "not_found", Message: "native session is absent or outside explicit workspace directories"}
	}
	for i := range batch.Sessions {
		session := &batch.Sessions[i]
		id := strings.TrimPrefix(session.ID, "opencode:")
		messages, err := tx.QueryContext(ctx, "SELECT id, data, time_created, time_updated FROM message WHERE session_id=? ORDER BY time_created,id", id)
		if err != nil {
			return batch, err
		}
		for messages.Next() {
			var sourceID, raw string
			var created, updated int64
			if err := messages.Scan(&sourceID, &raw, &created, &updated); err != nil {
				messages.Close()
				return batch, err
			}
			var data struct {
				Role    string          `json:"role"`
				Summary json.RawMessage `json:"summary"`
				Time    struct {
					Created int64 `json:"created"`
				} `json:"time"`
			}
			if err := json.Unmarshal([]byte(raw), &data); err != nil {
				messages.Close()
				return batch, invalid("invalid native message JSON; scan not published")
			}
			if data.Role != "assistant" && data.Role != "user" {
				continue
			}
			if data.Time.Created != 0 {
				created = data.Time.Created
			}
			if updated > session.Updated {
				session.Updated = updated
			}
			origin := "authored"
			if string(data.Summary) == "true" {
				origin = "synthetic"
			}
			message := AuthoredMessage{ID: session.ID + ":" + sourceID, SourceID: sourceID, SessionID: session.ID, Role: data.Role, Origin: origin, Created: created, Parts: []AuthoredPart{}, Coverage: "authored_text_attachments;tools_results_reasoning_excluded"}
			session.Messages = append(session.Messages, message)
		}
		err = messages.Err()
		messages.Close()
		if err != nil {
			return batch, err
		}
		for j := range session.Messages {
			message := &session.Messages[j]
			// Native OpenCode indexes message_id. Adding a session_id predicate
			// makes SQLite choose its session-only index and scan an entire long
			// conversation for EACH message. Select by globally unique message_id,
			// then independently verify every part's session ownership.
			parts, err := tx.QueryContext(ctx, "SELECT id,data,time_updated,session_id FROM part WHERE message_id=? ORDER BY time_created,id", message.SourceID)
			if err != nil {
				return batch, err
			}
			for parts.Next() {
				var partID, raw, partSession string
				var updated int64
				if err := parts.Scan(&partID, &raw, &updated, &partSession); err != nil {
					parts.Close()
					return batch, err
				}
				if partSession != id {
					parts.Close()
					return batch, invalid("native part/message session ownership is inconsistent")
				}
				var part struct {
					Type      string `json:"type"`
					Text      string `json:"text"`
					Synthetic bool   `json:"synthetic"`
					Ignored   bool   `json:"ignored"`
					MIME      string `json:"mime"`
					Filename  string `json:"filename"`
					URL       string `json:"url"`
				}
				if err := json.Unmarshal([]byte(raw), &part); err != nil {
					parts.Close()
					return batch, invalid("invalid native part JSON; scan not published")
				}
				if updated > session.Updated {
					session.Updated = updated
				}
				origin := "authored"
				if part.Synthetic || message.Origin == "synthetic" {
					origin = "synthetic"
				} else if part.Ignored {
					origin = "unknown"
				}
				switch part.Type {
				case "text":
					message.Parts = append(message.Parts, AuthoredPart{ID: partID, Type: "text", Text: part.Text, Origin: origin})
				case "file":
					uri := part.URL
					if strings.HasPrefix(uri, "data:") {
						uri = "inline:sha256:" + messageHash([]byte(uri))
					}
					message.Parts = append(message.Parts, AuthoredPart{ID: partID, Type: "attachment", Origin: origin, MIME: part.MIME, Filename: part.Filename, URI: uri})
				default:
					message.ExcludedParts = append(message.ExcludedParts, part.Type)
				}
			}
			err = parts.Err()
			parts.Close()
			if err != nil {
				return batch, err
			}
		}
	}
	for i := range batch.Sessions {
		filtered := []AuthoredMessage{}
		for _, m := range batch.Sessions[i].Messages {
			m = retainedMessage(m)
			if len(m.Parts) > 0 {
				filtered = append(filtered, m)
			}
		}
		batch.Sessions[i].Messages = filtered
	}
	inferMessageCopiedHistory(&batch)
	return batch, tx.Commit()
}

// Exact multi-message role/text/timestamp prefix matches with remapped IDs are
// evidence of copied history, NOT evidence of parent direction. Keep every
// candidate and never collapse the distinct source messages.
func inferMessageCopiedHistory(batch *MessageBatch) {
	for i := range batch.Sessions {
		session := &batch.Sessions[i]
		session.Lineage.Candidates = nil
		if session.Lineage.Status != "confirmed" {
			session.Lineage = MessageLineage{Status: "unresolved", Evidence: "no native parent; shared-prefix fingerprint is comparison evidence only, not ancestry"}
		}
		prefix := []string{}
		user, assistant := false, false
		for _, m := range session.Messages {
			m = retainedMessage(m)
			if len(m.Parts) == 0 {
				continue
			}
			parts := append([]AuthoredPart{}, m.Parts...)
			for j := range parts {
				parts[j].ID = ""
			}
			prefix = append(prefix, messageDigest(struct {
				Role    string
				Created int64
				Parts   []AuthoredPart
			}{m.Role, m.Created, parts}))
			user = user || m.Role == "user"
			assistant = assistant || m.Role == "assistant"
			if len(prefix) == 4 {
				break
			}
		}
		if len(prefix) == 4 && user && assistant {
			session.Lineage.SharedPrefix = messageDigest(prefix)
		}
	}
}
