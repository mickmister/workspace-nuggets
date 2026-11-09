# Concurrent writes and publication recovery

## Native Beads baseline

Local agents use ordinary `bd` commands against one workspace collection, selected
by the workspace-root redirect. Beads owns the database lifecycle, transactions
and commits. Independent machines and CI can have native remote peers. A private
physical database per local agent is not required by the nugs design.

`TestBeadsSharedLocalWriters` compares direct `bd dolt commit/pull/push` with
`nugs` synchronization in disposable embedded collections. Both use explicit
auto-commit on and auto-push off, two local writers, an independent remote edit
and a cold replacement reader. Native tool versions are logged. Interleaving
errors are reported, followed by a quiescent retry; the final local and cold
reads must preserve every writer's final revision. This is a bounded baseline,
not a claim that arbitrary continuously active writers can never exhaust retries.

The native comparison uses independent issue edits. It does not establish that
native pull's conflict policy is equivalent to nugs' clean-only policy. Nugs
continues to stop on actual row conflicts and constraint violations.

Run the native baseline and transport tests with compatible installed tools:

```sh
NUGS_TEST_BEADS=1 NUGS_TEST_DOLT=1 go test ./...
```

The embedded fixture does not establish SQL-server/proxy compatibility. Those
lifecycles need their own fixtures before claiming support. Never open the live
workspace database with a standalone second Dolt engine to run these tests.

## Partial native publication receipts

After a checkpoint has been identified, publication errors retain its collection,
ref, branch and native commit in the response. `beads.sync` also retains it in
`synchronization.publication`.

- `push_acknowledged: true` means the native push command returned success.
- `verified: true` means independent recovery verified the captured checkpoint.
- An error with acknowledgement true and verification false means publication
  progressed, but independent verification did not complete.
- Acknowledgement false does not prove absence from the remote: a response might
  have been lost. A retained local checkpoint alone is not a remote handoff.

Errors remain errors. Only successful independently verified publication can
produce a workspace handoff. Retrying synchronization after connectivity returns
can verify the already-published checkpoint without repeating agent work. This
is partial-progress reporting, not a persisted operation journal or a guarantee
of a final upload after process death.

## Document baseline

Documents use isolated Git transactions and per-resource expected blob versions.
Independent changes reconcile through clean Git merges; overlapping edits and
concurrent creates preserve intent and return conflicts. A stale version observed
before publication is rejected. Two edits based on the same revision can still
cleanly reconcile if their changes do not conflict.

The HTTP integration fixtures exercise simultaneous independent writers, actual
Git acceptance followed by a lost acknowledgement, and HTTP 401/503 failures.
Failed initial transfers retain local intent; restoring transport permits retry
and a fresh reader verifies the resulting bytes. A lost push acknowledgement is
reconciled against remote ancestry without creating a duplicate commit.

```sh
go test ./... -run 'TestDocumentsConcurrentWriters|TestDocumentLostPushAcknowledgement|TestDocumentTransportFailureRetainsIntent|TestDocumentReconciliationOverHTTP'
```

Current error codes still group some authentication/outage failures by operation
phase. Broader classification, continuous-writer stress, abrupt-process loss and
interrupted restore remain reliability work. No cross-kind atomicity or global
writer pause is implied by these tests.
