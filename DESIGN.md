# Nugs: Git-first design record

## Confirmed direction

The submitted form on `task-3vt.11` establishes:

- Product name **workspace nuggets**, executable shorthand **nugs**.
- Standalone Go CLI and ergonomic stdio MCP server, sharing operations.
- All implementation belongs in this repository, `../workspace-nuggets`, not the
  VD application. VD/VK is optional source integration, never the core schema.
- Start with Beads, documents and the OpenCode adapter; source-neutral conversations
  remain a first-class format.
- Git first, with per-workspace refs for Dolt, documents and conversations; assets
  have their own future namespace.
- Prefer native `dolt push/pull` for Beads, not JSONL replacement sync. Rclone is a
  candidate regular-file/S3 backend later; it is not a Git merge mechanism.
- Leave OTel out of this slice. Its future default backend is S3, with configurable
  content policy and enough evidence to debug/improve/validate.
- Consider per-kind backend selection and TTL cleanup later. A Docker/MinIO test
  was requested before the more specific Git-only direction; it is deferred rather
  than treated as an initial dependency.

The earlier Go/S3 specs in the VD repository are historical planning inputs. The
Git-first decisions here supersede their remote layout and publication assumptions.
Do not retain a mandatory S3 catalog, Node runtime or VK schema in the Go core.

## Local transport evidence

Installed Dolt advertises `dolt remote add --ref <git-ref>` and
`dolt clone --ref <git-ref>`. Its Git remote URLs use `git+file://`, `git+https://`
or `git+ssh://` as appropriate. This allows the proposed `refs/dolt/feature-123`
through native Dolt, rather than wrapping a copied database in our own backup.
The integration test checks the actual installed binary; help text alone is not
proof. Compatible Dolt version/capability must be a documented prerequisite.
The first native push experiment rejected a completely empty bare Git remote:
Dolt requires an ordinary branch/initial commit before creating the custom Dolt
ref. Fixtures now model that prerequisite; product bootstrap must explain it or
explicitly initialize a selected empty data repository, never modify an unrelated
code branch implicitly.

Beads' configured redirect and DB lifecycle remain owned by Beads. A production
adapter must resolve a selected workspace DB through supported Beads/Dolt context,
flush/commit through that interface, and avoid opening a live engine directory
with a conflicting second writer. A bare Dolt fixture does not test this lifecycle.

The initial native setup is now implemented in `beads.go` as `beads configure` /
`beads inspect` with matching MCP tools. It follows the outer workspace-root
redirect through `bd where`, validates embedded metadata/collection identity and
database layout, and sets native origin with `params.git_ref` using Dolt's
supported remote command. It declares `sync.remote` and enables explicit push via
`bd config`, preserving existing source-Git and auto-push policy. Setup does not
initialize a repo database, publish tasks, or bootstrap a storage branch.
Different existing URL/ref bindings are conflicts. The init lock coordinates
setup/init only; normal command concurrency belongs to the native driver.
Actual embedded Beads fixtures now exercise configure, issue/history preservation,
native push and custom-ref recovery, beyond the original synthetic Dolt table test.
Server/proxied setup and general synchronization orchestration remain follow-ups.

## Shared core and MCP ergonomics

The initial document implementation is in `documents.go`, with shared dispatch in
`service.go`, CLI handling in `cli.go`, and SDK-backed stdio MCP in `mcp.go`.
`cmd/nugs` is the executable entrypoint. Document trees contain regular blobs at
`documents/<safe-relative-name>`; blob OIDs are per-resource content versions and
commit OIDs identify workspace document snapshots. Each request uses a separate
bare transaction repository. Success removes it; failed publication or stale
upserts retain the local intent ref and available remote evidence for inspection.
Current operations cover workspace document inspection, list/read/upsert and MCP
resource reads. Media type is inferred from the name; richer per-run provenance
and other kinds remain subsequent schema/adapter work.

`manifest.go` now implements initial multi-repository branch-pointer bindings.
`workspace provision` (also `workspace_provision` in MCP) observes explicitly
selected local repository roots and publishes schema-v1 `workspace/manifest.json`
through the document backend. It captures portable membership paths, branch refs,
commit OIDs, dirty/detached state, sanitized fetch remotes and explicit unverified/
missing publication status. `workspace inspect` reads this manifest and documents
from one fetched snapshot. Reserved manifest contents are validated before each
push, including clean reconciliations. Source repositories are only observed;
there is no source push, Git-object upload, checkout or restore. PR/base-ref
relationships and actual remote-object availability checks remain future work.

