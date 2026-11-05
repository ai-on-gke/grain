# Committed context

Tasks often implement specifications written outside the sandbox: product briefs, architecture docs, HTML/CSS mocks, API contracts, or pinned data snapshots. Because grain sandboxes do not hold credentials to upstream planning or document systems, approved specifications travel to tasks through git as **committed context**.

Putting context in git gives both placements the same guarantees:
- **Reviewable before dispatch:** specs land through normal pull requests before implementation runs start;
- **Pinned by commit SHA:** every run, retry, fix, and review reads the exact revision the task was filed against, even if `main` moves later;
- **Reachable offline in the sandbox:** files are checked out on local disk before the agent starts;
- **Auditable:** humans inspecting the task page (`Committed context` card), CLI (`grain get`), API (`context`), or git history see the exact spec tree the agent read.

> [!CAUTION]
> **Never commit non-public, confidential, or access-restricted material into a public repository** (or into any repository whose readers should not see the spec). Deleting a file in a later commit does not erase it from git history, forks, or caches. Whenever the target code repository is public—or readable by a wider audience than the specification—keep the context in a **private context repository** and pass it via the `-read` context-repo placement below.

---

## One reference shape: `{repo, path, rev}`

Every committed-context reference resolves to three fields regardless of where the files live:

| Field | Meaning |
|---|---|
| `repo` | Repository holding the context (`owner/name` or `host/owner/name`). Defaults to the task's own target repository when omitted. |
| `path` | Clean repository-relative directory or file path (for example `design/plc/042` or `specs/auth.md`). `..`, leading `/`, and `.git` segments are rejected. |
| `rev` | Full 40-character commit SHA. When a branch, tag, or short ref is supplied (or `@rev` is omitted), grain resolves it to a 40-character commit SHA when the task is filed or updated. |

On the CLI and in API `designs` strings, all three fields fit in one compact string:

```text
[owner/name:]path[@rev]
```

Examples:
- `design/plc/042` — `design/plc/042` in the task's target repo, resolved to the target base branch's current HEAD SHA at filing time.
- `design/plc/042@9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90` — `design/plc/042` in the target repo at an explicit commit SHA.
- `acme/product-specs:plc/042@v2.1` — `plc/042` in `acme/product-specs`, with tag `v2.1` resolved to its commit SHA at filing time.
- `acme/product-specs:plc/042` — `plc/042` in `acme/product-specs`, resolved to `acme/product-specs`'s default-branch HEAD SHA at filing time.

---

## Two placements, one mechanism

### 1. Folder in the target repository

Use this placement when the specification has the **same visibility and audience** as the code implementing it.

1. Land the context folder (for example `design/billing/invoice-export/`) into the target repository's base branch through its own reviewed pull request **before** dispatching implementation tasks. Never pre-push unreviewed context commits onto a task's working branch ahead of dispatch.
2. File the task pointing `-design` at that folder:
   ```sh
   grain create \
     -repo acme/widgets \
     -title "Export monthly invoices as CSV" \
     -body "Implement CSV export per the committed spec." \
     -design design/billing/invoice-export@main
   ```
3. **In the sandbox:** grain checks out the task's branch at `/home/debian/work`, verifies that the pinned `rev` commit is present in `HEAD`'s ancestry (`git merge-base --is-ancestor <rev> HEAD`), and confirms `/home/debian/work/design/billing/invoice-export` exists. If the task's base branch does not contain `rev`, checkout fails immediately with a clear error rather than running the agent against missing or stale specs.

### 2. Separate `-read` context repository

Use this placement when the target code repository is **public**, or when product briefs, customer examples, or planning artifacts are **more sensitive** than the code repository's reader set.

1. Land the approved context folder in a private repository (for example `acme/product-specs`) that the grain deployment is configured to access.
2. File the task with `-design <context-repo>:<path>[@<rev>]` and grant `-read <context-repo>`:
   ```sh
   grain create \
     -repo acme/public-sdk \
     -read acme/product-specs \
     -title "Add streaming pagination helper" \
     -body "Implement pagination helper matching the approved SDK brief." \
     -design acme/product-specs:sdk/streaming-pagination@main
   ```
