package nuggets

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

type messageLocalObjects struct{ filename string }

func (s messageLocalObjects) open(ctx context.Context) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(s.filename), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(s.filename)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS message_state (key TEXT PRIMARY KEY,value BLOB NOT NULL)"); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(s.filename, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (s messageLocalObjects) get(ctx context.Context, key string) ([]byte, string, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	var data []byte
	err = db.QueryRowContext(ctx, "SELECT value FROM message_state WHERE key=?", key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return data, messageHash(data), nil
}
func (s messageLocalObjects) cas(ctx context.Context, key, expected string, data []byte, extras map[string][]byte) (string, error) {
	if len(extras) != 0 {
		return "", invalid("local checkpoints cannot hold payloads")
	}
	db, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var result sql.Result
	if expected == "" {
		result, err = db.ExecContext(ctx, "INSERT INTO message_state(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", key, data)
	} else {
		var before []byte
		err = db.QueryRowContext(ctx, "SELECT value FROM message_state WHERE key=?", key).Scan(&before)
		if errors.Is(err, sql.ErrNoRows) {
			return "", errMessageCAS
		}
		if err != nil {
			return "", err
		}
		if messageHash(before) != expected {
			return "", errMessageCAS
		}
		result, err = db.ExecContext(ctx, "UPDATE message_state SET value=? WHERE key=? AND value=?", data, key, before)
	}
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", errMessageCAS
	}
	return messageHash(data), nil
}

// One constant-size checkpoint per reader. Replay plans live in authenticated
// opaque tokens, not an ever-growing issued-page map. Immutable catalog roots
// share old tree blocks; replay fails explicitly if referenced blocks disappear.
type MessageReader struct {
	Schema       int           `json:"schema"`
	Binding      string        `json:"binding"`
	Name         string        `json:"name"`
	Filter       MessageFilter `json:"filter"`
	Epoch        string        `json:"epoch"`
	Generation   uint64        `json:"generation"`
	Acknowledged uint64        `json:"acknowledged"`
	Pending      string        `json:"pending,omitempty"`
	Delivered    string        `json:"delivered,omitempty"`
	LastAck      string        `json:"last_ack,omitempty"`
	Key          string        `json:"token_key,omitempty"`
}
type messagePagePlan struct {
	Binding    string `json:"b"`
	Reader     string `json:"r"`
	Snapshot   string `json:"s"`
	Epoch      string `json:"e"`
	Generation uint64 `json:"g"`
	From       uint64 `json:"f"`
	Through    uint64 `json:"t"`
	Limit      int    `json:"n"`
	Nonce      string `json:"nonce"`
}
type MessagePage struct {
	Token            string            `json:"token"`
	Snapshot         string            `json:"snapshot"`
	Generation       uint64            `json:"generation"`
	Acknowledged     uint64            `json:"acknowledged"`
	ScanThrough      uint64            `json:"scan_through"`
	DeliveredThrough uint64            `json:"delivered_through"`
	Entries          []MessageEntry    `json:"entries"`
	Messages         []AuthoredMessage `json:"messages,omitempty"`
	More             bool              `json:"more"`
}