Machine defaults now live at `os.UserConfigDir()/nugs/config.json` (normally
`~/.config/nugs/config.json` on Linux), managed by `nugs config set/show`, with
explicit `--config`, `--remote` and `--cache` overrides. Repository-local files
are not auto-discovered. Workspace defaults to a readable current-branch slug;
the full source ref remains in each repository pointer. Multi-repo root inference
requires a common branch among explicitly selected repositories. Detached or
ambiguous cases require an explicit workspace ID. CLI and MCP use the same default
workspace through Service; existing UUID identifiers are still supported.

Common operations should expose explicit workspace identity, kind, ref and version:

- Workspace inspection and local bindings, including all repo memberships.
- Workspace-scoped resource browsing across the supported kinds, with stable
  resource identities and actionable version/conflict receipts for agents.
- Document list/read/upsert with the expected prior version, immutable commits,
  useful diff/review context and a new-version receipt.
- Conversation import/list/read with an initial OpenCode exporter adapter.
- Beads inspect/sync through native capabilities, with status/conflict receipts.
- Fetch/reconcile/push with bounded retry and per-kind outcome; a partial publish
  is not reported as atomic success across all custom refs.

CLI and MCP directly call these operations; MCP does not shell out to `nugs`.
External `git`, `dolt`, `bd` and `opencode` are versioned storage/source adapters.
No ambient agent instruction or document text controls which ref/command executes.
Commands use argument arrays and explicit workspace context, never shell interpolation.

Upserts create a missing resource or update an existing resource under its stable
identity. Creation must detect concurrent creation; updates require the observed
version so a stale agent cannot silently overwrite another edit. Repeating an
unchanged upsert should not create duplicate resources. CLI and MCP expose the
same semantics, including explicit workspace selection and inspectable failures.

## Accepted "fix before push"

The response to `nugs_git_reconcile_policy_v1` on `task-3vt.20` selects
`native_clean_only`: safe deterministic repair, **not an automatic content winner**.

1. Persist the user's local intended changes/version before network activity.
2. Fetch the selected remote ref and compare its ancestry and logical versions.
3. Fast-forward or three-way merge independent changes through the native engine.
   Distinct document changes are different from two edits to the same paragraph;
   a Dolt row merge is different from importing a JSON dump.
4. Explicitly inspect conflicts, schema conflicts and constraint violations, not
   merely command exit code. Never publish unresolved state.
5. If clean, commit the reconciliation and push normally; on another remote
   advancement, re-fetch/reconcile with a small bounded retry budget.
6. Surface a structured conflict with base/local/remote versions and useful
   document/row evidence when automatic repair cannot preserve intent. Keep local
   work and remote history; no force push, blanket ours/theirs or wall-clock winner.

An agent can inspect that conflict through CLI/MCP. Automatic synchronization stops
at real conflicts; resolution and subsequent publication are explicit operations.
The tool does not invoke an agent to choose conflicting content or publish a winner.
Git non-fast-forward rejection is not permission to rewrite remote history.
OpenCode conversation identity should prevent duplicate ingestion while preserving
legitimate repeated prompts, branches/retries and corrections.

## Accepted Git confidentiality

The same response selects `private_git_v1`: authenticated private Git access
controls are sufficient for v1 Beads, documents and conversations. This explicitly
supersedes the earlier generated-key client-side encryption requirement for these
Git-backed components. Repository readers can read their content/history; custom
refs are organizational namespaces, not separate confidentiality boundaries.
Future S3/OTel confidentiality remains a separate backend decision.

The user also permits code/file changes and emphasizes ergonomic workspace-scoped
CLI/MCP resource browsing, including upserts. These two policy decisions no longer
block Git-first implementation.

## Remaining decisions

**TTL semantics:** Deleting a ref is not guaranteed erasure: Git objects can remain
in other refs/reflogs, provider retention and clones. Deleting active Dolt history
also needs a live-workspace/last-good policy. Record desired windows before a
cleanup command/cron; no background deletion is enabled by this experiment.

## Deferred experiments

MinIO in Docker, rclone S3 round trips, OTel capture/query, assets/large-object
retention, selectable per-kind backends and automated TTL cleanup are separate
tracked work. Validate the small Git-first slice and conflict semantics first.
