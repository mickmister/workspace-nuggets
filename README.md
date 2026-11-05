# Workspace nuggets (`nugs`)

A standalone Go CLI and MCP server for workspace-scoped agent context.

## Current first slice

Git-only storage, initially for **Beads, documents, and an OpenCode adapter**.
Both the eventual CLI and stdio MCP server will call the same Go operations.
Browsing workspace resources and upserting documents from an agent should be
straightforward and use explicit version/conflict receipts.

| Kind | Remote ref | Transport |
| --- | --- | --- |
| Beads | `refs/dolt/<workspace>` | Native Dolt Git remote, configured with `--ref` |
| Documents | `refs/documents/<workspace>` | Git objects/commits |
| Conversations | `refs/conversations/<workspace>` | Agent-neutral records; OpenCode export adapter first |
| Built assets (later) | `refs/assets/<workspace>` | Separate artifact storage policy |
| OTel (later) | Not part of Git-first work | Proposed default S3; user-configurable |

Example workspace ref suffix: `feature-123`. Validate suffixes and carry stable
workspace/repository provenance; a bare branch name does not identify an entire
multi-repo workspace. Custom refs need explicit fetch refspecs; ordinary clone
does not fetch them. Support for a ref namespace must be tested on the chosen
Git host, not assumed from this local transport experiment.

This initial repository contains the transport experiment and decisions below;
it is **not yet a working CLI/MCP server**. The sync/confidentiality policies are
settled: automatically reconcile clean native merges only, return real conflicts,
and use private Git access controls for v1. No executable named `nugs` is claimed yet.

## Verify the Git/Dolt transport boundary

```sh
go test ./...
NUGS_TEST_DOLT=1 go test -v -run TestNativeDoltGitRefs ./...
```

The experiment uses disposable local bare Git repositories and synthetic Dolt
tables. It never opens a live workspace Beads DB, pushes an external remote, starts
Docker, or modifies global Git/Dolt settings. It checks that native Dolt can use
the requested custom ref while documents occupy a separate Git ref, then exercises
two independent clients and real same-row conflicts. This proves transport
semantics, not complete Beads table/history/working-set fidelity or host support.

## OpenCode adapter boundary

Use the supported `opencode export <session-id>` interface, optionally consuming
an already exported file. Require an explicit session; do not scrape its private
SQLite schema or use an interactive "latest session" picker. The inspected export
shape is `{ info, messages: [{ info, parts }] }`. Preserve native IDs/order and
unknown parts as provenance alongside normalized prompts/responses, and label
truncated/incomplete sources honestly. Export sanitization is an explicit content
policy, not a claim that every native exporter has already redacted data.

## Decisions and outstanding questions

See [DESIGN.md](DESIGN.md). Implementation tracking lives in the enclosing
workspace's Beads DB, not a newly initialized repository-local DB.
