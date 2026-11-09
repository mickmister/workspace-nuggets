# Nugs: Git-first design record

## Confirmed direction

The submitted form on `task-3vt.11` establishes the initial direction, with later
scope refinements noted below:

- Product name **workspace nuggets**, executable shorthand **nugs**.
- Standalone Go CLI and ergonomic stdio MCP server, sharing operations.
- All implementation belongs in this repository, `../workspace-nuggets`, not the
  VD application. VD/VK is optional source integration, never the core schema.
- Start with Beads, documents and the OpenCode adapter. The later user clarification
  tracked by `task-3vt.47` distinguishes normalized messages from raw native sessions;
  both are first-class kinds alongside Beads, documents and OTel.
- Git first, with implemented per-workspace Dolt/document refs and proposed separate
  message/session refs. Built web assets are outside nugs storage/hosting scope.
- Prefer native `dolt push/pull` for Beads, not JSONL replacement sync. Rclone is a
  candidate regular-file/S3 backend later; it is not a Git merge mechanism.
- OTel is a required separate kind, outside the implemented Git slice. Its backend,
  content policy and retention remain undecided; the earlier S3 default was a
  proposal, not a settled choice under the clarified taxonomy.
- Consider per-kind backend selection and TTL cleanup later. A Docker/MinIO test
  was requested before the more specific Git-only direction; it is deferred rather
  than treated as an initial dependency.

The earlier Go/S3 specs in the VD repository are historical planning inputs. The
Git-first decisions here supersede their remote layout and publication assumptions.
Do not retain a mandatory S3 catalog, Node runtime or VK schema in the Go core.

## Typed nug boundaries and implementation plan

The five in-scope kinds are:

| Kind | Content boundary | Storage contract |
| --- | --- | --- |
| Beads | Native task graph and history | Implemented `refs/dolt/<workspace>` through native Dolt |
| Documents | Authored forms, plans, decisions, research and reports | Implemented `refs/documents/<workspace>` |
| Messages | Normalized back-and-forth user messages and agent responses | Proposed Git `refs/messages/<workspace>`; schema/API not implemented |
| Sessions | Raw native agent session exports, including source-specific parts | Proposed Git `refs/sessions/<workspace>`; schema/API not implemented |
| OTel | Agent/workload telemetry with signal identity and observation coverage | Required separate kind; backend, namespace and policy unresolved |

Kind is determined by purpose, not JSON/text encoding. A report about a session is
a document; the native export itself is a session; its normalized user/agent
exchanges are messages. Tool-call telemetry is OTel when captured as signals;
native tool parts also remain in the raw session without implying OTel coverage.
Built web payloads (including Storybook bundles) belong to external hosting such
as GitHub Pages. Documents can retain hosting links and build provenance, not the
hosted payload. A separate CI dependency/compiler-cache proposal does not expand
these nug kinds or authorize asset hosting.

### Schema and adapter requirements

- Bind each resource to the existing stable workspace identity. Storage handles
  select namespaces; source branch changes must not retarget them.
- Version messages and sessions independently, with source producer/version,
  source session/message IDs, observation time and explicit coverage. Link a
  normalized message revision to its source session revision; timestamps or equal
  text alone must not merge distinct messages, retries or session branches.
- Preserve imported native session bytes and unknown parts under the selected
  content policy. If sanitization transforms bytes, record that transformation
  and coverage rather than claiming a byte-identical source export.
- Re-importing the same source revision must be idempotent. Divergent revisions
  must preserve both intent and provenance and expose conflicts. Shared Go
  CLI/MCP operations need version-checked writes and per-kind outcome receipts;
  no cross-ref atomic publication is implied.
- Decide OTel backend/transport, signal/content capture and retention before its
  storage implementation. Do not infer that Git or S3 is selected by the existence
  of other kinds. Preserve native signal identity, late data and coverage gaps.

### Delivery and legacy migration

1. Extend the versioned schemas and independent reader contracts (`task-3vt.3`,
   `task-3vt.6`) for distinct messages, sessions and OTel.
2. Implement typed message/session operations and the explicit OpenCode export
   adapter (`task-3vt.16`), verifying raw-byte fidelity, normalized-source links,
   repeated-message identity, partial sources and CLI/MCP parity.
