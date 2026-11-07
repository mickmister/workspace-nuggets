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

Ordinary CLI/MCP operations use the persistent directory attachment in the
machine-private `nugs/registry.json`. Explicit `workspace init` inherits configured
engagement policy, or creates private local storage when no policy exists;
`workspace attach` preserves a selected existing namespace. Machine defaults and
branch changes never retarget existing bindings. Use returned `workspace_ref`
values for explicit same-engagement selection; missing/ambiguous bindings fail.
Each manifest records actual repository branch refs and commits separately from
the persisted readable storage handle. Setup does not migrate existing data.

Use the workspace-root `.beads/redirect` collection for task tracking and run `bd`
from that root. Do not initialize or select repository-based Beads databases.
