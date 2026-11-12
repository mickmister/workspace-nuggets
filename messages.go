package nuggets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

type AuthoredPart struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Origin   string `json:"origin"`
	MIME     string `json:"mime,omitempty"`
	Filename string `json:"filename,omitempty"`
	URI      string `json:"uri,omitempty"`
}
type AuthoredMessage struct {
	ID            string         `json:"id"`
	SourceID      string         `json:"source_id"`
	SessionID     string         `json:"session_id"`
	Role          string         `json:"role"`
	Origin        string         `json:"origin"`
	Created       int64          `json:"created"`
	Parts         []AuthoredPart `json:"parts"`
	Coverage      string         `json:"coverage"`
	ExcludedParts []string       `json:"excluded_parts,omitempty"`
}
type MessageLineage struct {
	Parent       string   `json:"parent,omitempty"`
	Status       string   `json:"status"`
	Evidence     string   `json:"evidence"`
	Candidates   []string `json:"candidates,omitempty"`
	SharedPrefix string   `json:"shared_prefix,omitempty"`
}
type MessageSession struct {
	ID             string                 `json:"id"`
	Producer       string                 `json:"producer"`
	Version        string                 `json:"version"`
	Directory      string                 `json:"directory"`
	Updated        int64                  `json:"updated"`
	Coverage       string                 `json:"coverage"`
	Lineage        MessageLineage         `json:"lineage"`
	BaseRevision   string                 `json:"base_revision,omitempty"`
	VKAssociations []MessageVKAssociation `json:"vk_associations,omitempty"`
	VKCoverage     string                 `json:"vk_coverage,omitempty"`
	Messages       []AuthoredMessage      `json:"messages,omitempty"`
}
type MessageBatch struct {
	Schema             int              `json:"schema"`
	WorkspaceReference string           `json:"workspace_ref"`
	Sessions           []MessageSession `json:"sessions"`
}
type MessageEntry struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Revision  string `json:"revision"`
	Sequence  uint64 `json:"sequence"`
	Deleted   bool   `json:"deleted,omitempty"`
	Chunk     string `json:"chunk,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}
type MessageDescriptor struct {
	Session         MessageSession `json:"session"`
	Revision        string         `json:"revision"`
	EntriesRoot     string         `json:"entries_root"`
	Count           uint64         `json:"count"`
	MessageCoverage string         `json:"message_coverage,omitempty"`
}
type MessagePublication struct {
	Target           string `json:"target"`
	Snapshot         string `json:"snapshot"`
	Validator        string `json:"validator"`
	Generation       uint64 `json:"generation"`
	Changed          bool   `json:"changed"`
	Verified         bool   `json:"verified"`
	PayloadBackend   string `json:"payload_backend"`
	CatalogBackend   string `json:"catalog_backend"`
	CatalogPublished bool   `json:"catalog_published"`
	UploadedObjects  int    `json:"uploaded_objects"`
	UploadedBytes    int64  `json:"uploaded_bytes"`
}
type MessageResult struct {
	MessageCount uint64              `json:"message_count,omitempty"`
	SessionCount uint64              `json:"session_count,omitempty"`
	TextBytes    uint64              `json:"text_bytes,omitempty"`
	Comparison   map[string]string   `json:"comparison,omitempty"`
	Target       string              `json:"target,omitempty"`
	Batch        *MessageBatch       `json:"batch,omitempty"`
	Publication  *MessagePublication `json:"publication,omitempty"`
	Sessions     []MessageDescriptor `json:"sessions,omitempty"`
	Messages     []AuthoredMessage   `json:"messages,omitempty"`
	Entries      []MessageEntry      `json:"entries,omitempty"`
	Page         *MessagePage        `json:"page,omitempty"`
	Reader       *MessageReader      `json:"reader,omitempty"`
	Digest       string              `json:"digest,omitempty"`
	Snapshot     string              `json:"snapshot,omitempty"`
	Verified     bool                `json:"verified,omitempty"`
}
type MessageFilter struct {
	Session string `json:"session,omitempty"`
	Role    string `json:"role,omitempty"`
}
type MessageStore struct {
	WorkspaceReference, Workspace, Target, CacheDir string
	Profile                                         MessageTarget
	catalog                                         messageObjects
	payload                                         messageObjects
	checkpoint                                      messageObjects
}

// Payloads contain no source IDs: forks can reuse exact content while occurrences
// retain native IDs, part IDs, role, ordering and timestamps independently.
type messageContentPart struct {
	Type      string   `json:"t"`
	Text      string   `json:"x,omitempty"`
	Fragments []string `json:"f,omitempty"`
	Origin    string   `json:"o"`
	MIME      string   `json:"m,omitempty"`
	Filename  string   `json:"n,omitempty"`
	URI       string   `json:"u,omitempty"`
}
type messageOccurrence struct {
	ID       string   `json:"id"`
	Source   string   `json:"src"`
	Role     string   `json:"r"`
	Origin   string   `json:"o"`
	Created  int64    `json:"t"`
	Sequence uint64   `json:"q"`
	Content  string   `json:"c"`
	PartIDs  []string `json:"p"`
	Coverage string   `json:"cv,omitempty"`
	Revision string   `json:"v"`
}
type messageEvent struct {
	Session string `json:"s"`
	Root    string `json:"ref"`
	Ordinal uint64 `json:"i"`
	Deleted bool   `json:"d,omitempty"`
}
type messageLatest struct {
	Session  string `json:"s"`
	Sequence uint64 `json:"q"`
}

func messageHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func messageDigest(v any) string     { return messageHash(encodeMessageJSON(v)) }
func randomNugID() (string, error) {
	value := make([]byte, 24)
	_, err := rand.Read(value)
	return hex.EncodeToString(value), err
}
func validateMessageLabel(value string) error {
	if value == "" || len(value) > 100 {
		return invalid("target/reader name must contain 1–100 characters")
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return invalid("target/reader names use letters, digits, hyphens and underscores")
		}
	}
	return nil
}
func NewMessageStore(reference, workspace, target, cache string, profile MessageTarget) (*MessageStore, error) {
	if !validWorkspaceReference(reference) || validateMessageLabel(target) != nil {
		return nil, invalid("messages require stable workspace identity and named target")
	}
	objects, err := newNugS3(profile, reference, "messages")
	if err != nil {
		return nil, err
	}
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		cache = filepath.Join(base, "nugs")
	}
	s := &MessageStore{WorkspaceReference: reference, Workspace: workspace, Target: target, CacheDir: cache, Profile: profile, catalog: objects, payload: objects}
	if profile.Checkpoint != "local" {
		s.checkpoint = objects
	}
	return s, nil
}
func (s *MessageStore) root(ctx context.Context, snapshot string, allowMissing bool) (nugRoot, string, string, error) {
	return loadNugRoot(ctx, s.catalog, s.WorkspaceReference, "messages", snapshot, allowMissing)
}

func retainedMessage(m AuthoredMessage) AuthoredMessage {
	m.ExcludedParts = nil
	parts := []AuthoredPart{}
	for _, part := range m.Parts {
		if part.Type == "attachment" || part.Type == "text" && part.Text != "" {
			parts = append(parts, part)
		}
	}
	m.Parts = parts
	return m
}
func normalizedMessageSession(input MessageSession) MessageSession {
	s := input
	s.BaseRevision = ""
	s.Updated = 0
	s.VKAssociations = nil
	s.VKCoverage = ""
	s.Lineage = MessageLineage{}
	s.Messages = []AuthoredMessage{}
	for _, message := range input.Messages {
		m := retainedMessage(message)
		if len(m.Parts) > 0 {
			s.Messages = append(s.Messages, m)
		}
	}
	return s
}
func validateMessageBatch(batch MessageBatch, reference string) error {
	if batch.Schema != 1 || batch.WorkspaceReference != reference {
		return invalid("message batch requires schema 1 and matching workspace_ref")
	}
	sessions, ids := map[string]bool{}, map[string]bool{}
	for _, session := range batch.Sessions {
		if session.ID == "" || len(session.ID) > 1024 || sessions[session.ID] || session.Producer == "" || session.Coverage == "" {
			return invalid("invalid session identity/coverage")
		}
		sessions[session.ID] = true
		if session.Lineage.Status != "confirmed" && session.Lineage.Status != "inferred" && session.Lineage.Status != "unresolved" || session.Lineage.Evidence == "" || session.Lineage.Parent == session.ID {
			return invalid("session lineage requires a valid status, evidence and non-self parent")
		}
		for _, m := range session.Messages {
			if m.ID == "" || len(m.ID) > 2048 || m.SourceID == "" || m.SessionID != session.ID || ids[m.ID] || m.Coverage == "" || m.Role != "user" && m.Role != "assistant" || m.Origin != "authored" && m.Origin != "synthetic" && m.Origin != "unknown" {
				return invalid("invalid authored message identity/role/origin/coverage")
			}
			ids[m.ID] = true
			if len(encodeMessageJSON(m)) > 32<<20 {
				return invalid("single message exceeds supported 32 MiB; source not published")
			}
			for _, part := range m.Parts {
				if part.Type != "text" && part.Type != "attachment" || part.Origin != "authored" && part.Origin != "synthetic" && part.Origin != "unknown" {
					return invalid("message parts require text/attachment and explicit origin")
				}
			}
		}
	}
	return nil
}
func nugBinaryKey(hash string) string { return "objects/" + hash + ".bin.gz" }
func writeNugBytes(ctx context.Context, objects messageObjects, data []byte) ([]string, error) {
	refs := []string{}
	for start := 0; start < len(data); start += nugBlockLimit {
		end := start + nugBlockLimit
		if end > len(data) {
			end = len(data)
		}
		packed, err := gzipNug(data[start:end])
		if err != nil {
			return nil, err
		}
		hash := messageHash(packed)
		if err := immutableObject(ctx, objects, nugBinaryKey(hash), packed); err != nil {
			return nil, err
		}
		refs = append(refs, hash)
	}
	return refs, nil
}
func readNugBytes(ctx context.Context, objects messageObjects, refs []string, max int) ([]byte, error) {
	var result []byte
	for _, hash := range refs {
		if !validNugHash(hash) {
			return nil, invalid("invalid nug payload digest")
		}
		packed, version, err := objects.get(ctx, nugBinaryKey(hash))
		if err != nil {
			return nil, err
		}
		if version == "" || messageHash(packed) != hash {
			return nil, &OperationError{Code: "corrupt_content", Message: "missing or corrupt nug payload"}
		}
		raw, err := ungzipNug(packed)
		if err != nil {
			return nil, err
		}
		if len(result)+len(raw) > max {
			return nil, invalid("assembled nug payload exceeds supported size")
		}
		result = append(result, raw...)
	}
	return result, nil
}
func (s *MessageStore) content(ctx context.Context, b *nugBlocks, root *nugRoot, m AuthoredMessage) (string, []string, error) {
	parts := make([]messageContentPart, 0, len(m.Parts))
	ids := make([]string, 0, len(m.Parts))
	for _, p := range m.Parts {
		parts = append(parts, messageContentPart{Type: p.Type, Text: p.Text, Origin: p.Origin, MIME: p.MIME, Filename: p.Filename, URI: p.URI})
		ids = append(ids, p.ID)
	}
	hash := messageDigest(parts)
	existing, err := b.get(ctx, root.Content, hash)
	if err != nil {
		return "", nil, err
	}
	if existing != nil {
		return hash, ids, nil
	}
	for i := range parts {
		if len(parts[i].Text) > 32<<10 {
			refs, err := writeNugBytes(ctx, s.payload, []byte(parts[i].Text))
			if err != nil {
				return "", nil, err
			}
			parts[i].Text = ""
			parts[i].Fragments = refs
		}
	}
	root.Content, err = b.set(ctx, root.Content, hash, encodeMessageJSON(parts))
	return hash, ids, err
}
func occurrenceRows(ctx context.Context, b *nugBlocks, root string) ([]messageOccurrence, error) {
	rows, err := b.all(ctx, root)
	if err != nil {
		return nil, err
	}
	out := make([]messageOccurrence, 0, len(rows))
	for i, row := range rows {
		if row.Key != ordinalKey(uint64(i)) {
			return nil, invalid("session message ordinals are not contiguous")
		}
		var m messageOccurrence
		if err := json.Unmarshal(row.Value, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
func getMessageDescriptor(ctx context.Context, b *nugBlocks, root, id string) (MessageDescriptor, bool, error) {
	var d MessageDescriptor
	data, err := b.get(ctx, root, id)
	if err != nil {
		return d, false, err
	}
	if data == nil {
		return d, false, nil
	}
	err = json.Unmarshal(data, &d)
	return d, true, err
}

func (s *MessageStore) Publish(ctx context.Context, batch MessageBatch) (*MessagePublication, error) {
	if s.Profile.Payload != "s3" || s.Profile.Catalog != "s3" || s.Profile.Checkpoint == "git" {
		return nil, invalid("Git message/catalog/checkpoint writes are disabled")
	}
	if err := validateMessageBatch(batch, s.WorkspaceReference); err != nil {
		return nil, err
	}
	receipt := &MessagePublication{Target: s.Target, PayloadBackend: "s3", CatalogBackend: "s3"}
	for attempt := 0; attempt < 8; attempt++ {
		root, validator, snapshot, err := s.root(ctx, "", true)
		if err != nil {
			return receipt, err
		}
		if root.Epoch == "" {
			root.Epoch, err = randomNugID()
			if err != nil {
				return receipt, err
			}
		}
		blocks := newNugBlocks(s.catalog)
		changed := false
		sessionRoots := []string{}
		for _, input := range batch.Sessions {
			normalized := normalizedMessageSession(input)
			revision := messageDigest(normalized)
			old, exists, err := getMessageDescriptor(ctx, blocks, root.Sessions, input.ID)
			if err != nil {
				return receipt, err
			}
			if exists && old.Revision == revision {
				continue
			}
			if exists && (input.BaseRevision != old.Revision || input.Updated < old.Session.Updated) || !exists && input.BaseRevision != "" {
				return receipt, &OperationError{Code: "conflict", Message: "source session changed since observation: " + input.ID}
			}
			previousRows, err := occurrenceRows(ctx, blocks, old.EntriesRoot)
			if err != nil {
				return receipt, err
			}
			previous := map[string]messageOccurrence{}
			ordinals := map[string]uint64{}
			for i, m := range previousRows {
				previous[m.ID] = m
				ordinals[m.ID] = uint64(i)
			}
			d := MessageDescriptor{Session: input, Revision: revision, EntriesRoot: old.EntriesRoot, Count: uint64(len(normalized.Messages))}
			d.Session.Messages = nil
			d.Session.BaseRevision = ""
			d.Session.Lineage.Candidates = nil
			if len(normalized.Messages) > 0 {
				d.MessageCoverage = normalized.Messages[0].Coverage
			}
			type queuedEvent struct {
				occ     messageOccurrence
				ordinal uint64
				deleted bool
				old     bool
			}
			queued := []queuedEvent{}
			for i, m := range normalized.Messages {
				before, had := previous[m.ID]
				delete(previous, m.ID)
				latestData, err := blocks.get(ctx, root.Latest, m.ID)
				if err != nil {
					return receipt, err
				}
				if latestData != nil {
					var latest messageLatest
					if err := json.Unmarshal(latestData, &latest); err != nil {
						return receipt, err
					}
					if latest.Session != input.ID {
						return receipt, invalid("message ID belongs to another session")
					}
				}
				if had && before.Role != m.Role {
					return receipt, invalid("existing message role is immutable; use a new source identity")
				}
				messageRevision := messageDigest(m)
				content, partIDs, err := s.content(ctx, blocks, &root, m)
				if err != nil {
					return receipt, err
				}
				occ := messageOccurrence{ID: m.ID, Source: m.SourceID, Role: m.Role, Origin: m.Origin, Created: m.Created, Content: content, PartIDs: partIDs, Revision: messageRevision}
				occ.Coverage = m.Coverage
				if had && before.Revision == messageRevision {
					occ.Sequence = before.Sequence
				} else {
					root.High++
					occ.Sequence = root.High
					queued = append(queued, queuedEvent{occ: occ, ordinal: uint64(i)})
				}
				d.EntriesRoot, err = blocks.set(ctx, d.EntriesRoot, ordinalKey(uint64(i)), encodeMessageJSON(occ))
				if err != nil {
					return receipt, err
				}
			}
			for i := len(normalized.Messages); i < len(previousRows); i++ {
				d.EntriesRoot, err = blocks.set(ctx, d.EntriesRoot, ordinalKey(uint64(i)), nil)
				if err != nil {
					return receipt, err
				}
			}
			removed := make([]string, 0, len(previous))
			for id := range previous {
				removed = append(removed, id)
			}
			sort.Strings(removed)
			for _, id := range removed {
				occ := previous[id]
				root.High++
				occ.Sequence = root.High
				queued = append(queued, queuedEvent{occ: occ, ordinal: ordinals[id], deleted: true, old: true})
			}
			for _, event := range queued {
				reference := d.EntriesRoot
				if event.old {
					reference = old.EntriesRoot
				}
				root.Events, err = blocks.set(ctx, root.Events, ordinalKey(event.occ.Sequence), encodeMessageJSON(messageEvent{Session: input.ID, Root: reference, Ordinal: event.ordinal, Deleted: event.deleted}))
				if err != nil {
					return receipt, err
				}
				root.Latest, err = blocks.set(ctx, root.Latest, event.occ.ID, encodeMessageJSON(messageLatest{Session: input.ID, Sequence: event.occ.Sequence}))
				if err != nil {
					return receipt, err
				}
			}
			root.Sessions, err = blocks.set(ctx, root.Sessions, input.ID, encodeMessageJSON(d))
			if err != nil {
				return receipt, err
			}
			sessionRoots = append(sessionRoots, d.EntriesRoot)
			changed = true
			blocks.prune(append(sessionRoots, root.Sessions, root.Latest, root.Events, root.Content)...)
		}
		if !changed && validator != "" {
			receipt.Snapshot, receipt.Validator, receipt.Generation, receipt.Verified, receipt.CatalogPublished = snapshot, validator, root.Generation, true, true
			return receipt, nil
		}
		root.Generation++
		roots := append(sessionRoots, root.Sessions, root.Latest, root.Events, root.Content)
		flushErr := blocks.flush(ctx, roots...)
		receipt.UploadedObjects += blocks.Uploaded
		receipt.UploadedBytes += blocks.UploadedBytes
		if flushErr != nil {
			return receipt, flushErr
		}
		snapshot, newVersion, err := publishNugRoot(ctx, s.catalog, root, validator)
		if errors.Is(err, errMessageCAS) {
			continue
		}
		receipt.Snapshot = snapshot
		if err != nil {
			return receipt, err
		}
		receipt.Validator, receipt.Generation, receipt.Changed, receipt.CatalogPublished = newVersion, root.Generation, true, true
		_, _, actual, err := s.root(ctx, snapshot, false)
		if err != nil {
			return receipt, err
		}
		if actual != snapshot {
			return receipt, invalid("catalog readback differs")
		}
		receipt.Verified = true
		return receipt, nil
	}
	return receipt, &OperationError{Code: "conflict", Message: "catalog kept changing; retry same source batch"}
}

func (s *MessageStore) readOccurrence(ctx context.Context, b *nugBlocks, root nugRoot, session string, occ messageOccurrence, coverage string) (AuthoredMessage, error) {
	parts, cached := b.messageContent[occ.Content]
	if !cached {
		data, err := b.get(ctx, root.Content, occ.Content)
		if err != nil {
			return AuthoredMessage{}, err
		}
		if data == nil {
			return AuthoredMessage{}, invalid("message content reference is missing")
		}
		if err := json.Unmarshal(data, &parts); err != nil {
			return AuthoredMessage{}, err
		}
		size := 0
		for i := range parts {
			if len(parts[i].Fragments) > 0 {
				raw, err := readNugBytes(ctx, s.payload, parts[i].Fragments, 32<<20)
				if err != nil {
					return AuthoredMessage{}, err
				}
				parts[i].Text = string(raw)
				parts[i].Fragments = nil
			}
			size += len(parts[i].Text) + len(parts[i].URI)
		}
		if size+b.messageCacheBytes <= 16<<20 {
			b.messageContent[occ.Content] = parts
			b.messageCacheBytes += size
		}
	}
	if len(parts) != len(occ.PartIDs) {
		return AuthoredMessage{}, invalid("message part provenance mismatch")
	}
	if occ.Coverage != "" {
		coverage = occ.Coverage
	}
	m := AuthoredMessage{ID: occ.ID, SourceID: occ.Source, SessionID: session, Role: occ.Role, Origin: occ.Origin, Created: occ.Created, Coverage: coverage, Parts: []AuthoredPart{}}
	for i, p := range parts {
		m.Parts = append(m.Parts, AuthoredPart{ID: occ.PartIDs[i], Type: p.Type, Text: p.Text, Origin: p.Origin, MIME: p.MIME, Filename: p.Filename, URI: p.URI})
	}
	if messageDigest(m) != occ.Revision {
		return m, &OperationError{Code: "corrupt_content", Message: "reconstructed message revision mismatch"}
	}
	return m, nil
}
func (s *MessageStore) Sessions(ctx context.Context, snapshot, session string) (*MessageResult, error) {
	root, _, pin, err := s.root(ctx, snapshot, false)
	if err != nil {
		return nil, err
	}
	b := newNugBlocks(s.catalog)
	result := &MessageResult{Target: s.Target, Snapshot: pin, Sessions: []MessageDescriptor{}}
	if session != "" {
		d, ok, err := getMessageDescriptor(ctx, b, root.Sessions, session)
		if err != nil {
			return nil, err
		}
		if ok {
			result.Sessions = append(result.Sessions, d)
		}
		return result, nil
	}
	rows, err := b.all(ctx, root.Sessions)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var d MessageDescriptor
		if err := json.Unmarshal(row.Value, &d); err != nil {
			return nil, err
		}
		result.Sessions = append(result.Sessions, d)
	}
	return result, nil
}
func (s *MessageStore) Query(ctx context.Context, snapshot string, filter MessageFilter, messageID string) (*MessageResult, error) {
	if filter.Role != "" && filter.Role != "user" && filter.Role != "assistant" {
		return nil, invalid("invalid message role filter")
	}
	root, _, pin, err := s.root(ctx, snapshot, false)
	if err != nil {
		return nil, err
	}
	b := newNugBlocks(s.catalog)
	result, err := s.Sessions(ctx, pin, filter.Session)
	if err != nil {
		return nil, err
	}
	result.Messages = []AuthoredMessage{}
	for _, d := range result.Sessions {
		occurrences, err := occurrenceRows(ctx, b, d.EntriesRoot)
		if err != nil {
			return nil, err
		}
		for _, occ := range occurrences {
			if filter.Role != "" && occ.Role != filter.Role || messageID != "" && occ.ID != messageID {
				continue
			}
			m, err := s.readOccurrence(ctx, b, root, d.Session.ID, occ, d.MessageCoverage)
			if err != nil {
				return nil, err
			}
			result.Messages = append(result.Messages, m)
		}
	}
	if messageID != "" && len(result.Messages) == 0 {
		return nil, &OperationError{Code: "not_found", Message: "message not found"}
	}
	result.Digest = messageDigest(result.Messages)
	result.MessageCount = uint64(len(result.Messages))
	result.SessionCount = uint64(len(result.Sessions))
	result.Verified = true
	return result, nil
}

// Verify streams semantic records into a digest instead of returning every body
// or materializing a workspace-sized JSON response. Occurrence ordering matches Query.
func (s *MessageStore) Verify(ctx context.Context, snapshot string, filter MessageFilter) (*MessageResult, error) {
	if filter.Role != "" && filter.Role != "user" && filter.Role != "assistant" {
		return nil, invalid("invalid message role filter")
	}
	root, _, pin, err := s.root(ctx, snapshot, false)
	if err != nil {
		return nil, err
	}
	result, err := s.Sessions(ctx, pin, filter.Session)
	if err != nil {
		return nil, err
	}
	b := newNugBlocks(s.catalog)
	digest := sha256.New()
	digest.Write([]byte("["))
	first := true
	for _, d := range result.Sessions {
		after := ""
		for {
			rows, err := b.scan(ctx, d.EntriesRoot, after, 128)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				var occ messageOccurrence
				if err := json.Unmarshal(row.Value, &occ); err != nil {
					return nil, err
				}
				if filter.Role != "" && filter.Role != occ.Role {
					continue
				}
				m, err := s.readOccurrence(ctx, b, root, d.Session.ID, occ, d.MessageCoverage)
				if err != nil {
					return nil, err
				}
				if !first {
					digest.Write([]byte(","))
				}
				first = false
				digest.Write(encodeMessageJSON(m))
				result.MessageCount++
				for _, part := range m.Parts {
					result.TextBytes += uint64(len(part.Text))
				}
			}
			after = rows[len(rows)-1].Key
		}
	}
	digest.Write([]byte("]"))
	result.Digest = hex.EncodeToString(digest.Sum(nil))
	result.SessionCount = uint64(len(result.Sessions))
	result.Sessions = nil
	result.Verified = true
	return result, nil
}
