# Remote document-store conventions

## Authored namespaces

| Path | Purpose |
|---|---|
| `projects/<project>/<initiative>/` | Shared current knowledge, specs, decisions, forms and compact evidence |
| `threads/<bead-id>/` | Working documents for one bounded question/outcome |
| `review-requests/<request-id>/` | Mechanically discoverable focused request, forms/patches and paired responses |
| `archive/<project>/<initiative>/` | Useful historical documents and phase manifests |

Root `README.md` is the workspace entry index. Do not recreate top-level artifact-type
folders (`forms`, `research`, `plans`, `design`, `designs`, `specs`, `evidence`,
`reviews`, `reports`). Forms/research/specs live WITH their initiative/thread.
No standing runbooks are required; create operating instructions only for a demonstrated
current need, normally under the owning initiative. Historical operating instructions
belong in archive, not a new active operations catalog.

Reserved/compatibility namespaces in the current workspace are `workspace/`, `ci/`,
`operations/`, and `workflow-runs/`. Existing machine readers/writers may require
their exact addresses. These are not destinations for new authored knowledge and
not permission to keep adding checkpoint/log payloads to Documents. Their migration
needs explicit source/reader/storage work; do not silently move them to tidy a tree.

Runner files are disposable working copies. All meaningful drafts/handoffs need
remote nugs publication. Source code, reusable scripts, schemas and maintained
product documentation belong in source repositories. Runtime/native state, messages,
transcripts, telemetry, raw outputs and growing execution histories use their typed
remote storage contracts, ordinarily S3 or explicitly selected CI artifacts. Do not
upload obsolete data elsewhere simply to avoid deciding whether to retire it.

## Identity, revisions and review

A Bead owns a thread; native sessions are attempts/contributors within it. A request
ID identifies one review packet/round, not the thread or native session itself.
Discover manifests at `review-requests/*/request.json`. Members can live elsewhere;
pin target and optional baseline resource/version/snapshot, not a mutable path or
timestamp. Responses live beside the request, bind the exact reviewed revision and
keep real Bead/form identities. Do not copy shared docs into each request.

Unanswered requests may be amended via observed-version writes. Publish members
first, manifest last; keep open human sessions pinned when R2 appears. Submitted
responses/answers do not silently apply to R2. Read current implementation docs
before choosing response transport; never infer approval from agent text, an open
page or a closed planning task. Document approval, current applicability and actual
implementation are different facts.

## Full user-facing document URLs

Until configured CLI/MCP link generation is available, construct links manually.
Resolve the known approved viewer deployment base and bound storage repository and
workspace. Never invent a preview hash or use the machine's current branch as the
workspace authority. Prefer current deployment information over old copied examples.

```text
<viewer-base> + "#/documents/"
  + percentEncode(<document-blob-version> + ":documents/" + <logical-name>)
  + "?repository=" + percentEncode(<owner/storage-repository>)
  + "&workspace=" + percentEncode(<bound-readable-workspace>)
```

Encode the complete route identity once, including `:` and `/`; encode each query
value separately. For this document route use the actual document blob version from
the receipt/list/read. A surrounding Git commit is a snapshot pin, not the blob
version; keep it separately for historical CLI reads/retention. Use a Markdown link
with a concise label. Record machine URI/version/snapshot as provenance too, but
never provide only a bare logical name, `nugs://` URI or runner path to the user.

Known deployment supplied for this workspace (verify it is still intended):
`https://mickmister.github.io/github-ultimate/previews/b-vk-46e5-vd-multi-instanc-0940c2d95906d253/app/`
Storage repository: `mickmister/workspace-storage`.
Bound workspace handle: `vk-46e5-vd-multi-instanc`.
These are deployment examples, not global defaults or permission to retarget a
binding. Future nugs URL generation (task-n3j) should hide this config from callers.

## Before publication, source commit, handoff or task closure

Ask whether the change affects current guidance, a recorded decision, an obsolete
draft, or a meaningful user-facing reviewable result. Update/link/archive affected
resources, preserve original answers and return full pinned URLs when applicable.
If no document is affected, create none. This is an advisory hygiene reminder, not
a requirement for a new artifact or approval gate on every tool call/commit.