3. **Operator auto-grant (`autoGrantContextReads`):** operators who want integrations to switch placements by changing `-design` alone can enable **Auto-grant context repo reads** (`autoGrantContextReads: true` in deployment settings, or `-auto-grant-context-reads` on `grain daemon`). When enabled, filing or updating a task that references an external context repository allowed by the deployment (`AllowedRepos`) automatically adds `-read <context-repo>` to the task (subject to the caller's normal permission to grant `-read` on that repository). When disabled (the default), omitting `-read <context-repo>` returns an explicit validation error naming the missing `-read` grant.
4. **In the sandbox:** grain clones `<owner>/<context-repo>` via the authenticated git proxy into `/home/debian/repos/<owner>/<context-repo>` with push disabled, fetches `rev` if needed, and checks out **detached `HEAD` at `rev`** (not the moving default branch). It then verifies `/home/debian/repos/<owner>/<context-repo>/<path>` exists.

### Switching between placements

Moving an integration between a target-repo folder and a separate context repo requires **no change to folder structure, manifests, prompts, or child-task wiring**—only configuration:

| Aspect | Target-repo folder | Separate context repo |
|---|---|---|
| `-design` value | `design/plc/042@<rev>` | `acme/product-specs:design/plc/042@<rev>` |
| Read grant | Covered by target repo checkout | `-read acme/product-specs` (or `autoGrantContextReads: true`) |
| Path inside sandbox | `./work/design/plc/042` | `./repos/acme/product-specs/design/plc/042` |
| Checkout check | `rev` is an ancestor of `./work` `HEAD` | Cloned read-only at detached `rev` |

---

## Prompt injection and UI visibility

Whether context comes from `./work` or `./repos/<owner>/<repo>`, grain adds a **Committed context** section to both initial and resumed agent prompts naming:
- the local path on disk inside the sandbox (`./work/<path>` or `./repos/<owner>/<repo>/<path>`);
- the placement (`target repo` or `read-only context repo <repo>`) and pinned commit SHA;
- any `context.yml` manifest title, description, and file list.

Humans can inspect a task's committed context:
- **Web UI:** the **Committed context** card in the task detail sidebar (`/tasks/<id>`) displays each reference's placement badge (`target repo` or `read-only repo`), repo-qualified path, a link to the exact tree at `@<short-sha>`, and any `context.yml` manifest entries and external source links.
- **CLI:** `grain get <id>` prints a `context:` summary line; `grain update <id> -design ...` updates a task's references (`-design ""` clears them).
- **HTTP API:** `POST /api/tasks` and `PATCH /api/tasks/{id}` accept `designs` (array of `[repo:]path[@rev]` strings) or `context` (array of `{repo, path, rev}` objects); `GET /api/tasks/{id}` returns structured `context` entries with `repo`, `path`, `rev`, `placement` (`"target"` or `"read"`), and optional `manifest`.

---

## Optional `context.yml` manifest

A committed-context directory may optionally include a `context.yml` file describing the files in the folder and where they originated.

```yaml
title: Theme Switcher Spec
description: Approved product brief, contrast tokens, and settings mock
files:
  - path: spec.md
    kind: spec
    title: Theme switch product brief
    source_url: https://specs.example.com/plc/theme-toggle
  - path: settings-mock.html
    kind: mock
    title: Settings pane layout mock
  - path: tokens.json
    kind: snapshot
    title: Pinned colour token snapshot
```

### Field reference

- `title` *(optional string)* — human-readable name for the context bundle.
- `description` *(optional string)* — short summary shown on the task card and in the agent prompt.
- `files` *(optional list)* — entries inside the context folder:
  - `path` *(required string)* — clean relative path within the context directory (`..` and leading `/` are rejected).
  - `kind` *(optional string)* — free-form label such as `spec`, `brief`, `mock`, `contract`, or `snapshot`.
  - `title` *(optional string)* — short label for the file.
  - `source_url` *(optional string)* — upstream URL for human traceability (`http://` or `https://` links are rendered as `source` links on the task page).

**Display and pass-through only:** grain reads `context.yml` during sandbox checkout (up to 64 KiB), attaches the parsed metadata to the task so the UI and API display it, and lists the entries in the agent's prompt. Grain never enforces approval gates on `context.yml` contents and never fetches `source_url`. Syncing documents from upstream planning tools into git belongs in the integration layer above grain.

---

## Inheritance into child tasks, reviews, and fixes

Committed context travels down a task's work tree automatically so reviews, repairs, and follow-up tasks always evaluate the **exact same pinned SHA** as the parent task:

- **Repairs on the same task** re-run against the task's stored `{repo, path, rev}` references.
- **Code reviews** (automatic PR/CL reviews and shepherd takeover reviews) inherit the reviewed task's committed-context references and required `-read` grants, so the reviewer checks the change against the same spec revision the author implemented.
- **Follow-up proposals** (`propose_task` MCP tool, `grain task propose`, HTTP child-task creation, PR path triggers, bypassed base-repair follow-ups, base-branch CI repairs, and branch ports) inherit the parent task's committed-context references and required `-read` grants **unless** the child is filed with its own explicit `-design` / `designs`.
- **Cross-repository children:** when a task in `acme/widgets` with target-repo context `design/plc/042@<sha>` spawns a child targeting `acme/docs`, grain automatically qualifies the inherited reference as `acme/widgets:design/plc/042@<sha>` and carries `-read acme/widgets` onto the child so the spec remains reachable in `./repos/acme/widgets/design/plc/042` at `<sha>`.

To override inherited context on a proposed child task, pass `--design` on `grain task propose` or `designs: [...]` in `propose_task`.

---

## Relationship to `Design-Item` / `Design-Rev` trailers

Committed context (`-design` / `context`) and design-doc progress trailers ([`docs/design-docs.md`](design-docs.md)) complement each other:
- **Committed context** hands any task (whether filed by an external planning integration, a human, or a trigger) pinned `{repo, path, rev}` spec references in either the target repo or a separate `-read` repo, verifies/clones them at that SHA in the sandbox, surfaces them in the prompt and UI, and carries them to child tasks and reviews.
- **`Design-Item:` / `Design-Rev:` commit trailers** are written into git commit messages when using the repository-local design planner (`pkg/designdoc`) to track individual `{#ID}` headings inside a repository's own design docs.
