package nuggets

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var (
	nativeHTTPAuth        = regexp.MustCompile(`requested url returned error: (401|403)\b`)
	nativeHTTPUnavailable = regexp.MustCompile(`requested url returned error: 5[0-9][0-9]\b`)
	nativeNonFastForward  = regexp.MustCompile(`(?m)^\s*!\s+\[rejected\]\s+\S+\s+->\s+\S+\s+\(non-fast-forward\)\s*$`)
)

// classifyBeadsPushError recognizes native diagnostics, not general advice or
// every possible backend error. Unknown failures must never authorize a merge.
// Beads currently appends credential hints even to HTTP 503 failures.
func classifyBeadsPushError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var diagnostics []string
	for _, line := range strings.Split(strings.ToLower(err.Error()), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "hint:") {
			diagnostics = append(diagnostics, line)
		}
	}
	message := strings.Join(diagnostics, "\n")
	if nativeHTTPAuth.MatchString(message) ||
		strings.Contains(message, "authentication failed") ||
		strings.Contains(message, "permission denied (publickey") ||
		(strings.Contains(message, "could not read username for") && strings.Contains(message, "terminal prompts disabled")) ||
		(strings.Contains(message, "could not read password for") && strings.Contains(message, "terminal prompts disabled")) {
		return "authentication_failed"
	}
	if nativeHTTPUnavailable.MatchString(message) {
		return "remote_unavailable"
	}
	for _, diagnostic := range []string{
		"could not resolve host:", "could not resolve hostname ",
		"failed to connect to ", "connection refused", "connection timed out", "operation timed out",
		"connection reset by peer", "remote end hung up unexpectedly",
		"empty reply from server", "unexpected disconnect",
	} {
		if strings.Contains(message, diagnostic) {
			return "remote_unavailable"
		}
	}
	if nativeNonFastForward.MatchString(message) {
		return "push_diverged"
	}
	return "push_failed"
}
