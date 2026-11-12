---
name: nug-document-archival
description: Use when nug documents are superseded, investigations finish, old guidance needs archival, or a document cleanup/move/removal is requested; preserves live consumers and exact review/history references.
---

# Archive or retire documents

Read the [shared store conventions](../nug-document-authoring/references/document-store.md).

## Choose disposition

- Active: a current use, unresolved question or meaningful ongoing deliverable.
- Historical: a useful previous phase, investigation, decision or superseded
  approach; move to `archive/<project>/<initiative>/` with reason/replacement links.
- Disposable: redundant drafts, obsolete generated payloads or test copies with
  no continuing use; remove under the applicable accepted cleanup scope.

Age is a review priority, not proof of obsolescence. Closed design tasks may own
current unimplemented proposals. Do not preserve a large active catalog merely
because its original folder counts were proposed. Do not archive raw checkpoints,
logs or source archives unchanged under a new name to evade Documents hygiene.

## Verify dependencies before changing paths

1. Pin the current workspace document snapshot and source versions. Inventory the
   exact paths, bytes, hashes, proposed targets and reasons; revalidate before writes.
2. Inspect current readers/writers, task references, review members/baselines,
   response history, form identities and recovery requirements. Review pins are
   retention roots. Do not break machine namespaces or live resume/deduplication
   readers to simplify a directory tree.
3. Preserve useful originals byte-for-byte where historical fidelity matters. Keep
   original human responses and strict wire records immutable. Update current prose
   to full pinned replacement URLs; retain a compact old-to-new mapping and exact
   pre-migration snapshots for old references. Do not imply an alias resolver exists.
4. A file still needed by a current consumer remains until that reader/storage
   transition is verified. Move useful bulky payloads to remote S3/selected artifacts
   only where needed and authorized; otherwise retire obsolete live data under the
   accepted policy. A hash is not a backup when actual bytes are required.

## Apply and verify

Use supported version-checked move/remove operations when present. Inspect CLI/MCP
help first: do not invent a `documents move/delete` command. If the necessary
operation is absent, record the implementation need or use an explicitly authorized
bounded native Git transaction: exact source-version checks, normal child commit,
non-force publication, conflict rejection/reconciliation, and remote verification.
Never rewrite history, force-push a winner or run garbage collection as incidental
cleanup. Retained reachable Git history is distinct from the current live tree.

Verify new bytes/hashes, selected removals, unrelated resources, reserved manifest,
old pinned snapshot reads and review baselines. Record before/after counts and bytes,
the mapping, held dependencies and remote receipts. Return a full revision-pinned
UI link to the result. Do not leave the audit or handoff solely on a runner disk.

For this workspace, accepted cleanup removes obsolete LIVE files while preserving
Git history, without new S3 copies unless a current consumer needs them. This is
not permission to remove other engagements' data or erase historical decisions.
