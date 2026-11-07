# Workspace nuggets (`nugs`)

A standalone Go CLI and MCP server for workspace-scoped agent context.

## Current first slice

Git-only storage, initially for **Beads, documents, and an OpenCode adapter**.
The CLI and stdio MCP server now share Go operations for workspace document
browsing, reading and version-checked upserts. Workspace-owned Beads native remote
configuration and inspection are also available. Existing `bd` operations own task
editing and database sync; the OpenCode conversation adapter remains next.

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

# Configure the machine once; commands infer the current branch workspace.
nugs config set --remote https://github.com/mickmister/workspace-storage.git
nugs workspace inspect
nugs documents list

# Create only; reads exact bytes from a file, or stdin when --file is omitted.
nugs documents upsert --name reviews/design.md --file ./design.md

nugs documents read --name reviews/design.md

# Update using the version returned by read/list/upsert.
nugs documents upsert --name reviews/design.md --expected "$VERSION" --file ./design.md
```

All commands return JSON. Read results contain `content_base64` for exact bytes,
and `text` when the document is valid UTF-8. Resource versions are Git blob OIDs;
receipts also identify the published commit, SHA-256, byte length and inferred
media type. An unchanged upsert with the current version creates no new commit.
Omitting `--expected` means **create only**, not unconditional overwrite.

## Intermediate agent artifacts

Session-specific form JSON, decision drafts, research captures and generated
reports belong in workspace nugget storage, not the source-code branch. Today
these use the document backend, which preserves arbitrary exact bytes under
`refs/documents/<workspace>`. A separate `refs/artifacts` backend is not required
to keep them out of source refs.

```sh
nugs documents upsert --name forms/workspace-composition.json --file /tmp/opencode/form.json
nugs documents read --name forms/workspace-composition.json
```

Record the returned resource URI/version in the bead, and attach forms using
`beads-form` as well. Use the observed version for artifact updates. Repository
`AGENTS.md`, CLI help and MCP initialization instructions communicate this workflow.
Maintained project documentation, schemas and reusable test fixtures remain
source-controlled; purpose, rather than a `.json` extension, determines placement.

## Machine config and workspace selection

Config defaults to `nugs/config.json` under the machine's user config directory:
`$XDG_CONFIG_HOME/nugs/config.json`, normally `~/.config/nugs/config.json` on Linux.
It is not discovered from repository files. `nugs config show` prints its location
and values; `nugs config set --remote URL [--cache DIR]` updates it atomically.

```json
{
  "remote": "https://github.com/mickmister/workspace-storage.git"
}
```

`--config FILE` selects a different config. `--remote` and `--cache` override its
values for one command. Relative paths in config are resolved from that file's
directory; `config set` converts local path arguments to absolute paths. An absent
default config is allowed when flags provide a remote; an explicitly selected
missing or malformed config is an error.

Workspace defaults to the current branch slug: `vk/46e5-vd-multi-instanc` becomes
`vk-46e5-vd-multi-instanc`. Slugs lowercase ASCII letters and digits and collapse
punctuation into hyphens. Full branch refs remain in the manifest. When invoked
from a non-Git workspace root, provisioning can infer a common branch from the
explicitly selected repositories. Detached HEAD, disagreeing selected branches,
or a branch with no supported slug require `--workspace ID`.

`--workspace` can select another workspace, an existing UUID, or disambiguate
branch names that normalize to the same slug. Storage remote and workspace slug
together scope the resources; using a slug does not merge independent Beads
collections. Existing UUID refs remain addressable. Explicit workspace IDs accept
letters, digits, hyphens and underscores. Document names are safe relative paths.

Storage refs are derived from the selected workspace handle, not independently
from every member repository's branch: `refs/documents/<workspace>` for documents
and intermediate artifacts, and `refs/dolt/<workspace>` for the native Beads
binding. `refs/conversations/<workspace>` is reserved for the planned adapter.
When invoked inside a Git worktree, its current branch supplies the default even
if another member repository has a different branch. The common-branch fallback
applies only outside a Git worktree with explicit repository memberships. Different
member branches remain explicit manifest bindings; they do not create multiple
implicit workspace storage refs. Source branch renames change the inferred slug;
use an explicit workspace handle to continue querying an older namespace.

## Provision a workspace manifest

```sh
nugs workspace provision --root /path/to/workspace \
  --repo application --repo shared-library