func (s *MessageStore) readerStorage() messageObjects {
	if s.checkpoint != nil {
		return s.checkpoint
	}
	if s.Profile.Checkpoint != "local" {
		return s.catalog
	}
	return messageLocalObjects{filepath.Join(s.CacheDir, "messages", s.WorkspaceReference, s.Target, "index.sqlite")}
}
func (s *MessageStore) readerBinding() string {
	return messageDigest(struct{ Workspace, Endpoint, Bucket, Prefix, Kind string }{s.WorkspaceReference, s.Profile.Endpoint, s.Profile.Bucket, s.Profile.Prefix, "messages"})
}
func readerView(state MessageReader) *MessageReader { state.Key = ""; return &state }
func (s *MessageStore) loadReader(ctx context.Context, name string, filter MessageFilter) (MessageReader, string, error) {
	state := MessageReader{Schema: 2, Binding: s.readerBinding(), Name: name, Filter: filter}
	if err := validateMessageLabel(name); err != nil {
		return state, "", err
	}
	if filter.Role != "" && filter.Role != "user" && filter.Role != "assistant" {
		return state, "", invalid("role must be user or assistant")
	}
	data, version, err := s.readerStorage().get(ctx, "readers/"+name+".json")
	if err != nil || version == "" {
		return state, version, err
	}
	if len(data) > 8192 {
		return state, "", invalid("reader checkpoint exceeds 8 KiB")
	}
	state = MessageReader{}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, "", err
	}
	if state.Schema != 2 || state.Binding != s.readerBinding() || state.Name != name || state.Filter != filter || len(state.Key) != 48 {
		return state, "", &OperationError{Code: "conflict", Message: "reader target/filter/schema binding differs; explicit recovery required"}
	}
	return state, version, nil
}
func (s *MessageStore) saveReader(ctx context.Context, state MessageReader, version string) error {
	data := encodeMessageJSON(state)
	if len(data) > 8192 {
		return invalid("reader checkpoint exceeds 8 KiB")
	}
	_, err := s.readerStorage().cas(ctx, "readers/"+state.Name+".json", version, data, nil)
	if errors.Is(err, errMessageCAS) {
		return &OperationError{Code: "conflict", Message: "reader changed concurrently; retry operation"}
	}
	return err
}
func signMessagePlan(plan messagePagePlan, key string) string {
	data := encodeMessageJSON(plan)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + hex.EncodeToString(mac.Sum(nil))
}
func parseMessagePlan(token string, state MessageReader) (messagePagePlan, error) {
	var plan messagePagePlan
	if len(token) > 2048 || state.Key == "" {
		return plan, invalid("invalid page token")
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return plan, invalid("invalid page token")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return plan, invalid("invalid page token")
	}
	sig, err := hex.DecodeString(signature)
	if err != nil {
		return plan, invalid("invalid page token")
	}
	mac := hmac.New(sha256.New, []byte(state.Key))
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return plan, invalid("page token authentication failed")
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return plan, err
	}
	if plan.Binding != state.Binding || plan.Reader != state.Name || plan.Epoch != state.Epoch || plan.Through < plan.From || plan.Limit < 1 || plan.Limit > 1000 {
		return plan, invalid("misbound page token")
	}
	return plan, nil
}
func (s *MessageStore) checkReaderRoot(state MessageReader, root nugRoot) error {
	if state.Epoch != "" && state.Epoch != root.Epoch || state.Acknowledged > root.High {
		return &OperationError{Code: "catalog_reset", Message: "catalog epoch/position no longer matches reader; explicit recovery required"}
	}
	return nil
}

