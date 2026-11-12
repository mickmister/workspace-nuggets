# Workspace-scoped messages and media

Messages and media use **S3 for payloads and catalogs**. Portable message readers
also use S3; interactive readers may explicitly select local SQLite checkpoints.
Git-backed message/catalog/checkpoint routes are rejected before transport access.
Existing document and native Beads storage contracts are independent.

## Setup and namespace

Configure a named S3 profile, then explicitly bind each nug kind:

```json
{
  "payload": "s3",
  "catalog": "s3",
  "layout": "workspace-v2",
  "endpoint": "http://127.0.0.1:19000",
  "bucket": "workspace-nuggets-pilot",
  "region": "us-east-1",
  "checkpoint": "s3",
  "credentials_file": "/private/path/s3.env"
}
```

```sh
nugs config target --target local-s3 --file profile.json
nugs messages bind --target local-s3
nugs media bind --target local-s3
nugs types list
```

Credential files accept `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and optional
`AWS_SESSION_TOKEN`, or native pilot `MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD` names.
When omitted, the selected S3 transport uses AWS environment credentials. Secrets
are never catalog fields or command arguments. Bucket creation is explicit setup.

The physical namespace is:

```text
s3://<bucket>/<optional-prefix>/workspaces/<workspace-ref>/<kind>/
```

The optional prefix is a deployment namespace, not another workspace/type path.
The stable workspace reference survives branch changes and native agent forks.
Named targets select destinations; target aliases do not add another path level.
Bindings retain the configured profile snapshot. Editing setup policy never
retargets an existing binding. Different target aliases selecting the same
bucket/prefix/workspace/kind address the same collection.

The old experimental layout is deliberately rejected by new operations. Old
profiles remain readable in private registries so unrelated document/Beads
commands keep working. Bind a **new** `workspace-v2` profile to recapture reviewed
data; old pilot objects and local review backups are not implicitly migrated.

## Storage format and enforced budgets

Each kind owns the following namespace:

```text
catalog/root.json                  # current conditional root
catalog/published.json             # immutable publication epoch marker
catalog/roots/<sha256>.json        # immutable small root for pinned reads
catalog/nodes/<sha256>.jsonl.gz    # shared pool of immutable typed tree blocks
objects/<sha256>.bin.gz            # fragmented long text / binary media bytes
readers/<name>.json                # message reader state, when S3 is selected
```

The root contains workspace, kind, epoch, generation and a fixed number of tree
references. It does not enumerate the workspace's messages, sessions or segments.
The compressed JSONL block pool contains independent session, occurrence, latest
sequence, event-reference and content-dictionary trees. **Content-dictionary leaf
blocks contain message text**; `catalog/nodes` is a shared block pool, not an
assertion that every block contains metadata only. Media catalogs contain binary
fragment references, never base64 images/videos.

| Boundary | Limit |
| --- | --- |
| Root JSON | 4 KiB |
| Reader checkpoint JSON | 8 KiB |
| Transport object, write and read | 4 MiB |
| Decompressed JSONL/binary block | 1 MiB |
| Index row value | 256 KiB |
| Tree fanout | 64 rows/children per node |
| Tree traversal depth | 32 |
| Normalized single message input | 32 MiB; also subject to row/provenance limits |
| Media import/assembly | 64 MiB |
| Catch-up delivered records | 1–1000 per page |
| Event positions examined per catch-up page | At most 4096 |

Text larger than 32 KiB is fragmented transparently into bounded compressed
objects. Media is fragmented into 1 MiB raw pieces and reconstructed byte-exactly.
Unsupported oversized metadata fails explicitly; no source text is truncated.
Compressed-object digests and reconstructed content digests are checked.

Tree updates copy only paths to changed leaves. A publication stages intermediate
nodes locally and uploads only nodes reachable from final roots. Unchanged blocks
and exact content are reused. Immutable root snapshots share tree blocks rather
than storing full workspace catalogs. CAS retries reconcile disjoint sessions;
competing changes to the same observed source revision return a conflict.

An interrupted upload may leave unadvertised immutable objects. They are not a
successful publication. No automatic deletion/garbage collection is enabled.
Small root history and actual content revisions accumulate; this is not a promise
of constant total storage regardless of communication/history retained.

## Message capture and identity

```sh
nugs messages scan --target local-s3 --native-version 1.19.4 --session ses_EXPLICIT
nugs messages sync --target local-s3 --native-version 1.19.4 --repo member-repo --limit 0
nugs messages sessions --target local-s3
nugs messages read --target local-s3 --session opencode:ses_EXPLICIT
nugs messages verify --target local-s3
```

OpenCode capture uses a WAL-aware read-only transaction and validates the supported
1.19.4 native schema. `--database` selects a native DB; omitted uses the standard
OpenCode data path. `--native-version` may be omitted to invoke `opencode --version`.
Ownership requires exact workspace/member-directory matching or explicit historical
`--alias` paths. `/` is never an ownership wildcard. `--session` accepts one native
ID or a comma-separated explicit set. `--limit 0` scans all matching sessions.
Optional `--vk-database FILE --vk-workspace UUID` verifies the native workspace /
process / coding-agent-turn join and labels dropped/reset resume eligibility.

The adapter retains user/assistant text, commentary and attachment descriptors with
authored/synthetic/unknown origin labels. Native-marked generated summaries remain
synthetic; absence of a synthetic flag is not proof of human authorship. Inline
attachment data is represented by a descriptor digest, not retained binary bytes.
Tools/results/reasoning bodies are excluded. Records with no retained text or
attachments are omitted. Exact whitespace and distinct source occurrence IDs are
preserved. Equal text is never used to merge message identities.

Content has no source IDs, permitting exact payload sharing across fork copies.
Occurrences independently preserve source IDs, part IDs, roles, timestamps and
ordering. Excluded-tool diagnostics, source update timestamps and observational
VK/lineage annotations do not create authored-message corrections. Role changes
require a new source message identity. Session changes require observed
`base_revision`; identical normalized retries are idempotent.

Native `parent_id` is confirmed lineage evidence. A fixed-size fingerprint of four
nonempty role/text/source-time records supports shared-history comparison without
all-pairs candidate lists or an inferred parent direction. It is stable regardless
of which other sessions are scanned concurrently.

`messages verify` streams all selected messages through a semantic digest and
reports session/message/text-byte counts without returning every body. Repeated
fork occurrences remain part of those counts. `messages import --file BATCH.json`
accepts producer-neutral normalized batches, not raw native exports. Full capture
and legacy export migration are distinct operations.

## Explicit reader delivery

```sh
nugs messages catch-up --target local-s3 --reader reviewer --limit 50
nugs messages read-page --target local-s3 --reader reviewer --token TOKEN
nugs messages ack --target local-s3 --reader reviewer --token TOKEN
```

`prepare` pins a bounded page; `read-page` verifies and returns it; only `ack`
advances the acknowledged position. `catch-up` combines preparation and reading.
Late imports and corrections use publication sequences, not source timestamps.
Deletions are explicit tombstones. Latest-state selection skips superseded events.

Each reader stores one compact checkpoint with a pending token, delivery receipt,
last ACK and token-authentication key. Opaque authenticated tokens carry historical
page plans, avoiding an ever-growing server-side issued-page map. The key is not
returned by reader inspection. Replay is supported while the pinned immutable
objects exist; missing objects fail explicitly. Missing or reset catalogs never
silently produce a fresh/caught-up reader, and established missing roots cannot
be silently recreated by publication.

Use a stable logical reader name across native forks/fresh runners. A named
reader's filter is immutable. One active continuation per reader plus checkpoint
CAS protects against stale jobs. This storage API does not itself schedule CI
continuations or implement native agent-session resume.

## Media, including screenshots

```sh
nugs media import --target local-s3 --name screenshot-1 --file screenshot.png --category screenshot --source run:example
nugs media list --target local-s3 --limit 50
nugs media read --target local-s3 --name screenshot-1 --file new-output.png
nugs media verify --target local-s3 --name screenshot-1
```

Metadata includes logical ID, original filename, MIME, category, source, size,
SHA-256 and image dimensions when recognized. Screenshots are `media` records, not
a separate duplicate payload authority. Different logical IDs can share exact
binary bytes. Updates require `--expected` with the observed metadata revision.
Pinned listing tokens retain a catalog snapshot across concurrent imports.
Output files are create-only; existing files are not overwritten. Without an
output file, reads return JSON with base64 content. MCP exposes corresponding
`media_bind`, `media_import`, `media_list`, `media_read`, `media_verify` operations
through the same Go Service as the CLI.

## Local acceptance

```sh
go test ./...
NUGS_TEST_S3_ENDPOINT=http://127.0.0.1:19000 \
NUGS_TEST_S3_BUCKET=workspace-nuggets-pilot \
NUGS_TEST_S3_CREDENTIALS_FILE=/private/path/s3.env \
go test -run 'TestMessagesActual|TestMediaActual' -v .
```

Tests cover bounded tree updates, source filtering, large exact text, shared bytes
with distinct identities, conflicts, missing/reset catalogs, stale ACKs, historical
replay with bounded checkpoints, native scope/schema gates, media corruption,
screenshots, actual MinIO, and CLI/MCP binary parity. They do not publish to GitHub.
