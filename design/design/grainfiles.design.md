> Copied from `bwsalmon/silo` at commit `f27fbd48ed065d3e9ab7fd240e692eb5a7dafb52` (`docs/grainfiles.md`).

# The Grainfile Format: Rationale & Overview

**Status: design for review (bsalmon@google.com).** The worked examples are in [`examples/grainfiles/`](https://github.com/bwsalmon/silo/blob/main/examples/grainfiles/README.md).

This document explains the design rationale for the Grainfile Format (`grainfile: v1`). The concise agent quick-reference is **[`docs/grainfile-v1.md`](../spec/grainfiles/grainfile-v1.md)**, and the normative specification is **[The Grainfile Format](../spec/grainfiles/grainfiles.spec.md)** (`docs/grainfiles-spec.md`), accompanied by its JSON Schemas ([`schemas/grainfiles/`](../spec/grainfiles/schemas)) and reference linter ([`tools/grainfiles/grainlint.py`](https://github.com/bwsalmon/silo/blob/main/tools/grainfiles/grainlint.py)). Where this document and the specification ever disagree, the specification wins.

**Use Case & Overview:** Grainfiles are portable, version-controlled, declarative Markdown documents (`.task.grain.md` and `.process.grain.md`) that coordinate one-shot tasks and standing event-driven processes across Grain deployments. **Grain makes it easy to export and import Grainfiles either from and to Git repositories or by parsing issues.** A `.task.grain.md` dispatches a discrete unit of work (optionally gated by `depends_on`, `not_before`, and requested `capabilities`) to a target `recipient` deployment (defaulting to `"/"` for the source repository's deployment, or a forge repository/folder URL `https://...` or repo-local folder path), while a `.process.grain.md` defines a reusable automation workflow or merge gate triggered by repository, CI workflow, schedule, or manual events (`when`). Each capability server is a standalone Streamable HTTP MCP server (`POST /mcp`) identified by an `https://` URL (`<capability-url>[#<method>]`, such as `https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key`), configured in Grain by mapping `(capability_id, port)`, and `.task.grain.md` and `.process.grain.md` files declare them via `capabilities:` (`<capability-url>[#<method>]` or `{capability: "<capability-url>[#<method>]", with: {<args>}}`). Grain deployments watch or import Grainfiles from Git repositories or by parsing issues and automatically run matching tasks and processes when their triggers, dependencies, and capabilities are satisfied.

## Contents

1. Purpose, versioning (`grainfile: v1`), and scope
2. Two kinds executed directly: `.task.grain.md` and `.process.grain.md`
3. Source authority and task identities: `recipient` (default `/`) and optional `requestor`
4. References, `capabilities`, and `depends_on` target rules
5. Triggers (`when`) and merge gating (`gate`)
6. Execution, import, export, task lifecycle, and security invariants
7. Prior art and design evolution

## 1. Purpose, versioning (`grainfile: v1`), and scope

A Grainfile is a portable, version-controlled declarative Markdown file with YAML front matter (`**/*.grain.md`). Its goal is to let people and deployments author work in ordinary git repositories—reviewed through standard pull requests and protected branches—and to make it easy for Grain to **export and import Grainfiles either from and to Git repositories or by parsing issues**, in a form that a Grain deployment can pick up and run directly without an intermediate translation layer.

Each Grainfile must declare `grainfile: v1` (mandatory; `GF020` when omitted), which maps programmatically in APIs, SDKs, and UI responses to:
- `spec_summary_path`: [`docs/grainfile-v1.md`](../spec/grainfiles/grainfile-v1.md) (~50-line low-token agent quick reference)
- `spec_path`: [`docs/grainfiles-spec.md`](../spec/grainfiles/grainfiles.spec.md) (full normative specification)
- `schemas/grainfiles/`: JSON Schemas for `common`, `task`, and `process`

Everything needed to specify **what to run, which deployment runs a task, optional requestor/template attribution, when a process triggers across repositories and paths, what capabilities it requests, what it depends on, and what references it reads** lives in two canonical file suffixes with ASCII stems (`^[A-Za-z0-9][A-Za-z0-9._-]*$`):

- **`<name>.task.grain.md` (`task`):** A discrete 1-to-1 work item directed to a single `recipient` (defaulting to `/`) to execute once per canonical `(repo_url, file_path)` on the repository's default/protected branch (or imported by parsing an issue).
- **`<name>.process.grain.md` (`process`):** A standing, reusable 1-to-many process with structured triggers (`when`) and an optional `gate` flag that any number of Grain deployments can subscribe to or import (by repository, folder, or `.process.grain.md` URL).

Everything that belongs to an execution engine's runtime—live task states, queue ordering, sandbox sizing, model routing, and run logs—stays in the deployment's database rather than being committed back and forth into git. Similarly, there is no `created_at` field in front matter because git commit metadata already tracks creation and modification timestamps.

## 2. Two kinds executed directly: `.task.grain.md` and `.process.grain.md`

Earlier drafts of the format had eleven kinds (`config`, `task`, `comment`, `template`, `trigger`, `gate`, `capability`, `design`, `lifecycle`, `moved`, `record`), container folding (`<name>.task/`), Jinja2 template rendering, section SHA-256 requirement hashes, custom `GRAINOWNERS` syntax, and `grain.config.md` alias dirhashes.

Collapsing the format to portable **`.task.grain.md`** and **`.process.grain.md`** (`**/*.grain.md`) documents—which Grain can easily export and import either from and to Git repositories or by parsing issues—eliminates that indirection:

- **Distinct `.grain.md` suffix.** Using `.task.grain.md` and `.process.grain.md` allows fast `**/*.grain.md` glob discovery without colliding with ordinary prose documentation (`*.md`), while supporting clean ASCII stems `[A-Za-z0-9][A-Za-z0-9._-]*`.
- **No separate `trigger`, `template`, or `gate` kinds.** A standing job or review check is one `<name>.process.grain.md` file: its front matter says when it fires (`when`) and whether it gates a commit or pull request (`gate: true`), and its Markdown body *is* the prompt given to the agent.
- **No separate `config` or `GRAINOWNERS` files.** Any repository can hold `.task.grain.md` and `.process.grain.md` files (for example under `.grain/` in a code repository or in a dedicated tasks repository) without needing a marker file, and standard forge branch protection and `CODEOWNERS` govern who can merge changes.

## 3. Source authority and task identities: `recipient` (default `/`) and optional `requestor`

The **authoritative source** of a repository-backed `.task.grain.md` or `.process.grain.md` is the URL of the repository and folder where the file itself lives (spec §6.2, §11.1). Because a front-matter string can be written by anyone who can propose a commit, trust comes from the repository's branch protection, merge policy, and `CODEOWNERS` reviews—not from an unverified string inside YAML front matter.

Accordingly, `.task.grain.md` files (1-to-1 work items) separate routing from optional attribution, while `.process.grain.md` files (1-to-many reusable processes) omit both `recipient` and `requestor` so any subscribed deployment can run them:

- **`recipient` (`.task.grain.md` only; optional, defaults to `"/"`):** Which deployment is intended to execute the task. When omitted, `recipient` defaults to `"/"`, meaning the root of the enclosing repository where the task lives. When set explicitly, it is a **deployment identity**: a plain `https://` URL naming the git repository or folder that the deployment controls (for example, `https://github.com/acme/grain-eu`), or a repo-local relative / `/`-rooted folder path. Rejected on `.process.grain.md` (`GF020`).
- **`requestor` (`.task.grain.md` only; optional):** Attribution metadata recording who requested or owns the task. Rejected on `.process.grain.md` (`GF020`). When present on a task, this may be either:
  - A **user identity** in standard RFC 5322 / git author notation (`Bruce Salmon <bsalmon@google.com>`), or
  - A **deployment / URL identity** (`https://github.com/acme/grain-eu`).

## 4. References, `capabilities`, and `depends_on` target rules

Grainfiles use two repository/forge reference lists (`references` and `depends_on`), sharing the same plain `https://` and repo-local reference syntax (spec §8.1), plus an optional `capabilities` list of Streamable HTTP MCP capability URL references (spec §6.6):

- **Cross-repository references (`references`, `depends_on`)** use standard clickable forge URLs (`https://github.com/acme/widgets/blob/main/docs/widget-schema.md#schema-version-header`, `https://github.com/acme/widgets/tree/main/src`).
- **Repository-local references (`references`, `depends_on`)** use `/`-rooted paths from the repository root (`/docs/widget-schema.md`, `/src/widget.go`) or relative paths (`./2026-10-16-widget-schema-v2.task.grain.md`).
- **Capabilities (`capabilities` on `.task.grain.md` and `.process.grain.md`):** Each capability server is a standalone Streamable HTTP MCP server (`POST /mcp`) identified by an `https://` URL (`<capability-url>[#<method>]`, such as `https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key`), configured in Grain by mapping `(capability_id, port)` and embedding its metadata (`name`, `description`, `inputSchema`, `_meta`) in the MCP server itself (there is no `.capability.grain.md` file kind, and OCI image URLs such as `https://ghcr.io/bwsalmon/grain/capabilities/self-debug:latest` are only an optional naming convention rather than an OCI-specific grammar). Each entry in `capabilities:` is either:
  1. A URL string (`<capability-url>[#<method>]`) when no arguments are passed (e.g. `https://github.com/bwsalmon/grain/capabilities/self-debug` or `https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key`), or
  2. An object (`{capability: "<capability-url>[#<method>]", with: {<args>}}`) when passing structured arguments to the capability method (validated against `https://<host>/<path>[#<method>]` with `GF020`):
     ```yaml
     capabilities:
       - https://github.com/bwsalmon/grain/capabilities/self-debug
       - capability: https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key
         with:
           account: infra-staging
       - capability: https://github.com/bwsalmon/grain/capabilities/github#git-repo
         with:
           repo: bwsalmon/grain
           mode: write
           base: main
     ```

### `depends_on` target rules (spec §7, §8.3) and `.grain.md.jinja2` templates (spec §5.7)

- **`depends_on` is restricted exclusively to `.task.grain.md` (`GF020` on `.process.grain.md`):** A `.task.grain.md` is a concrete 1-off task with a single identity `(repo_url, file_path)`, a single `recipient` deployment, and a single terminal `done` state — so `depends_on` between concrete `.task.grain.md` files is a well-defined DAG (`GF034` on cycles). A `.process.grain.md` is a standing 1-to-many trigger (`when`, `gate`, `references`, `draft`, `not_before`) with no `recipient` and no single completion state; multi-step or cross-task coordination inside a process is expressed in natural language in the `.process.grain.md` prompt itself (where the executing agent can run steps or propose/chain sub-tasks locally) and via `references`.
- **Every `depends_on` entry on a `.task.grain.md` MUST resolve to a concrete `.task.grain.md` file (`GF032`):** Never a `.process.grain.md` and never an unrendered `.jinja2` template.
- **Plain `.grain.md` vs. self-contained `.grain.md.jinja2` templates and `from_template` provenance:** Plain `<name>.task.grain.md` and `<name>.process.grain.md` files are 100% literal (zero template evaluation). Self-contained `<name>.task.grain.md.jinja2` and `<name>.process.grain.md.jinja2` templates apply sandboxed Jinja2 (`ImmutableSandboxedEnvironment(loader=None)`, rejecting `include`/`extends`/`import`/`from` with `GF034` and syntax errors with `GF033`) to the whole file to produce a standard `.task.grain.md` or `.process.grain.md` document. Concrete Grainfiles may also specify **`from_template`** (normalized like `references` / `depends_on` and validated with `GF035` to end in `.task.grain.md.jinja2` or `.process.grain.md.jinja2`), which is included in `authorized_references` at `commit_sha` when same-repo or recorded as provenance metadata when cross-repo, and is auto-populated when rendering a `.grain.md.jinja2` template.

## 5. Triggers (`when`) and merge gating (`gate`)

A `.process.grain.md` specifies when it fires using **`when`** (required on every `.process.grain.md`; spec §7.2.1). Using `when` rather than `on` ensures that YAML 1.1 parsers never misparse an unquoted `on:` key as boolean `true`:

- **`schedule: {cron, timezone?}` (or non-empty list of `{cron, timezone?}` objects):** Standard five-field POSIX cron expression with an optional IANA Time Zone Database identifier (default `"UTC"`, e.g. `"America/Los_Angeles"`, `"Europe/Berlin"`; unknown names and 3-letter abbreviations like `"PST"`/`"EST"` are rejected with `GF020`).
- **`push`, `merge`, `pull_request`:** Either a single filter object or a non-empty list of filter objects, where each filter supports `repo` / `repos` (repository URL or `owner/repo` slug, defaulting to the process's repository), `branches`, `paths` (file paths, folder paths `dir/` or `/dir` matching `dir/**`, globs, or full forge `https://` file/folder URLs), and `ignore` (at least one of `repo`, `repos`, `branches`, or `paths` is required; `GF020`).
- **`workflow`:** Either a single workflow filter object or a non-empty list of workflow filter objects (`GF020`), where each filter specifies `workflow` and/or `workflows` (workflow name, glob, or `.github/workflows/*.yml` / `.grain/workflows/*.yml` path; required, `GF020`), optional `events` (`["triggered", "succeeded", "failed"]`, defaulting to `["succeeded", "failed"]`), optional `repo` / `repos`, and optional `branches`.
- **`manual: {}`:** On-demand firing by a human or operator.
- **`scenario: <string> | {description}`:** A natural-language scenario description specifying when the process applies.

Setting **`gate: true`** on a `.process.grain.md` marks the process as a merge/commit gate: when triggered by a commit or pull request (including cross-repo within the same team or owner), the deployment holds that change until the process run passes.

## 6. Execution, import, export, task lifecycle, and security invariants

Grainfiles are portable, version-controlled declarative Markdown documents (`.task.grain.md` and `.process.grain.md`). **Grain makes it easy to export and import Grainfiles either from and to Git repositories or by parsing issues**, and deployments watch or import from their repositories and issues for `.task.grain.md` files targeting their `recipient` identity and `.process.grain.md` files matching their configured repositories, folders, or process URLs:

1. **Export and import from Git repositories or by parsing issues (spec §9):**
   - Grain deployments and tools can import `.task.grain.md` and `.process.grain.md` files directly from Git repositories or synthesize/import them by parsing issues, and export tasks and standing processes back to Git repositories as standard `.task.grain.md` and `.process.grain.md` documents.
2. **`.task.grain.md` lifecycle idempotency, edits, and deletion (spec §9.3):**
   - A task's identity is its canonical `(repo_url, file_path)`.
   - Once completed, subsequent pushes do not re-run it.
   - Editing a queued or deferred task updates its indexed definition in place.
   - Deleting a queued or running task (or setting `draft: true`) emits a removal event and cancels it; deleting a completed task is a no-op.
3. **Base-branch execution, issue import validation, & `pull_request` trigger safety (spec §9, §11.1):**
   - Repository-backed `.task.grain.md` files execute only from default/protected branches, never from unmerged pull-request heads, and Grainfiles imported by parsing issues are validated against the `grainfile: v1` schema and capability/reference policies before execution.
   - On `when: {pull_request: ...}`, the `.process.grain.md` definition (prompt, `when`, `gate`, `references`) is always read from the target base branch, never from the untrusted pull-request head.
4. **Repository & `recipient` authorization (spec §11.2):**
   - Deployments verify authorization for the repository of any `recipient`, `repos`, `folders`, or `grainfiles` URL they watch, import, fetch references for, or record completion for.
5. **Commit-SHA pinning & regular-file check (spec §11.3):**
   - `authorized_references` are pinned to the exact commit SHA at which the Grainfile was indexed or imported, and repository content fetchers reject `symlink` and `submodule` git objects.
6. **Bounded `depends_on` DAG depth (spec §11.4):**
   - `depends_on` evaluation enforces a visited set (`GF034`) and a maximum transitive depth of 16.
7. **Prompt/reference separation (spec §11.5):**
   - Executors structurally delimit the Grainfile Markdown body (trusted instructions) from materialized `references` and PR diffs (tagged with source URL and commit SHA).

## 7. Prior art and design evolution

- **Markdown with YAML front matter** (Jekyll, Hugo, Obsidian) for human-readable, reviewable files in git and issue bodies.
- **Standard forge `https://` URLs** (`/blob/<ref>/<path>`, `/tree/<ref>/<path>`, `/-/blob|tree/<ref>/<path>`, `/+/<ref>/<path>`) for clickable cross-repository references, deployment identities, and Streamable HTTP MCP capability identifiers (`<capability-url>[#<method>]`).
- **Git author notation** (`Name <email>`) for optional user attribution.
- **POSIX `crontab(5)` and GitHub Actions path/branch filters** for `when:` trigger sources.
- **Evolution from earlier drafts:** Tasks 4446–4635 explored storing full runtime state, backlogs, lanes, and execution folders in git; Task 4658 narrowed the format to an authoring language with eleven kinds; the `grainfile: v1` specification finalizes the two-kind `.task.grain.md` and `.process.grain.md` format with `when:` triggers, `<capability-url>[#<method>]` + `with:` capability references, default `/` task recipient, 1-to-many reusable processes, repo/issue import and export, deterministic task lifecycle rules, and execution security invariants.