func (s *MessageStore) eventPage(ctx context.Context, root nugRoot, filter MessageFilter, from, through uint64, limit int, delivered bool) (*MessagePage, error) {
	b := newNugBlocks(s.catalog)
	rows, err := b.scan(ctx, root.Events, ordinalKey(from), 4096)
	if err != nil {
		return nil, err
	}
	page := &MessagePage{Acknowledged: from, ScanThrough: from, Entries: []MessageEntry{}}
	expected := from + 1
	for _, row := range rows {
		sequence, err := strconv.ParseUint(row.Key, 10, 64)
		if err != nil || sequence != expected {
			return nil, invalid("event log has a missing or invalid sequence")
		}
		expected++
		if through != 0 && sequence > through {
			break
		}
		page.ScanThrough = sequence
		var event messageEvent
		if err := json.Unmarshal(row.Value, &event); err != nil {
			return nil, err
		}
		if filter.Session != "" && event.Session != filter.Session {
			continue
		}
		data, err := b.get(ctx, event.Root, ordinalKey(event.Ordinal))
		if err != nil {
			return nil, err
		}
		if data == nil {
			return nil, invalid("event occurrence is unavailable")
		}
		var occ messageOccurrence
		if err := json.Unmarshal(data, &occ); err != nil {
			return nil, err
		}
		if filter.Role != "" && occ.Role != filter.Role {
			continue
		}
		latestData, err := b.get(ctx, root.Latest, occ.ID)
		if err != nil {
			return nil, err
		}
		if latestData == nil {
			return nil, invalid("latest message index is unavailable")
		}
		var latest messageLatest
		if err := json.Unmarshal(latestData, &latest); err != nil {
			return nil, err
		}
		if latest.Sequence != sequence {
			continue
		}
		entry := MessageEntry{ID: occ.ID, SessionID: event.Session, Role: occ.Role, Revision: occ.Revision, Sequence: sequence, Deleted: event.Deleted}
		page.Entries = append(page.Entries, entry)
		if delivered {
			m, err := s.readOccurrence(ctx, b, root, event.Session, occ, occ.Coverage)
			if err != nil {
				return nil, err
			}
			page.Messages = append(page.Messages, m)
			page.DeliveredThrough = sequence
		}
		if len(page.Entries) == limit {
			break
		}
	}
	if through != 0 && page.ScanThrough != through {
		return nil, invalid("pinned page range is incomplete")
	}
	if page.ScanThrough == from && from < root.High {
		return nil, invalid("event log is shorter than catalog high-water mark")
	}
	page.More = page.ScanThrough < root.High
	return page, nil
}
func (s *MessageStore) Prepare(ctx context.Context, name string, filter MessageFilter, limit int) (*MessagePage, error) {
	if limit < 1 || limit > 1000 {
		return nil, invalid("page limit must be 1–1000")
	}
	root, _, snapshot, err := s.root(ctx, "", false)
	if err != nil {
		return nil, err
	}
	state, version, err := s.loadReader(ctx, name, filter)
	if err != nil {
		return nil, err
	}
	if err := s.checkReaderRoot(state, root); err != nil {
		return nil, err
	}
	if state.Key == "" {
		state.Key, err = randomNugID()
		if err != nil {
			return nil, err
		}
		state.Epoch = root.Epoch
	}
	if state.Pending != "" {
		return s.readIssuedPage(ctx, state, state.Pending, false)
	}
	page, err := s.eventPage(ctx, root, filter, state.Acknowledged, 0, limit, false)
	if err != nil {
		return nil, err
	}
	page.Snapshot, page.Generation = snapshot, state.Generation
	if page.ScanThrough == state.Acknowledged {
		if version == "" {
			if err := s.saveReader(ctx, state, version); err != nil {
				return nil, err
			}
		}
		return page, nil
	}
	nonce, err := randomNugID()
	if err != nil {
		return nil, err
	}
	plan := messagePagePlan{state.Binding, name, snapshot, root.Epoch, state.Generation, state.Acknowledged, page.ScanThrough, limit, nonce}
	page.Token = signMessagePlan(plan, state.Key)
	state.Pending = page.Token
	state.Delivered = ""
	if err := s.saveReader(ctx, state, version); err != nil {
		return nil, err
	}
	return page, nil
}
func (s *MessageStore) readIssuedPage(ctx context.Context, state MessageReader, token string, delivered bool) (*MessagePage, error) {
	plan, err := parseMessagePlan(token, state)
	if err != nil {
		return nil, err
	}
	root, _, _, err := s.root(ctx, plan.Snapshot, false)
	if err != nil {
		return nil, err
	}
	if root.Epoch != plan.Epoch || plan.Through > root.High {
		return nil, invalid("page snapshot epoch/range differs")
	}
	page, err := s.eventPage(ctx, root, state.Filter, plan.From, plan.Through, plan.Limit, delivered)
	if err != nil {
		return nil, err
	}
	page.Token, page.Snapshot, page.Generation = token, plan.Snapshot, plan.Generation
	return page, nil
}
func (s *MessageStore) ReadPage(ctx context.Context, name string, filter MessageFilter, token string) (*MessagePage, error) {
	state, version, err := s.loadReader(ctx, name, filter)
	if err != nil {
		return nil, err
	}
	page, err := s.readIssuedPage(ctx, state, token, true)
	if err != nil {
		return nil, err
	}
	if state.Pending == token && state.Delivered != messageHash([]byte(token)) {
		state.Delivered = messageHash([]byte(token))
		if err := s.saveReader(ctx, state, version); err != nil {
			return nil, err
		}
	}
	return page, nil
}
func (s *MessageStore) Ack(ctx context.Context, name string, filter MessageFilter, token string) (*MessageReader, error) {
	state, version, err := s.loadReader(ctx, name, filter)
	if err != nil {
		return nil, err
	}
	plan, err := parseMessagePlan(token, state)
	if err != nil {
		return nil, err
	}
	root, _, _, err := s.root(ctx, "", false)
	if err != nil {
		return nil, err
	}
	if err := s.checkReaderRoot(state, root); err != nil {
		return nil, err
	}
	digest := messageHash([]byte(token))
	if state.LastAck == digest && state.Generation == plan.Generation+1 && state.Acknowledged == plan.Through {
		return readerView(state), nil
	}
	if state.Pending != token || state.Delivered != digest || state.Generation != plan.Generation || state.Acknowledged != plan.From {
		return nil, &OperationError{Code: "conflict", Message: "ACK requires current successfully delivered page and matching generation"}
	}
	state.Acknowledged = plan.Through
	state.Generation++
	state.Pending = ""
	state.Delivered = ""
	state.LastAck = digest
	if err := s.saveReader(ctx, state, version); err != nil {
		return nil, err
	}
	return readerView(state), nil
}
