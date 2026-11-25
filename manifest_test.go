package nuggets

import (
	"encoding/json"
	"testing"
)

func TestStandaloneManifestPurpose(t *testing.T) {
	for _, repositories := range []string{"", `,"repositories":[]`, `,"repositories":null`} {
		for _, purpose := range []string{"", " ", "\n\t"} {
			encoded, _ := json.Marshal(purpose)
			content := []byte(`{"schema_version":1,"workspace":"planning","observed_at":"2026-11-09T00:00:00Z","purpose":` + string(encoded) + repositories + `}`)
			if _, err := parseManifest(content, "planning"); err == nil {
				t.Fatalf("blank standalone purpose accepted: %s", content)
			}
		}
		content := []byte(`{"schema_version":1,"workspace":"planning","observed_at":"2026-11-09T00:00:00Z","purpose":"Evaluate design alternatives before choosing repositories"` + repositories + `}`)
		manifest, err := parseManifest(content, "planning")
		if err != nil || manifest.Purpose == "" || len(manifest.Repositories) != 0 {
			t.Fatalf("standalone manifest rejected: %+v, %v", manifest, err)
		}
	}
}

func TestBranchManifestDoesNotNeedObservedCommit(t *testing.T) {
	base := `{"schema_version":1,"workspace":"branch","observed_at":"2026-11-25T00:00:00Z","repositories":[`
	for _, entry := range []string{
		`{"path":"app","branch":"refs/heads/main","publication":"no_remote"}`,
		`{"path":"app","branch":"refs/heads/main","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publication":"no_remote"}`,
		`{"path":"app","detached":true,"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publication":"no_remote"}`,
	} {
		if _, err := parseManifest([]byte(base+entry+`]}`), "branch"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := parseManifest([]byte(base+`{"path":"app","detached":true,"publication":"no_remote"}]}`), "branch"); err == nil {
		t.Fatal("detached source without an explicit revision accepted")
	}
}