nugs workspace inspect
```

Select every repository membership explicitly with repeated `--repo` flags.
Repository paths are relative to `--root`; absolute paths within that root also
work. Provisioning observes the selected local repositories and writes the
reserved `workspace/manifest.json` resource in the existing document ref. It
requires a committed HEAD in each selected repository. To refresh it, pass
`--expected` with the `manifest_version` from inspection.

The versioned manifest contains:

- Workspace identity, schema version and observation timestamp.
- All selected repository membership paths relative to the workspace root.
- Each current full branch ref and exact commit OID, or explicit detached HEAD.
- Working-tree dirty state and configured fetch remotes, with URL auth fields
  removed from observed HTTP URLs and passwords removed from SSH URLs.
- `publication: "unverified"` or `"no_remote"`; capturing a commit does not prove
  its branch/objects exist on another machine.

Inspection returns the parsed manifest and version from the same fetched commit
as its document list. The manifest is schema-validated before publication,
including after automatic reconciliation. Source observations are sequential,
not an atomic multi-repository snapshot; HEAD/branch changes detected during an
individual repository observation cause a retryable failure.

This is a **branch-pointer manifest**, not a workspace restore or source-code
backup. Provisioning does not create/push source branches, upload Git bundles,
copy uncommitted changes, configure source remotes, or initialize Beads. Base/PR
relationships and verification that source commits are remotely fetchable remain
future identity/materialization work. Ordinary document refs work with an empty
storage repository; no default source branch needs to be created.

## Use from an MCP client

Launch `nugs mcp` as a stdio MCP server. Remote/cache come from machine config;
the default workspace is inferred from the launch directory's branch. Flags can
override those defaults. Tool requests may omit `workspace` to use the server
default, or select one explicitly. Outside a branch checkout, launch with
`--workspace ID` or provide a workspace on every tool request. Available tools:

- `workspace_inspect`: `{ "workspace": "feature-123" }` — manifest, version and
  current document view; manifest is absent for an unprovisioned workspace.
- `workspace_provision`: `workspace`, explicit local `root`, `repositories` array,
  and `expected_version` (empty for create only).
- `beads_inspect` / `beads_configure`: optional `workspace` and local workspace
  `root`; omitted root discovers the outer workspace redirect from server cwd.
- `documents_list`: same input; returns resource URIs and versions.
- `document_read`: add `"name": "reviews/design.md"`.
- `document_upsert`: add `"name"`, `"expected_version"` (empty for create only),
  and exactly one of `"text"` or `"content_base64"`.

The server also advertises a `nugs://workspace/{workspace}/documents/{name}`
resource template. Use the URI returned by listing rather than manually escaping
names. Tool errors set MCP `isError` and retain structured error/conflict data.
Protocol stdout contains only MCP messages.

## Workspace-owned Beads

For a new, explicitly selected **data repository**, run `nugs storage bootstrap`
before its first native Beads push. The shared CLI/MCP operation creates an empty,
deterministic root commit at `refs/heads/nugs-storage` only when no ordinary branch
exists. Existing branches and custom document/Dolt refs are preserved. Repeated
or raced bootstrap requests converge without a force push. MCP exposes the same
operation as `storage_bootstrap`. This initializes storage only; it does not
initialize a Beads database, push tasks, change source branches, or publish a
workspace snapshot. Use it only with the selected workspace data repository.

After configuring the workspace's native remote, `nugs beads publish` (MCP
`beads_publish`) checkpoints pending changes through `bd dolt commit`, pushes
without force, and independently clones the workspace's custom ref to verify the
exact Dolt checkpoint. Its receipt contains the collection ID, branch, Git and
Dolt commit IDs, and verification result, with no storage URL or credentials.
Quiesce workers before a handoff: the setup lock does not exclude ordinary `bd`
writes. A changed local/remote head during verification returns an error. A
rejected push retains the local checkpoint and leaves remote reconciliation for
an explicit next step. Publication does not pull or resolve conflicts, initialize
a collection, enable auto-push, or bootstrap the storage repo implicitly.

To attach an exact published native snapshot to a fresh workspace:

```sh
nugs beads restore --workspace WORKSPACE --root /path/to/fresh-workspace \
  --data-dir /path/to/new-persistent-collection \
  --collection-id COLLECTION_ID --dolt-commit DOLT_COMMIT
```

Use the collection ID and Dolt commit from the verified publication receipt.
The workspace root and the persistent directory's parent must exist; the root's
`.beads` marker and the persistent directory must be absent. Restoration clones
the selected custom ref into staging, checks the native `_project_id` and exact
checkpoint, verifies adoption through Beads, then installs a persistent collection
and workspace-root redirect. Existing or symlinked destinations are rejected.
An advanced remote checkpoint is rejected rather than silently adopted. Failed
identity/revision checks leave the destination unattached. If installation succeeds
but attachment fails, the verified persistent directory is retained for recovery.
MCP exposes the same operation as `beads_restore`. Restoration preserves native
history and records; it creates only local routing/configuration, with auto-push
and source Git operations disabled. It does not initialize a new task database.

