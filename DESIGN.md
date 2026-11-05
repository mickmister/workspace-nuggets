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

## Shared core and MCP ergonomics

Common operations should expose explicit workspace identity, kind, ref and version:

- Workspace inspection and local bindings, including all repo memberships.
- Document list/read/update with the expected prior version, immutable commits,
  useful diff/review context and a new-version receipt.
- Conversation import/list/read with an initial OpenCode exporter adapter.
- Beads inspect/sync through native capabilities, with status/conflict receipts.
- Fetch/reconcile/push with bounded retry and per-kind outcome; a partial publish
  is not reported as atomic success across all custom refs.

CLI and MCP directly call these operations; MCP does not shell out to `nugs`.
External `git`, `dolt`, `bd` and `opencode` are versioned storage/source adapters.
No ambient agent instruction or document text controls which ref/command executes.
Commands use argument arrays and explicit workspace context, never shell interpolation.

## Proposed "fix before push"

Default proposal: safe deterministic repair, **not an automatic content winner**.

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

An agent can inspect that conflict through MCP and propose an explicit resolution.
Whether it may autonomously edit/resolve and publish is still a user decision.
Git non-fast-forward rejection is not permission to rewrite remote history.
OpenCode conversation identity should prevent duplicate ingestion while preserving
legitimate repeated prompts, branches/retries and corrections.

## Remaining decisions

**Conflict authority:** Should repair stop at deterministic native merges and
return conflicts, or may a tool-invoking agent resolve conflicting content and
publish? A tool must not claim a conflict is resolved because a model chose text.

**Git data confidentiality:** Earlier planning required generated-key client-side
encryption. Native Dolt-in-Git plus directly mergeable text refs does not by itself
implement that. Decide private Git access controls for v1 versus retaining a
client-side encrypted transport/envelope. Do not claim credentials/private repo
access equals shared-key encryption. Synthetic local tests contain no private data.

**TTL semantics:** Deleting a ref is not guaranteed erasure: Git objects can remain
in other refs/reflogs, provider retention and clones. Deleting active Dolt history
also needs a live-workspace/last-good policy. Record desired windows before a
cleanup command/cron; no background deletion is enabled by this experiment.

## Deferred experiments

MinIO in Docker, rclone S3 round trips, OTel capture/query, assets/large-object
retention, selectable per-kind backends and automated TTL cleanup are separate
tracked work. Validate the small Git-first slice and conflict semantics first.
