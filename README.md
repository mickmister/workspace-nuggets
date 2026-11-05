# Workspace nuggets (`nugs`)

A standalone Go CLI and MCP server for workspace-scoped agent context.

## Current first slice

Git-only storage, initially for **Beads, documents, and an OpenCode adapter**.
The CLI and stdio MCP server now share Go operations for workspace document
browsing, reading and version-checked upserts. Native Beads integration and the
OpenCode conversation adapter are next; their namespaces below are reserved.

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

Automatic sync reconciles clean native merges only, returns real conflicts,
and uses private Git access controls for v1.

## Use the document CLI

Requires Go 1.25+ and Git 2.39+ (including `merge-tree --write-tree`).

```sh
go install ./cmd/nugs

# Select a data repository and a stable workspace ID explicitly.
nugs workspace inspect --remote "$REMOTE" --workspace feature-123
nugs documents list --remote "$REMOTE" --workspace feature-123

# Create only; reads exact bytes from a file, or stdin when --file is omitted.
nugs documents upsert --remote "$REMOTE" --workspace feature-123 \
  --name reviews/design.md --file ./design.md

nugs documents read --remote "$REMOTE" --workspace feature-123 \
  --name reviews/design.md

# Update using the version returned by read/list/upsert.
nugs documents upsert --remote "$REMOTE" --workspace feature-123 \
  --name reviews/design.md --expected "$VERSION" --file ./design.md
```

All commands return JSON. Read results contain `content_base64` for exact bytes,
and `text` when the document is valid UTF-8. Resource versions are Git blob OIDs;
receipts also identify the published commit, SHA-256, byte length and inferred
media type. An unchanged upsert with the current version creates no new commit.
Omitting `--expected` means **create only**, not unconditional overwrite.

Workspace IDs accept letters, digits, hyphens and underscores. Use a durable
workspace identity rather than assuming a branch name uniquely identifies all
repositories in a workspace. Document names are safe relative paths.

## Use from an MCP client

Launch `nugs mcp --remote "$REMOTE"` as a stdio MCP server. The remote is bound at
launch; every tool request selects its workspace explicitly. Available tools:

- `workspace_inspect`: `{ "workspace": "feature-123" }` — current document view.
- `documents_list`: same input; returns resource URIs and versions.
- `document_read`: add `"name": "reviews/design.md"`.
- `document_upsert`: add `"name"`, `"expected_version"` (empty for create only),
  and exactly one of `"text"` or `"content_base64"`.

The server also advertises a `nugs://workspace/{workspace}/documents/{name}`
resource template. Use the URI returned by listing rather than manually escaping
names. Tool errors set MCP `isError` and retain structured error/conflict data.
Protocol stdout contains only MCP messages.

## Reconciliation and recovery

Each operation uses an isolated bare repository; it never checks out or modifies
your source branch. Documents live as regular blobs under `documents/<name>` in
`refs/documents/<workspace>`. Remote publication uses normal Git pushes, with at
most three attempts and native clean three-way merges between raced attempts.
Same-document ambiguous edits, concurrent conflicting creation and binary
conflicts stop publication. Stale expected versions are rejected before pushing.

Upserts retain their input as a candidate commit before the first network request.
On a stale version, conflict, failed fetch or failed push, the JSON error includes
`local_repository`, local/base/
remote commits and expected/actual versions. `base_commit` is the fetched snapshot
used to construct the candidate; on a stale request it is not the caller's older
observed snapshot. Failed transactions remain in the local user cache under
`nugs/transactions`, or the directory supplied with `--cache`.

Inspect the retained evidence using the paths from the error:

```sh
git --git-dir "$LOCAL_REPOSITORY" show refs/nugs/intent:documents/reviews/design.md
git --git-dir "$LOCAL_REPOSITORY" show refs/nugs/remote:documents/reviews/design.md
```

The remote evidence ref is present when a remote document commit was available.
Resolution/publication is explicit; `nugs` does not invoke a model to choose a
winner. CLI exit codes: `0` success, `1` operation failure, `2` usage error,
`3` version/content conflict. Retained failed transactions are not auto-deleted.

## Verify the implementation

```sh
go test ./...
go test -race ./...
go vet ./...
NUGS_TEST_DOLT=1 go test -v -run TestNativeDoltGitRefs ./...
```

Document integration tests use an ephemeral loopback smart-HTTP Git server with
Git's actual `git-http-backend`, temporary repositories and automatic teardown.
They exercise real operations, the built CLI and a real stdio MCP session:
cross-client exact bytes, workspace isolation, idempotency, stale versions,
concurrent creation, independent and same-file clean merges, real conflicts,
retry exhaustion, rejected publication and retained recovery evidence. The server
has no authentication because it contains only isolated synthetic test data.

The optional Dolt experiment uses synthetic tables and disposable local remotes.
It verifies native custom-ref transport, independent merges and same-row conflicts,
not complete Beads table/history/working-set fidelity or external host support.
These tests never open a live Beads DB, push an external remote, start Docker or
modify global Git/Dolt settings. Native Dolt requires an initial ordinary Git
branch/commit; the document backend does not bootstrap an unrelated source branch.

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