For an already attached, quiesced workspace, use `nugs beads catch-up
--dolt-commit HASH` (MCP `beads_catch_up`) with the remote publication's exact Dolt
commit. Catch-up checkpoints local changes, independently validates the remote
collection/revision, fetches through Beads' federation status interface, and
invokes bare `bd vc merge` against the pinned hash. It never uses Beads' pull
auto-resolver or a winner-selection strategy. A changed local checkpoint or
unclean fetch stops the operation. Merge failures return local/remote checkpoint
IDs for inspection; catch-up does not publish the result. Native row/schema/
constraint errors are surfaced rather than repaired or resolved automatically.

One Beads collection belongs to the **workspace**, including all its repositories.
Run native `bd` commands from the workspace root. `nugs` discovers the outer
workspace `.beads/redirect`, resolves it using `bd where`, and inspects the existing
collection rather than selecting a repository's `.beads` database. An explicit
`--root` must contain that redirect. It never initializes databases in source repos.

```sh
# From a branch checkout inside the workspace; machine config supplies the remote.
nugs beads inspect
nugs beads configure

# Outside a branch checkout, supply the readable workspace slug explicitly.
nugs beads configure --root /path/to/workspace --workspace feature-workspace
```

Setup is idempotent. It reports the collection's stable Beads project ID, resolved
storage paths, native mode/database, remote URL and `refs/dolt/<workspace-slug>`.
It configures native `origin`, declares `sync.remote` through `bd config`, and
clears `no-push` so explicit native pushes work. It does not enable auto-push,
alter `no-git-ops`, commit task changes, pull, or push. An existing origin URL/ref
or declaration pointing elsewhere is returned as a conflict, preserving the
collection binding instead of repointing it after a branch change.

The tested Beads 1.2.2 embedded backend stores this collection under
`<resolved-beads-dir>/embeddeddolt/<database>`. Its `bd dolt remote add` lacks a
custom-ref flag and embedded `bd sql` is unavailable, so setup uses the supported
`dolt remote add --ref ...` in that verified directory, then confirms Beads sees
the result. The ref is stored in native remote `params.git_ref`, **not** a URL
query parameter. No engine files are manually rewritten. The Beads init lock
serializes initialization and nugs setup; normal database concurrency remains
owned by the embedded driver, not that init lock. Server/proxied backends are
rejected by this initial setup path rather than opened as embedded databases.

Native `bd list`, `bd show`, `bd dolt commit`, `bd dolt pull` and `bd dolt push`
remain the task APIs. Remote configuration is not itself publication. The tested
Dolt Git transport requires an ordinary initial branch/commit in the storage
repository before its first push; custom document refs alone do not satisfy that
prerequisite. Setup does not bootstrap a storage branch or publish live task data.

For the current VD workspace, the root marker points to the persisted collection
under `/var/lib/vd/beads/workspaces/<workspace-id>/.beads`, not either source repo.
Native remote configuration follows that collection and retains its project ID.

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
NUGS_TEST_BEADS=1 go test -v -run TestWorkspaceBeads ./...
```

Document integration tests use an ephemeral loopback smart-HTTP Git server with
Git's actual `git-http-backend`, temporary repositories and automatic teardown.
They exercise real operations, the built CLI and a real stdio MCP session:
cross-client exact bytes, workspace isolation, idempotency, stale versions,
concurrent creation, independent and same-file clean merges, real conflicts,
retry exhaustion, rejected publication and retained recovery evidence. The server
has no authentication because it contains only isolated synthetic test data.
Manifest tests cover multi-repository branch/commit pointers, detached HEAD,
dirty and missing-remote states, credential stripping, version-checked updates,
invalid manifests and CLI/MCP provisioning parity.
Config tests exercise machine-level discovery, flag/config precedence, branch
slug inference, ambiguous/detached cases and omitted MCP workspace arguments.
The opt-in Beads test creates an actual isolated workspace collection outside its
repositories, configures it, checks issue/history preservation and idempotency,
rejects repointing, then pushes only synthetic data with `bd` over local HTTP and
recovers the issue from the workspace custom ref. Ambient/nested repo routing is
ignored. It requires compatible `bd` and `dolt` binaries.

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
