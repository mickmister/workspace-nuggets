---
name: nug-document-maintenance
description: Use after human decisions, implementation changes, review feedback, or before a commit, handoff or task closure to reconcile current nug documents, exact-revision links and superseded guidance.
---

# Maintain current knowledge

Read the [shared store conventions](../nug-document-authoring/references/document-store.md).

1. Read the owning Bead, current initiative entry and affected documents. Separate
   human decisions, agent recommendations, implementation claims and evidence.
   Update the canonical design/plan rather than publish another unlinked dated copy.
2. Preserve the original question/answer, real form identity and exact reviewed
   revision. Incorporate the outcome into the current proposal and link to its
   decision receipt; never rewrite a human's historical answer to fit a new design.
3. Identify what changed, why, what is superseded, and the replacement. Update
   unresolved questions and links. Keep task status/actions in Beads rather than
   maintaining another detailed live task ledger in Markdown.
4. Record actual implementation/evidence separately from document approval. A
   completed "write the plan" task does not implement the plan or make it obsolete.
   An older accepted decision can remain historically approved but superseded.
5. Use the observed version for the write. On conflict, reread and reconcile the
   new content; do not silently choose an automatic winner. Verify publication and
   readback, then hand off a full revision-pinned UI URL, not a runner path.

## Review packets

For an existing unanswered packet covering the same outcome, update that request
with a new manifest revision and verified member references. Keep target and
baseline fixed for each revision; the user can see both the full document and delta.
Show an explicit update rather than change content underneath an answer. Responses
remain paired with the exact request revision. Follow the currently implemented
review publisher/intake contract; do not guess flags or fabricate receipts.

A materially different question or follow-up round may warrant a linked request.
A different runner or native session does not. Shared documents stay in their
canonical location; feedback/drafts stay isolated by request/reviewed revision.

## Boundary reminder

Before publishing, committing source, handing off or closing the task: reconcile
affected current documents, preserve decisions, archive obsolete material and link
the user to the resulting reviewable resource. No affected knowledge means no new
document. Meaningful unfinished work must be saved remotely before claiming it is
durable; finalization alone cannot guarantee saving after runner loss.