3. Migrate existing document-stored native exports only through the explicit
   operation tracked by `task-3vt.16.1`: inventory source URI/version, hash and links;
   publish typed targets; read back and verify bytes/provenance; record a mapping
   receipt; update consumers to pinned typed revisions. Interrupted/repeated runs
   must resume without duplicate ingestion. Preserve legacy documents and links
   until verification and any separate removal/retention decision. No automatic
   migration during attach, init or ordinary reads.
4. Resolve OTel decisions in `task-3vt.14` and per-kind retention in `task-3vt.22`.
   No cleanup starts merely because a resource can be classified.

The earlier `refs/conversations/<workspace>` and `refs/assets/<workspace>` were
design proposals, not implemented namespaces to rename or delete. Existing native
exports under document paths remain valid historical evidence during this work.

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
Server/proxied setup remains a follow-up. Native `beads sync` now provides bounded
publication/fetch/clean-merge retries. Checkpoint receipts are immutable history
claims, not leases on local or remote HEAD: concurrent local edits and published
descendants do not invalidate a handoff. Fresh restore materializes the pinned
historical checkpoint after verifying ancestry and identity; catch-up incorporates
it into current local history. Only native clean merges are automatic. True
conflicts and constraint violations retain both histories and stop synchronization.
No long-lived workspace-wide writer pause is part of this contract. Source-repo
callers of reusable workflows should supply stable setup policy; changing native
checkpoint descriptors belong to trusted workflow plumbing, not caller arguments.

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

Onboarding policy lives at `os.UserConfigDir()/nugs/config.json`, managed by
`nugs config set/show`; repository-local policy files are not auto-discovered.
Explicit `workspace init` inherits that policy or, when absent, creates a private
local bare data repository. `workspace attach` registers an existing namespace
without migration. Immutable bindings and canonical directory attachments live
in the separate machine-private `nugs/registry.json`, shared by CLI/MCP Service.
Ordinary operations use attachment context, stable opaque workspace references,
and engagement-scoped discovery/resources/caches. Branch slugs are setup-time
handles only; source branch changes and policy/profile edits cannot retarget a
binding. Missing/ambiguous bindings fail without fallback. Existing hosted refs,
native Beads collection IDs and resource versions remain unchanged. Rebinds and
cross-engagement transfers are separate explicit operations, not implicit setup.

Common operations should expose explicit workspace identity, kind, ref and version:

- Workspace inspection and local bindings, including all repo memberships.
- Workspace-scoped resource browsing across the supported kinds, with stable
  resource identities and actionable version/conflict receipts for agents.
- Document list/read/upsert with the expected prior version, immutable commits,
  useful diff/review context and a new-version receipt.
- Planned message/session import/list/read with an initial OpenCode exporter adapter.
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
OpenCode source session/message identity should prevent duplicate ingestion while
preserving legitimate repeated prompts, branches/retries and corrections.

## Accepted Git confidentiality

The same response selects `private_git_v1`: authenticated private Git access
controls are sufficient for v1 Beads, documents and Git-backed messages/sessions.
This explicitly supersedes the earlier generated-key client-side encryption requirement for these
Git-backed components. Repository readers can read their content/history; custom
refs are organizational namespaces, not separate confidentiality boundaries.
OTel and any future S3 backend confidentiality remain separate decisions.

The user also permits code/file changes and emphasizes ergonomic workspace-scoped
CLI/MCP resource browsing, including upserts. These two policy decisions no longer
block Git-first implementation.

## Remaining decisions

**TTL semantics:** Deleting a ref is not guaranteed erasure: Git objects can remain
in other refs/reflogs, provider retention and clones. Deleting active Dolt history
also needs a live-workspace/last-good policy. Record desired windows before a
cleanup command/cron; no background deletion is enabled by this experiment.

## Deferred experiments

MinIO in Docker, rclone S3 round trips, OTel capture/query, selectable per-kind
backends and automated TTL cleanup are separate tracked work. Built web asset
storage/hosting is excluded, rather than deferred. Validate the typed Git slice
and conflict semantics before adding backends.
