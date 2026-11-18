package nuggets

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestNativePushFailureClassification(t *testing.T) {
	hints := "\nhint: configure git credentials; authentication failed\nhint: connection refused\nhint: non-fast-forward"
	for _, test := range []struct {
		name, message, want string
	}{
		{"http auth", "fatal: unable to access '<storage>': The requested URL returned error: 403", "authentication_failed"},
		{"missing credentials", "fatal: could not read Username for '<storage>': terminal prompts disabled", "authentication_failed"},
		{"ssh auth", "git@host: Permission denied (publickey).", "authentication_failed"},
		{"outage ignores auth hints", "fatal: unable to access '<storage>': The requested URL returned error: 503" + hints, "remote_unavailable"},
		{"lost acknowledgement", "fatal: the remote end hung up unexpectedly" + hints, "remote_unavailable"},
		{"dns", "fatal: Could not resolve host: storage.invalid", "remote_unavailable"},
		{"transport timeout", "fatal: Operation timed out after 1000 milliseconds" + hints, "remote_unavailable"},
		{"diverged", "To <storage>\n ! [rejected] main -> main (non-fast-forward)\nerror: failed to push some refs" + hints, "push_diverged"},
		{"mixed transport and rejection", " ! [rejected] main -> main (non-fast-forward)\nfatal: The requested URL returned error: 503", "remote_unavailable"},
		{"remote hook rejection", " ! [remote rejected] main -> main (pre-receive hook declined)" + hints, "push_failed"},
		{"generic rejection", "push rejected" + hints, "push_failed"},
		{"unknown http", "fatal: The requested URL returned error: 418" + hints, "push_failed"},
		{"local filesystem", "open database file: permission denied" + hints, "push_failed"},
		{"divergence advice only", "retry after pull; non-fast-forward may be the cause" + hints, "push_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyBeadsPushError(errors.New(test.message)); got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		err  error
		want string
	}{{context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"}} {
		if got := classifyBeadsPushError(fmt.Errorf("native push: %w", test.err)); got != test.want {
			t.Errorf("wrapped %v classified as %s", test.err, got)
		}
	}
}
