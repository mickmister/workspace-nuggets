---
name: nug-document-authoring
description: Use before creating or publishing workspace nug documents, plans, research findings, decision forms, or review packets; chooses whether a document is needed, its owning Bead and canonical remote location.
---

# Author a useful document

Read [the document-store conventions](references/document-store.md) before choosing
a location. These skills are provider-neutral; project `AGENTS.md` links are also
entry points when automatic skill discovery is unavailable.

## Decide whether to write

Create a document when someone needs to review a proposal, reuse a research finding,
understand a durable decision, or operate/recover a current system. Name the reader,
purpose and owning Bead. Search the current initiative/thread before creating a new
file: often the right action is to update the current design or add a task note.
Task progress, immediate next actions and blockers belong in Beads. Routine activity
can stay in conversation. No mandatory research/plan/handoff packet per session.

Do not put non-documents in Documents, even as a temporary convenience: no native
DBs/checkpoints, transcripts, telemetry, event histories, raw result dumps, binaries,
build output or code/dependency archives. A Markdown wrapper does not change purpose.
Use the appropriate S3/selected CI-artifact resource for useful payloads, source
control for maintained code/tooling, and discard obsolete material when authorized.
Small human-reviewable forms, schemas/examples, decisions, patches and compact
evidence summaries qualify by purpose; do not turn that exception into a log store.

## Choose ownership and path

- Working material for one bounded outcome: `threads/<bead-id>/proposal.md`,
  `research.md`, or a specifically named document. A new runner/native session does
  not create a new thread. Create a new linked Bead/thread for an independently
  completable question/deliverable. Session contribution directories are optional
  only for parallel/competing drafts, not required for each execution.
- Shared current knowledge: `projects/<project>/<initiative>/design.md`, `plan.md`,
  `research/summary.md`, `decisions/`, or `forms/`. Use existing initiatives and
  stable paths; do not create a project for every task or duplicate canonical docs.
- Review resources: `review-requests/` is an existing namespace. Its operating
  process is pending acceptance under task-z5a; see the shared conventions before
  working with it. General document publication does not require a review packet.

Use lowercase kebab-case; dates identify immutable investigations/phase records,
not every prose edit. `vN` identifies actual form/schema contracts. A short prose
header should identify purpose, owner, draft/approved state and replacement links;
do not add metadata to strict JSON wire formats unless their schema permits it.

## Publish and hand off

All durable drafts are remote. Publish meaningful work through the bound nugs
workspace; do not leave a runner path as the only handoff. Use create-only writes
for a new name and the observed version for updates. Read back and compare exact
bytes/version before saying saved. Use a pinned snapshot for historical reads.

Give the user a full revision-pinned UI URL using the conventions, and record the
resource/version in the owner Bead. Preserve existing reviewed revisions and answers.
Review-request creation, updating and reading procedures will be documented after
the workflow is established and accepted; do not infer them from directory names.
