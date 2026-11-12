# Agent workflow

## Document hygiene skills

Use these repository-owned skills for workspace artifacts (also usable by agents
working in sibling repositories):

- [nug-document-authoring](.agents/skills/nug-document-authoring/SKILL.md): before
  creating a draft, research finding, plan, form, or review packet.
- [nug-document-maintenance](.agents/skills/nug-document-maintenance/SKILL.md): after
  decisions or implementation changes, and before handoff, commit, or task closure.
- [nug-document-archival](.agents/skills/nug-document-archival/SKILL.md): when guidance
  is superseded or work is complete, or when moving/removing document resources.

The shared [store conventions](.agents/skills/nug-document-authoring/references/document-store.md)
define the permitted authored namespaces: `projects/`, `threads/`,
`review-requests/`, and `archive/`. Do not create top-level `forms/`, `research/`,
`plans/`, `design/`, `designs/`, `specs/`, `evidence/`, `reviews/`, or `reports/`.
Existing `ci/`, `operations/`, `workflow-runs/`, and `workspace/` are compatibility
or reserved API resources, not new authored-document destinations. Do not rename
their machine contracts as an incidental cleanup.

Meaningful drafts and handoffs are remote nugs resources. Runner files are
disposable materializations, never a durable local-draft tier. Use supported nugs
CLI/MCP operations with observed versions and verify publication/readback. Keep
runtime checkpoints, databases, transcripts, telemetry, raw execution logs, build
outputs and source-tree bundles OUT of Documents; use the appropriate remote
payload store, source repository, or retire obsolete material explicitly.

Record resource provenance in Beads and give users full revision-pinned document
UI URLs. Construct them manually as described in the shared conventions until
configured URL generation in nugs is available (task-n3j). Do not hand off only a
bare nug path, `nugs://` URI, or runner path. Preserve real form IDs and exact DSL
versions; use the supported form/review response transport and keep original
answers immutable. Product code, maintained product documentation, schemas and
reusable test fixtures remain source-controlled.

Before a source commit or artifact publication, and at task closure, ask: did this
change current guidance, record a decision, supersede a draft, or produce a
reviewable deliverable? Update/link/archive the affected documents when needed;
do not create a document or review request for every tool call or routine commit.

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
