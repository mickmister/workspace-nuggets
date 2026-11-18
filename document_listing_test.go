package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentPagesRetainSnapshotAcrossWritesAndBindSelection(t *testing.T) {
	g := newGitServer(t)
	ctx := context.Background()
	s := g.store("paged-documents")
	for i := 0; i < 23; i++ {
		if _, err := s.Upsert(ctx, fmt.Sprintf("threads/task/page-%02d.md", i), "", []byte("body")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Upsert(ctx, "archive/old.md", "", []byte("not selected")); err != nil {
		t.Fatal(err)
	}
	rows, first, err := s.ListPage(ctx, "threads/task/", "", "", 0, false)
	if err != nil || len(rows) != 20 || first.Total != 23 || first.NextToken == "" || first.Snapshot == "" {
		t.Fatalf("first page: %d %+v %v", len(rows), first, err)
	}
	if _, err := s.Upsert(ctx, "threads/task/page-00-new.md", "", []byte("arrived after page one")); err != nil {
		t.Fatal(err)
	}
	last, second, err := s.ListPage(ctx, "", "", first.NextToken, 20, false)
	if err != nil || len(last) != 3 || second.Total != 23 || second.Snapshot != first.Snapshot || second.NextToken != "" {
		t.Fatalf("continuation: %d %+v %v", len(last), second, err)
	}
	if last[0].Name != "threads/task/page-20.md" || last[2].Name != "threads/task/page-22.md" {
		t.Fatalf("skipped or duplicated rows: %+v", last)
	}
	all, page, err := s.ListPage(ctx, "threads/task/", "", "", 0, true)
	if err != nil || len(all) != 24 || page.NextToken != "" {
		t.Fatalf("explicit inventory: %d %+v %v", len(all), page, err)
	}
	for _, req := range []struct {
		prefix, snapshot, token string
		limit                   int
		all                     bool
	}{
		{prefix: "archive/", token: first.NextToken},
		{snapshot: page.Snapshot, token: first.NextToken},
		{token: "broken"}, {limit: -1}, {limit: 101},
		{prefix: "../"}, {token: first.NextToken, all: true},
	} {
		if _, _, err := s.ListPage(ctx, req.prefix, req.snapshot, req.token, req.limit, req.all); err == nil {
			t.Fatalf("invalid page accepted: %+v", req)
		}
	}
	other := g.store("other-workspace")
	if _, _, err := other.ListPage(ctx, "", "", first.NextToken, 20, false); err == nil {
		t.Fatal("page token crossed workspace boundary")
	}
	response := (Service{Remote: g.url, DefaultWorkspace: "paged-documents", CacheDir: s.CacheDir}).Execute(ctx, Request{Operation: "documents.list"})
	if response.Error != nil || len(response.Resources) != 20 || response.Page == nil || response.Page.NextToken == "" {
		t.Fatalf("service did not bound discovery: %+v", response)
	}
	encoded, _ := json.Marshal(response)
	if bytes.Contains(encoded, []byte("content_base64")) || bytes.Contains(encoded, []byte(`"text":`)) {
		t.Fatal("listing included document bodies")
	}
	var stdout, stderr bytes.Buffer
	code := RunCLI(ctx, []string{"documents", "list", "--remote", g.url, "--workspace", "paged-documents",
		"--prefix", "threads/task/", "--limit", "2"}, strings.NewReader(""), &stdout, &stderr)
	var listed Response
	if code != 0 || json.Unmarshal(stdout.Bytes(), &listed) != nil || len(listed.Resources) != 2 || listed.Page == nil ||
		listed.Page.Total != 24 || listed.Page.NextToken == "" || bytes.Count(stdout.Bytes(), []byte("\n")) < 5 {
		t.Fatalf("CLI page/format contract: exit=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}

func TestSubcommandHelpIsSuccessfulWithoutWorkspaceOrConfiguration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{
		{"documents", "--help"}, {"documents", "list", "--help"},
		{"documents", "read", "-h"}, {"runs", "--help"}, {"beads", "sync", "--help"},
		{"documents", "list", "--config", filepath.Join(t.TempDir(), "missing.json"), "--help"},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunCLI(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Usage:") {
			t.Fatalf("help %v: exit=%d stdout=%s stderr=%s", args, code, &stdout, &stderr)
		}
	}
}
