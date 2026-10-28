# Grainfile v1 Quick Reference

**Normative specification:** [`docs/grainfiles-spec.md`](grainfiles-spec.md) — it wins wherever this page is briefer. Capability references are specified in [`docs/capabilities-spec.md`](capabilities-spec.md) §2.

**Overview:** Grainfiles are portable, version-controlled declarative Markdown documents for authoring one-shot tasks (`<stem>.task.grain.md`) and standing event-driven processes (`<stem>.process.grain.md`). Deployments watch repositories for them, can import them from issues and export them back (§9 of the spec), and execute them directly. A `.task.grain.md` dispatches one unit of work (optionally gated by `depends_on`, `not_before`, and requested `capabilities`) to a `recipient` deployment (default `"/"`, the enclosing repository's deployment); a `.process.grain.md` defines a reusable workflow or merge gate fired by repository, CI workflow, schedule, or manual events (`when`).

## 1. File Naming & Templates (`GF001`, `GF005`, `GF006`, `GF033`, `GF034`)
- **One-shot task:** `<stem>.task.grain.md` — executes once on its `recipient` deployment when `depends_on`, `not_before`, and requested `capabilities` are satisfied.
- **Standing process:** `<stem>.process.grain.md` — 1-to-many workflow or merge gate (`gate: true`) that fires whenever `when` matches.
- **Plain `.grain.md` files are 100% literal:** no template evaluation; `{{ ... }}` in prose or code blocks is literal text.
- **Jinja2 templates:** `<stem>.task.grain.md.jinja2` and `<stem>.process.grain.md.jinja2` are rendered as a whole file (front matter and body) in a sandboxed Jinja2 environment with no loader (Jinja2 ≥ 3.1.6, bounded render time) to produce a `.task.grain.md` / `.process.grain.md` that must then pass the `v1` validator. `{% include %}`, `{% extends %}`, `{% import %}`, `{% from %}` are rejected (`GF034`); syntax/evaluation errors or timeouts are rejected (`GF033`); raw and rendered size must fit the limits (`GF006`). Only templates read from a repository are rendered — never one carried in an issue. Undeclared variables are listed in sorted order (`template_vars`).
- `<stem>` MUST match `^[A-Za-z0-9][A-Za-z0-9._-]*$` (`GF001`). Two paths in one repository MUST NOT differ only in ASCII case, and path/size limits apply (`GF006`).
- A `.grain.md` or `.grain.md.jinja2` file with an unregistered `<kind>` (e.g. `foo.unknown.grain.md`) is an asset and warns with `GF005`; ordinary `.md` files never do.

## 2. Front Matter (`---` YAML `---` + Optional Markdown Body)
YAML 1.2 Core Schema, single mapping document; no anchors `&`, aliases `*`, merge keys `<<`, or tags `!` (`GF004`); missing or unclosed `---`, invalid UTF-8, NUL, BOM, or Git LFS pointer is `GF003`. Unknown non-`x_*` keys warn (`GF022`); vendor extensions MUST start with `x_`. The Markdown body after the closing `---` is the prompt and **may be empty** when the front matter carries everything.

The `grainfile: v1` vocabulary is **12 fields** (plus `x_*` extensions):
- **Shared by both kinds (7):** `grainfile` (`"v1"`, **required**, `GF020`), `title` (**required**, `GF020`), `draft`, `not_before`, `capabilities`, `references`, `from_template`
- **`.task.grain.md` only (3):** `recipient` (default `"/"`), `requestor`, `depends_on` (targets must be concrete `.task.grain.md` files; `GF032`, `GF034`)
- **`.process.grain.md` only (2):** `when` (**required**), `gate` (default `false`)

| Field | Kind | Type | Requirement / Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `grainfile` | both | `"v1"` | **Required** | Format version on every grainfile and rendered template; missing or unsupported fails (`GF020`). |
| `title` | both | `string` | **Required** | Single-line summary, 1–200 characters (`GF020`). |
| `draft` | both | `boolean` | `false` | When `true`, indexed for inspection only; never executes or grants `references` access. |
| `not_before` | both | `string` | *(none)* | Earliest activation as an RFC 3339 `date-time` (`2026-10-25T09:00:00Z`, `2026-10-25T09:00:00+02:00`; normalized to UTC; anything else is `GF020`). |
| `capabilities` | both | `(string \| {capability: string, with?: object})[]` | `[]` | Capability grants: `<capability_id>[#<method>]` as a string, or `{capability: "<capability_id>[#<method>]", with: {<args>}}`; grammar per [capabilities-spec §2.1](capabilities-spec.md) (`GF020`). |
| `references` | both | `string[]` | `[]` | Read-only context files/folders (`https://...`, `/repo-rooted`, `./relative`, `../relative`). |
| `from_template` | both | `string` | *(none)* | Provenance link to the `.task.grain.md.jinja2` / `.process.grain.md.jinja2` the file was rendered from, matching its kind (`GF035`); auto-populated on rendering. |
| `recipient` | task | `string` | `"/"` | Target deployment: forge repository/folder URL (`https://...`) or repo-local path. |
| `requestor` | task | `string` | *(none)* | Attribution (`Name <email@domain>` or an `https://` user/team/deployment URL or repo-local folder path). |
| `depends_on` | task | `string[]` | `[]` | Prerequisite concrete `.task.grain.md` files (`GF032`), acyclic (`GF034`). |
| `when` | process | `object` | **Required** | Trigger map: `schedule`, `push`, `pull_request`, `merge`, `workflow`, `manual`, `scenario`. |
| `gate` | process | `boolean` | `false` | Merge-gate on `pull_request` / `push` / `merge` / `workflow` triggers. |

### Capabilities
- `<capability_id>` is an `https://` URL with a non-empty host and at least one path segment (no query, userinfo, `..`, or trailing `/`); `#<method>` matches `^[a-z0-9][a-z0-9_-]{0,62}$` and may be omitted when the URL's last path segment names the method (or the server offers exactly one). `with:` is validated by the executing deployment against the server's advertised argument schema; unknown keys on the entry object are `GF020`. Never put secrets in `with:`.

### Timestamps, Timezones, and Schedules (`GF020`)
- **`not_before`:** RFC 3339 `date-time` only.
- **`when.schedule`:** a `{ cron, timezone? }` object or a non-empty list of them. `cron` is five-field POSIX. `timezone` defaults to `"UTC"` and must be `"UTC"` or an IANA zone (`"America/Los_Angeles"`); numeric offsets and abbreviations (`"PST"`) are rejected. In non-UTC zones cron is evaluated on local wall-clock time: a spring-forward gap minute does not fire (or fires once at the boundary), a fall-back repeated minute fires once.

### `when` Trigger Sources (`.process.grain.md` only)
- `schedule`: `{ cron: "0 2 * * *", timezone?: "UTC" }` or a non-empty list.
- `push`, `pull_request`, `merge`: a filter object or non-empty list of them; each supports `repo?` / `repos?` (`owner/repo` or `https://github.com/owner/repo`; default the process's own repository; cross-repo only within the same team or owner), `branches?` (names or globs), `paths?` (repo-relative files, folders `dir/` or `/dir`, globs, or forge `https://` file/folder URLs), `ignore?`; at least one of `repo`, `repos`, `branches`, `paths` is required.
- `workflow`: a filter object or non-empty list; `workflow?` / `workflows?` (name, glob, or workflow file path; at least one required), `events?` (`"triggered" | "succeeded" | "failed"`; aliases `started`/`success`/`failure`; default `["succeeded", "failed"]`), `repo?` / `repos?`, `branches?` (matched against the run's head branch).
- `manual: {}`
- `scenario: "Description"` or `scenario: { description: "..." }`

## 3. Minimal Copy-Paste Examples

**`.task.grain.md`:**
```markdown
---
grainfile: v1
title: Migrate widget schema to v2
recipient: /
requestor: Platform Team <platform@example.com>
not_before: "2026-10-25T09:00:00+02:00"
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
      root: true   # this checkout is the run's root working directory; additional repos use root: false
references:
  - /docs/widget-schema.md#v2
depends_on:
  - ./prepare-schema.task.grain.md
---
Migrate the widget table to v2 following `/docs/widget-schema.md#v2` and run all unit tests.
```

**`.process.grain.md`:**
```markdown
---
grainfile: v1
title: Security review and CI failure triage
gate: true
capabilities:
  - https://github.com/bwsalmon/grain/capabilities/self-debug
references:
  - /docs/security-checklist.md
when:
  pull_request:
    branches: [main]
    paths: ["schemas/**", "db/**"]
  workflow:
    workflows: ["grain CI", "e2e-*", ".github/workflows/release.yml"]
    events: ["triggered", "succeeded", "failed"]
    branches: ["main", "release/*"]
  schedule:
    - cron: "0 9 * * 1-5"
      timezone: "America/Los_Angeles"
    - cron: "0 9 * * 1-5"
      timezone: "Europe/Berlin"
  manual: {}
---
Review the pull request diff or failing CI workflow run against `/docs/security-checklist.md`.
```

**`.task.grain.md.jinja2`:**
```markdown
---
grainfile: v1
title: Roll out widget service to {{ region | default("eu-west-1") | upper }}
recipient: {{ recipient_path | default("/") }}
depends_on:
  - ./prepare-schema.task.grain.md
---
Deploy widget service to `{{ region | default("eu-west-1") }}` in `{{ env | default("staging") | lower }}`.
```

## 4. Core Rules & Invariants
- **Where a grainfile may come from (spec §9.1, §11.1):** a `.task.grain.md` in a repository executes only from the default or a protected branch, never from a pull-request head or feature branch. A `pull_request`-triggered process is read from the base branch, and a run for a fork or non-collaborator pull request gets no capability grants unless the repository opts in. An issue-sourced grainfile runs only if its author has write access (or is allowlisted), never has its `capabilities` auto-approved, and may only name its own repository as `recipient`. Exports go to non-default branches or carry `draft: true`, carry an origin marker, and contain no secrets.
- **References (spec §8, §11.2–11.3):** a repository can grant read access only to its own files at the grainfile's pinned commit SHA (`authorized_references`, including same-repo `references` and `from_template`); fetched objects must be regular files or trees. A cross-repository reference is materialized only if the target is public, shares the source repository's team or owner, or the pair is allowed by policy. Executors keep references and diffs structurally separate from the prompt.
- **Idempotency & withdrawal (spec §9.3):** a `.task.grain.md` identified by `(repo_url, file_path)` executes once; edits after completion do nothing; edits before completion update it in place. Deleting an incomplete task or setting `draft: true` cancels it (dependents see it as cancelled); deleting or drafting a process unregisters its trigger and gate.
- **1-to-1 tasks vs 1-to-many processes:** a task targets one `recipient` and has one terminal state; a process has no `recipient`, no `requestor`, no `depends_on`, and no single completion — multi-step coordination lives in its prompt.
- **`depends_on` & `capabilities`:** `depends_on` is task-only (`GF020` on a process), each target a concrete `.task.grain.md` (`GF032`), acyclic (`GF034`). `capabilities` is valid on both kinds with the grammar above (`GF020`).

## 5. Lint Codes (`GF001`–`GF036`, spec §10)
| Code | Severity | Condition |
| :--- | :--- | :--- |
| `GF001` | error | Stem violates `^[A-Za-z0-9][A-Za-z0-9._-]*$` |
| `GF003` | error | Malformed file: missing or unclosed `---` front matter, invalid UTF-8, NUL byte or BOM, Git LFS pointer, or unparseable YAML |
| `GF004` | error | Front matter outside the YAML subset: not a mapping, non-string or duplicate key, anchor `&`, alias `*`, merge key `<<`, or tag `!` |
| `GF005` | warning | Unregistered `.<kind>.grain.md` or `.<kind>.grain.md.jinja2` suffix |
| `GF006` | error | Path or file/front-matter size limit exceeded (including rendered `.jinja2` output), or two paths differing only in case |
| `GF020` | error | Schema failure: missing/invalid `grainfile: v1`, missing `title` or `when`, field on the wrong kind, wrong type, multiline or over-long `title`, invalid identity, invalid capability reference or `with`, invalid RFC 3339 `not_before`, unknown trigger source, invalid cron/timezone or trigger filter |
| `GF022` | warning | Unknown top-level key not prefixed with `x_` |
| `GF030` | error | A reference does not resolve: missing file, directory, or `#<anchor>` heading |
| `GF032` | error | Wrong target kind: `depends_on` target is not a concrete `.task.grain.md`, or a deployment identity resolves to a file |
| `GF033` | error | Jinja2 syntax, evaluation, or render-time error in a `.grain.md.jinja2` |
| `GF034` | error | Cycle in `depends_on`, or forbidden Jinja2 tag (`include`, `extends`, `import`, `from`) |
| `GF035` | error | Malformed reference or `from_template`: not `https://` (or `file://` in tests) or a local path, `git::` prefix, `//`, query string, anchor on a deployment identity or template, or template suffix not matching the grainfile's kind |
| `GF036` | error | A relative reference climbs above the repository root |
