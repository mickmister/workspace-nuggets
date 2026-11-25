package nuggets

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const messageTestReference = "ws-112ddba5d5a701238022cf9d6319f65d"

type memoryMessageObjects struct {
	mu        sync.Mutex
	data      map[string][]byte
	beforeCAS func(string)
	failRoot  bool
	gets      int
}

func (m *memoryMessageObjects) get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	value, ok := m.data[key]
	if !ok {
		return nil, "", nil
	}
	return bytes.Clone(value), messageHash(value), nil
}
func (m *memoryMessageObjects) cas(_ context.Context, key, expected string, data []byte, extras map[string][]byte) (string, error) {
	if len(data) > nugObjectLimit {
		return "", invalid("object budget")
	}
	if key == nugRootKey && m.beforeCAS != nil {
		hook := m.beforeCAS
		m.beforeCAS = nil
		hook(key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if key == nugRootKey && m.failRoot {
		return "", errors.New("injected root outage")
	}
	actual := ""
	if old, ok := m.data[key]; ok {
		actual = messageHash(old)
	}
	if expected != actual {
		return "", errMessageCAS
	}
	for k, v := range extras {
		m.data[k] = bytes.Clone(v)
	}
	m.data[key] = bytes.Clone(data)
	return messageHash(data), nil
}
func memoryMessages(t *testing.T) (*MessageStore, *memoryMessageObjects) {
	t.Helper()
	objects := &memoryMessageObjects{data: map[string][]byte{}}
	return &MessageStore{WorkspaceReference: messageTestReference, Workspace: "fixture", Target: "fixture", CacheDir: t.TempDir(), Profile: MessageTarget{Payload: "s3", Catalog: "s3", Layout: "workspace-v2", Checkpoint: "catalog"}, catalog: objects, payload: objects}, objects
}
func authoredFixture(id string, count int) MessageSession {
	s := MessageSession{ID: id, Producer: "unrelated-fixture", Version: "1", Directory: "/fixture", Updated: 1, Coverage: "complete_fixture", Lineage: MessageLineage{Status: "unresolved", Evidence: "no parent evidence"}, Messages: []AuthoredMessage{}}
	for i := 0; i < count; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		s.Messages = append(s.Messages, AuthoredMessage{ID: fmt.Sprintf("%s:m%d", id, i), SourceID: fmt.Sprintf("m%d", i), SessionID: id, Role: role, Origin: "authored", Created: int64(i), Coverage: "fixture_authored", Parts: []AuthoredPart{{ID: fmt.Sprintf("p%d", i), Type: "text", Text: "equal text\n", Origin: "authored"}}})
	}
	return s
}
func publishFixture(t *testing.T, s *MessageStore, sessions ...MessageSession) *MessagePublication {
	t.Helper()
	r, err := s.Publish(context.Background(), MessageBatch{Schema: 1, WorkspaceReference: messageTestReference, Sessions: sessions})
	if err != nil || r == nil || !r.Verified {
		t.Fatalf("publish: %+v %v", r, err)
	}
	return r
}
func sessionBase(t *testing.T, s *MessageStore, id string) string {
	t.Helper()
	result, err := s.Sessions(context.Background(), "", id)
	if err != nil || len(result.Sessions) != 1 {
		t.Fatalf("session: %+v %v", result, err)
	}
	return result.Sessions[0].Revision
}

func TestNugTreeBudgetsDeltaAndBoundedRange(t *testing.T) {
	ctx := context.Background()
	objects := &memoryMessageObjects{data: map[string][]byte{}}
	b := newNugBlocks(objects)
	root := ""
	for i := 0; i < 3000; i++ {
		var err error
		root, err = b.set(ctx, root, ordinalKey(uint64(i)), encodeMessageJSON(map[string]int{"value": i}))
		if err != nil {
			t.Fatal(err)
		}
		if i%128 == 0 {
			b.prune(root)
		}
	}
	if err := b.flush(ctx, root); err != nil {
		t.Fatal(err)
	}
	if b.Uploaded > 110 {
		t.Fatal("intermediate tree versions leaked", b.Uploaded)
	}
	for _, data := range objects.data {
		raw, err := ungzipNug(data)
		if err != nil || len(raw) > nugBlockLimit {
			t.Fatal("invalid block budget", err)
		}
	}
	fresh := newNugBlocks(objects)
	objects.gets = 0
	rows, err := fresh.scan(ctx, root, ordinalKey(1500), 10)
	if err != nil || len(rows) != 10 || rows[0].Key != ordinalKey(1501) {
		t.Fatal("range lookup", rows, err)
	}
	if objects.gets > 5 {
		t.Fatal("range scanned whole index", objects.gets)
	}
	updated, err := fresh.set(ctx, root, ordinalKey(1505), encodeMessageJSON(map[string]int{"value": 99}))
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.flush(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if fresh.Uploaded > 4 {
		t.Fatal("single update rewrote whole tree", fresh.Uploaded)
	}
	old, err := newNugBlocks(objects).get(ctx, root, ordinalKey(1505))
	if err != nil || !bytes.Contains(old, []byte("1505")) {
		t.Fatal("old immutable snapshot mutated")
	}
	if _, err := fresh.set(ctx, root, "too-large", make([]byte, nugValueLimit+1)); err == nil {
		t.Fatal("oversized value accepted")
	}
}

func TestMessageFilteringStableRevisionsAndSharedContent(t *testing.T) {
	s, objects := memoryMessages(t)
	a := authoredFixture("a", 4)
	empty := a.Messages[0]
	empty.ID = "a:tool-only"
	empty.SourceID = "tool-only"
	empty.Parts = nil
	empty.ExcludedParts = []string{"tool"}
	a.Messages = append(a.Messages, empty)
	first := publishFixture(t, s, a)
	result, err := s.Query(context.Background(), "", MessageFilter{}, "")
	if err != nil || len(result.Messages) != 4 {
		t.Fatal("empty envelope retained", result, err)
	}
	a.Messages[0].ExcludedParts = []string{"tool", "tool"}
	a.Updated = 999
	a.Lineage = MessageLineage{Status: "inferred", Candidates: []string{"other"}, Evidence: "batch annotation"}
	a.VKCoverage = "changed_observation"
	if again := publishFixture(t, s, a); again.Changed || again.Snapshot != first.Snapshot {
		t.Fatal("diagnostics/lineage alone rewrote content")
	}
	b := authoredFixture("b", 4)
	publishFixture(t, s, b)
	root, _, _, _ := s.root(context.Background(), "", false)
	rows, err := newNugBlocks(objects).all(context.Background(), root.Content)
	if err != nil || len(rows) != 1 {
		t.Fatal("copied payload content was duplicated", len(rows), err)
	}
	query, err := s.Query(context.Background(), "", MessageFilter{}, "")
	if err != nil || len(query.Messages) != 8 {
		t.Fatal("sharing merged distinct occurrences", err)
	}
	batch := MessageBatch{Sessions: []MessageSession{a, b}}
	inferMessageCopiedHistory(&batch)
	alone := MessageBatch{Sessions: []MessageSession{a}}
	inferMessageCopiedHistory(&alone)
	if messageDigest(batch.Sessions[0].Lineage) != messageDigest(alone.Sessions[0].Lineage) {
		t.Fatal("lineage depends on batch")
	}
	if len(objects.data[nugRootKey]) > 4096 {
		t.Fatal("root exceeded budget")
	}
}

func TestMessagePagingCorrectionsLateImportsAndReplay(t *testing.T) {
	s, _ := memoryMessages(t)
	ctx := context.Background()
	initial := authoredFixture("one", 4)
	publishFixture(t, s, initial)
	page, err := s.Prepare(ctx, "continuation", MessageFilter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ack(ctx, "continuation", MessageFilter{}, page.Token); err == nil {
		t.Fatal("undelivered page ACKed")
	}
	delivered, err := s.ReadPage(ctx, "continuation", MessageFilter{}, page.Token)
	if err != nil || len(delivered.Messages) != 2 {
		t.Fatal("delivery", err)
	}
	correction := authoredFixture("one", 3)
	correction.BaseRevision = sessionBase(t, s, "one")
	correction.Updated = 2
	correction.Messages[1].Parts[0].Text = "corrected\n"
	publishFixture(t, s, correction, authoredFixture("late", 1))
	replay, err := s.ReadPage(ctx, "continuation", MessageFilter{}, page.Token)
	if err != nil || !bytes.Equal(encodeMessageJSON(delivered), encodeMessageJSON(replay)) {
		t.Fatal("pinned replay changed", err)
	}
	if _, err := s.Ack(ctx, "continuation", MessageFilter{}, page.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ack(ctx, "continuation", MessageFilter{}, page.Token); err != nil {
		t.Fatal("ACK retry", err)
	}
	seen := map[string]bool{}
	for {
		next, err := s.Prepare(ctx, "continuation", MessageFilter{}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if next.Token == "" {
			break
		}
		next, err = s.ReadPage(ctx, "continuation", MessageFilter{}, next.Token)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range next.Entries {
			seen[entry.ID+fmt.Sprint(entry.Deleted)] = true
		}
		if _, err := s.Ack(ctx, "continuation", MessageFilter{}, next.Token); err != nil {
			t.Fatal(err)
		}
	}
	if !seen["late:m0false"] || !seen["one:m1false"] || !seen["one:m3true"] {
		t.Fatal("missed late/corrected/tombstone", seen)
	}
	if _, err := s.Ack(ctx, "continuation", MessageFilter{}, page.Token); err == nil {
		t.Fatal("stale ACK accepted")
	}
	if _, err := s.ReadPage(ctx, "continuation", MessageFilter{}, page.Token); err != nil {
		t.Fatal("old token not replayable", err)
	}
	if _, err := s.Prepare(ctx, "continuation", MessageFilter{Role: "assistant"}, 2); err == nil {
		t.Fatal("reader silently changed filter")
	}
	if _, err := s.ReadPage(ctx, "continuation", MessageFilter{}, page.Token+"0"); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestMessageMissingResetCatalogAndBoundedReader(t *testing.T) {
	s, objects := memoryMessages(t)
	ctx := context.Background()
	session := authoredFixture("session", 130)
	publishFixture(t, s, session)
	var oldToken string
	for {
		page, err := s.Prepare(ctx, "reader", MessageFilter{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if page.Token == "" {
			break
		}
		if oldToken == "" {
			oldToken = page.Token
		}
		if _, err := s.ReadPage(ctx, "reader", MessageFilter{}, page.Token); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Ack(ctx, "reader", MessageFilter{}, page.Token); err != nil {
			t.Fatal(err)
		}
	}
	if len(objects.data["readers/reader.json"]) > 4096 {
		t.Fatal("reader grew with pages")
	}
	if _, err := s.ReadPage(ctx, "reader", MessageFilter{}, oldToken); err != nil {
		t.Fatal("bounded checkpoint lost replay", err)
	}
	objects.mu.Lock()
	delete(objects.data, nugRootKey)
	objects.mu.Unlock()
	if _, err := s.Prepare(ctx, "reader", MessageFilter{}, 1); err == nil {
		t.Fatal("missing root masqueraded as caught up")
	}
	if _, err := s.Publish(ctx, MessageBatch{Schema: 1, WorkspaceReference: messageTestReference, Sessions: []MessageSession{session}}); err == nil {
		t.Fatal("publisher silently recreated a missing established catalog")
	}
}

func TestMessageCASPartialFailureAndLargeExactText(t *testing.T) {
	s, objects := memoryMessages(t)
	ctx := context.Background()
	objects.beforeCAS = func(string) { publishFixture(t, s, authoredFixture("peer", 1)) }
	publishFixture(t, s, authoredFixture("local", 1))
	query, err := s.Query(ctx, "", MessageFilter{}, "")
	if err != nil || len(query.Messages) != 2 {
		t.Fatal("concurrent disjoint writer lost", err)
	}
	objects.failRoot = true
	r, err := s.Publish(ctx, MessageBatch{Schema: 1, WorkspaceReference: messageTestReference, Sessions: []MessageSession{authoredFixture("orphan", 1)}})
	if err == nil || r.CatalogPublished {
		t.Fatal("partial publish called success")
	}
	objects.failRoot = false
	publishFixture(t, s, authoredFixture("orphan", 1))
	large := authoredFixture("large", 1)
	large.Messages[0].Parts[0].Text = strings.Repeat("exact 🌏\n", 300000)
	publishFixture(t, s, large)
	read, err := s.Query(ctx, "", MessageFilter{Session: "large"}, "")
	if err != nil || read.Messages[0].Parts[0].Text != large.Messages[0].Parts[0].Text {
		t.Fatal("large text did not round-trip", err)
	}
	for key, data := range objects.data {
		if len(data) > nugObjectLimit {
			t.Fatal("writer exceeded reader budget", key)
		}
	}
	stale := authoredFixture("local", 1)
	stale.Messages[0].Parts[0].Text = "stale"
	if _, err := s.Publish(ctx, MessageBatch{Schema: 1, WorkspaceReference: messageTestReference, Sessions: []MessageSession{stale}}); err == nil {
		t.Fatal("stale source overwrote current")
	}
}

func TestMessagesRejectGitAndLegacyLayout(t *testing.T) {
	for _, p := range []MessageTarget{{Payload: "git", Catalog: "git", Remote: "https://example.invalid/never-contact.git"}, {Payload: "s3", Catalog: "git"}, {Payload: "s3", Catalog: "s3", Checkpoint: "git"}, {Payload: "s3", Catalog: "s3", Endpoint: "http://127.0.0.1:1", Bucket: "test"}} {
		if _, err := NewMessageStore(messageTestReference, "fixture", "test", t.TempDir(), p); err == nil {
			t.Fatal("unsafe/legacy route allowed", p)
		}
	}
}

func actualNugProfile(t *testing.T) MessageTarget {
	t.Helper()
	credentials := os.Getenv("NUGS_TEST_S3_CREDENTIALS_FILE")
	if credentials == "" {
		t.Skip("set NUGS_TEST_S3_CREDENTIALS_FILE for actual MinIO acceptance")
	}
	return MessageTarget{Payload: "s3", Catalog: "s3", Layout: "workspace-v2", Endpoint: os.Getenv("NUGS_TEST_S3_ENDPOINT"), Bucket: os.Getenv("NUGS_TEST_S3_BUCKET"), Region: "us-east-1", Prefix: "nugs-v2-tests/" + messageHash([]byte(t.TempDir())), CredentialsFile: credentials, Checkpoint: "catalog"}
}
func TestMessagesActualS3PortableCheckpointAndNamespace(t *testing.T) {
	p := actualNugProfile(t)
	s, err := NewMessageStore(messageTestReference, "fixture", "first-alias", t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	publishFixture(t, s, authoredFixture("actual", 200))
	ctx := context.Background()
	page, err := s.Prepare(ctx, "ci", MessageFilter{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadPage(ctx, "ci", MessageFilter{}, page.Token); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewMessageStore(messageTestReference, "fixture", "another-alias", t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Ack(ctx, "ci", MessageFilter{}, page.Token); err != nil {
		t.Fatal("fresh client lost reader", err)
	}
	if !strings.HasSuffix(s.catalog.(messageS3Objects).prefix, "/workspaces/"+messageTestReference+"/messages/") {
		t.Fatal("namespace not workspace-first")
	}
	result, err := fresh.Query(ctx, "", MessageFilter{}, "")
	if err != nil || len(result.Messages) != 200 {
		t.Fatal("actual source roundtrip", err)
	}
	t.Logf("snapshot=%s messages=%d", result.Snapshot, len(result.Messages))
}

func TestMessagesActualConcurrentWriters(t *testing.T) {
	p := actualNugProfile(t)
	p.Remote = "https://example.invalid/must-not-be-contacted.git"
	s, err := NewMessageStore(messageTestReference, "fixture", "minio", t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	publishFixture(t, s)
	start := make(chan struct{})
	errorsOut := make(chan error, 2)
	for _, id := range []string{"peer-a", "peer-b"} {
		go func(id string) {
			<-start
			_, err := s.Publish(ctx, MessageBatch{Schema: 1, WorkspaceReference: messageTestReference, Sessions: []MessageSession{authoredFixture(id, 40)}})
			errorsOut <- err
		}(id)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errorsOut; err != nil {
			t.Fatal("disjoint writer", err)
		}
	}
	verified, err := s.Verify(ctx, "", MessageFilter{})
	if err != nil || verified.MessageCount != 80 {
		t.Fatal("concurrent source lost", verified, err)
	}
	base := sessionBase(t, s, "peer-a")
	start = make(chan struct{})
	for _, text := range []string{"correction-a", "correction-b"} {
		go func(text string) {
			input := authoredFixture("peer-a", 40)
			input.BaseRevision = base
			input.Updated = 2
			input.Messages[0].Parts[0].Text = text
			<-start
			_, err := s.Publish(ctx, MessageBatch{Schema: 1, WorkspaceReference: messageTestReference, Sessions: []MessageSession{input}})
			errorsOut <- err
		}(text)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		err := <-errorsOut
		if err == nil {
			success++
		} else {
			var op *OperationError
			if !errors.As(err, &op) || op.Code != "conflict" {
				t.Fatal("unexpected conflict result", err)
			}
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("competing correction chose silent winner", success, conflict)
	}
}

func TestNugSkewedValuesRespectByteBudget(t *testing.T) {
	objects := &memoryMessageObjects{data: map[string][]byte{}}
	b := newNugBlocks(objects)
	root := ""
	ctx := context.Background()
	for i := 0; i < 70; i++ {
		size := 10
		if i >= 60 {
			size = nugValueLimit - 2
		}
		value := []byte(`"` + strings.Repeat("x", size) + `"`)
		var err error
		root, err = b.set(ctx, root, ordinalKey(uint64(i)), value)
		if err != nil {
			t.Fatal("valid skewed row rejected", i, err)
		}
	}
	if err := b.flush(ctx, root); err != nil {
		t.Fatal(err)
	}
	rows, err := newNugBlocks(objects).all(ctx, root)
	if err != nil || len(rows) != 70 {
		t.Fatal("skewed rows lost", len(rows), err)
	}
}

func TestOpenCodeReadOnlyScopeOriginAndCoverage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "member"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE session(id TEXT PRIMARY KEY,parent_id TEXT,directory TEXT,version TEXT,time_updated INTEGER)", "CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT,data TEXT,time_created INTEGER,time_updated INTEGER)", "CREATE TABLE part(id TEXT PRIMARY KEY,message_id TEXT,session_id TEXT,data TEXT,time_created INTEGER,time_updated INTEGER)", "CREATE INDEX part_message_id_id_idx ON part(message_id,id)", "CREATE INDEX part_session_idx ON part(session_id)"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []struct{ id, dir, parent string }{{"one", root, ""}, {"fork", filepath.Join(root, "member"), "one"}, {"outside", root + "-other", ""}, {"historical", "/old/root", ""}} {
		if _, err := db.Exec("INSERT INTO session VALUES(?,?,?,?,?)", s.id, s.parent, s.dir, "1.19.4", 1); err != nil {
			t.Fatal(err)
		}
		for i, role := range []string{"user", "assistant", "assistant"} {
			id := s.id + fmt.Sprint(i)
			data := map[string]any{"role": role, "summary": map[string]any{"diffs": []string{}}, "time": map[string]int{"created": i}}
			if _, err := db.Exec("INSERT INTO message VALUES(?,?,?,?,?)", id, s.id, string(encodeMessageJSON(data)), i, 1); err != nil {
				t.Fatal(err)
			}
			parts := []map[string]any{{"type": "text", "text": "same exact text\n"}, {"type": "tool", "text": "TOOL SECRET"}, {"type": "reasoning", "text": "REASONING SECRET"}, {"type": "text", "text": "commentary\n", "synthetic": true}, {"type": "file", "mime": "image/png", "filename": "image.png", "url": "data:image/png;base64,ATTACHMENT BYTES"}}
			if i == 2 {
				parts = parts[1:3]
			}
			for j, p := range parts {
				if _, err := db.Exec("INSERT INTO part VALUES(?,?,?,?,?,?)", id+fmt.Sprint(j), id, s.id, string(encodeMessageJSON(p)), j, 1); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	source := OpenCodeMessageSource{Database: file, Version: "1.19.4", Root: root, Members: []string{"member"}, Aliases: []string{"/old/root"}}
	batch, err := ScanOpenCodeMessages(ctx, messageTestReference, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Sessions) != 3 {
		t.Fatal("scope", len(batch.Sessions))
	}
	encoded := string(encodeMessageJSON(batch))
	for _, forbidden := range []string{"TOOL SECRET", "REASONING SECRET", "ATTACHMENT BYTES", "excluded_parts"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatal("excluded envelope leaked", forbidden)
		}
	}
	for _, s := range batch.Sessions {
		if len(s.Messages) != 2 || len(s.Messages[1].Parts) != 3 || s.Messages[1].Parts[1].Origin != "synthetic" {
			t.Fatal("capture/origins", s)
		}
		if s.ID == "opencode:fork" && s.Lineage.Status != "confirmed" {
			t.Fatal("native parent lost")
		}
	}
	source.Session = "outside"
	if _, err := ScanOpenCodeMessages(ctx, messageTestReference, source); err == nil {
		t.Fatal("outside source selected")
	}
	source.Session = ""
	source.Version = "future"
	if _, err := ScanOpenCodeMessages(ctx, messageTestReference, source); err == nil {
		t.Fatal("unknown version accepted")
	}
	source.Version = "1.19.4"
	if _, err := db.Exec("UPDATE part SET data='broken {' WHERE id='one00'"); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanOpenCodeMessages(ctx, messageTestReference, source); err == nil {
		t.Fatal("corrupt source called complete")
	}
}
