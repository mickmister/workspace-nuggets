# Agent workflow

## Intermediate artifacts belong in nuggets

Store session-specific forms, decision drafts, research captures and generated
reports as versioned workspace resources, rather than files in the source-code
branch. Use `nugs documents upsert` or MCP `document_upsert`; the current artifact
backend is `refs/documents/<workspace>` in the configured storage repository.

- Use logical names such as `forms/workspace-composition.json` or
  `research/source-inventory.md`.
- Use `/tmp/opencode` for scratch files or pipe content through stdin. Upload the
  selected artifact before removing its scratch copy, and verify the receipt.
- Record its workspace, resource URI and version in the relevant bead. Updates
  use the observed version, so another agent's artifact cannot be overwritten
  silently.
- Attach human decision forms with `beads-form` as well; a form attachment and its
  nugget artifact should refer to the same DSL/revision. Use the artifact or stdin
  for subsequent attachment instead of recreating a tracked source file.
- Product code, reusable test fixtures, schemas and maintained project
  documentation belong in source control. Classify by purpose, not extension:
  intermediate form JSON is an artifact; a reusable parser fixture may be source.

## Workspace identity and Beads

The storage remote defaults to the machine's `nugs/config.json`. Workspace names
default to branch slugs; use `--workspace` to select another workspace or resolve
a slug collision. Each manifest records its repositories' actual full branch
refs and commit IDs separately from that handle.

Use the workspace-root `.beads/redirect` collection for task tracking and run `bd`
from that root. Do not initialize or select repository-based Beads databases.
