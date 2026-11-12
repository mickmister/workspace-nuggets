# Document-backed workflow runs

A run groups versioned documents under `workflow-runs/<id>/` in the attached
workspace. Its immutable-by-convention `run.json` records identity, purpose, an
optional task reference and creation time. Methodology, phase and approval state
belong to the workflow's documents; nugs does not schedule agents or approve work.

```sh
nugs runs create --run goals-example --purpose 'Confirm the problem framing' --task beads://collection/task-123
nugs runs write --run goals-example --name config.md --file config.md
nugs runs inspect --run goals-example
nugs runs read --run goals-example --name config.md --snapshot FULL_COMMIT_ID
nugs runs write --run goals-example --name config.md --expected OBSERVED_BLOB_VERSION --file config.md
```

Omit `--file` to write from stdin. An empty expected version means create only.
Reads return the same document and resource structure as `documents read`; writes
return publication receipts. Inspect returns the manifest and the run's resources.
Run IDs use 1–100 lowercase letters, digits or hyphens, starting with a letter or
digit. Resource paths must be safe relative document names.

Create retries with the same ID, purpose and task recover the original manifest.
Changed identity inputs conflict. Run writes reserve `run.json` and require an
existing run. They use strict optimistic concurrency: advancing the target blob
conflicts even when the two edits could text-merge; independent resources can
still reconcile. Ordinary document writes keep their existing merge behavior.

Pinned reads and inspection accept a full Git commit ID reachable from the
workspace's published document history. A blob version is a resource identity;
the receipt's commit is the multi-document snapshot identity. Pin that commit
when materializing several inputs so all reads refer to the same revision.

MCP equivalents are `run_create`, `run_inspect`, `run_read`, and `run_write`.
The Go API exposes `CreateRun`, `InspectRun`, `ReadRun`, `WriteRun`, and
`UpsertStrict`. CLI/MCP operations use the ordinary workspace binding and storage
credentials. The prefix and reserved manifest are helper conventions, not an
access-control boundary: the underlying documents API can address these paths.

Publication is per document. A workflow applying several effects must preserve
operation identities and pinned inputs, record results before acknowledging
external work, and repair partial application on retry. These helpers do not
provide multi-document transactions or trusted submission acceptance.
