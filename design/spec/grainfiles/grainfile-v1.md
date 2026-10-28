> Copied from `bwsalmon/silo` at commit `f27fbd48ed065d3e9ab7fd240e692eb5a7dafb52` (`docs/grainfile-v1.md`).

# Grainfile v1 Quick Reference (`grainfile: v1`)

**Use Case & Overview:** Grainfiles are portable, version-controlled declarative Markdown documents (`.task.grain.md` and `.process.grain.md`) for authoring one-shot tasks and standing event-driven processes. **Grain makes it easy to export and import Grainfiles either from and to Git repositories or by parsing issues.** A `.task.grain.md` dispatches a discrete unit of work (optionally gated by `depends_on`, `not_before`, and requested `capabilities`) to a target `recipient` deployment (defaulting to `"/"` for the source repository's deployment, or a forge repository/folder URL `https://...` or repo-local folder path), while a `.process.grain.md` defines a reusable automation workflow or merge gate triggered by repository, CI workflow, schedule, or manual events (`when`). Each capability server is a standalone Streamable HTTP MCP server (`POST /mcp`) identified by an `https://` URL (`<capability-url>[#<method>]`, such as `https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key`), configured in Grain by mapping `(capability_id, port)` and embedding its metadata (`name`, `description`, `inputSchema`, `_meta`) in the MCP server itself, and `.task.grain.md` and `.process.grain.md` files declare them via `capabilities:` (`<capability-url>[#<method>]` or `{capability: "<capability-url>[#<method>]", with: {<args>}}`). Grain deployments watch or import Grainfiles from Git repositories or by parsing issues and execute matching tasks and processes when their triggers, dependencies, and capabilities are satisfied.

## 1. File Naming & Templates (`GF001`–`GF006`, `GF033`, `GF034`)
- **Portable declarative Markdown documents (repo & issue import/export):** Grainfiles (`.task.grain.md` and `.process.grain.md`) are portable, version-controlled declarative Markdown documents that Grain makes it easy to export and import either from and to Git repositories or by parsing issues.
- **One-shot task:** `<stem>.task.grain.md` — `kind = "task"`: executes once on its `recipient` deployment when dependencies (`depends_on`), `not_before`, and requested `capabilities` are satisfied.
- **Recurring/event process:** `<stem>.process.grain.md` — `kind = "process"`: standing 1-to-many workflow or merge gate (`gate: true`) that executes whenever its `when` condition fires.
- **Plain `.grain.md` files are 100% literal:** Zero template evaluation is performed on `<stem>.task.grain.md` or `<stem>.process.grain.md`; `{{ ... }}` inside Markdown or code blocks is literal text.
- **Jinja2 Grainfile templates:** `<stem>.task.grain.md.jinja2` and `<stem>.process.grain.md.jinja2` apply sandboxed Jinja2 (`ImmutableSandboxedEnvironment(loader=None)`) as-is to the entire file (parameterizing both YAML front matter and Markdown body) to produce a `<stem>.task.grain.md` or `<stem>.process.grain.md` document, which is then validated by the standard `v1` Grainfile validator (including the mandatory `grainfile: v1` field).
  - Forbidden Jinja2 tags (`{% include %}`, `{% extends %}`, `{% import %}`, `{% from %}`) are rejected (`GF034`).
  - Jinja2 syntax or evaluation errors are rejected (`GF033`), and both raw and rendered size must fit within `MaxFileBytes` (`GF006`).
  - Undeclared variables are extracted from the AST in deterministic sorted order (`template_vars`).
- `<stem>` MUST match `^[A-Za-z0-9][A-Za-z0-9._-]*$` (uppercase and lowercase ASCII letters, digits, `.`, `_`, `-`, starting with an ASCII letter or digit; `GF001`). Two Grainfiles in the same repository MUST NOT collide when compared case-insensitively (`GF006`).
- Files ending in `.grain.md` or `.grain.md.jinja2` with an unknown `<kind>` suffix (e.g. `foo.unknown.grain.md`) warn with `GF005`. Ordinary `.md` files without `.grain.md` (such as `README.md` or `docs/design.md`) are regular Markdown assets and do not trigger `GF005`.

## 2. Front Matter (`---` YAML `---` + Optional Markdown Body)
Standard YAML 1.2 Core Schema (`GF004`: no anchors `&`, aliases `*`, merge keys `<<`, or tags `!`). Unknown non-`x_*` keys warn (`GF022`); vendor extensions MUST start with `x_`. The Markdown body after the closing `---` delimiter is **optional and may be empty** when the front matter (`title`, `from_template`, `references`, `when`, etc.) carries everything the task or process requires. Because Grainfiles are portable declarative Markdown documents, **Grain makes it easy to export and import Grainfiles either from and to Git repositories or by parsing issues**.

The finalized `grainfile: v1` front-matter vocabulary consists of **12 fields** (plus `x_*` extensions):
- **Shared across both kinds (`task`, `process`) (7):** `grainfile` (`"v1"`, **required**, `GF020`), `title` (**required**, `GF020`), `draft`, `not_before`, `capabilities` (optional list of Streamable HTTP MCP capability URL references `<capability-url>[#<method>]` or `{capability: "<capability-url>[#<method>]", with: {<args>}}`; `GF020`), `references`, `from_template` (optional reference to a `.grain.md.jinja2` template of the **same kind**: `.task.grain.md.jinja2` on `task`, `.process.grain.md.jinja2` on `process`; `GF035`)
- **`.task.grain.md` only (3):** `recipient` (optional, default `"/"`), `requestor` (optional), `depends_on` (optional; targets must resolve to concrete `.task.grain.md` files, never `.process.grain.md` or unrendered `.jinja2` templates; `GF032` / `GF034`)
- **`.process.grain.md` only (2):** `when` (**required**), `gate` (optional, default `false`)

| Field | Kind | Type | Requirement / Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `grainfile` | both | `"v1"` | **Required** (`"v1"`) | Mandatory format version identifier on every `.task.grain.md` and `.process.grain.md` (and rendered `.grain.md.jinja2` output). Omitting `grainfile` or specifying an unsupported version fails validation (`GF020`). |
| `title` | both | `string` | **Required** | Single-line human-readable summary (`1..200` chars). |
| `draft` | both | `boolean` | `false` | When `true`, indexed for inspection only; never executes or grants `references` access. |
| `not_before` | both | `string` | *(none)* | Earliest activation time: RFC 3339 timestamp (`date-time`, e.g. `2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`, normalized to UTC; naive timestamps and bare dates fail with `GF020`). |
| `capabilities` | both | `(string \| {capability: string, with?: object})[]` | `[]` | Requested Streamable HTTP MCP (`POST /mcp`) capabilities (`https://<host>/<path>[#<method>]`, configured in Grain by mapping `(capability_id, port)`), either as a URL string (`<capability-url>[#<method>]`) or as `{capability: "<capability-url>[#<method>]", with: {<args>}}` (`GF020`). |
| `references` | both | `string[]` | `[]` | Read-only context files/folders (`https://...`, `/repo-rooted`, `./relative`, `../relative`). |
| `from_template` | both | `string` | *(none)* | Optional provenance link to the source `.task.grain.md.jinja2` or `.process.grain.md.jinja2` template matching the Grainfile's kind (`GF035`). Same-repo templates are added to `authorized_references` at `commit_sha`; auto-populated on `.grain.md.jinja2` rendering. |
| `recipient` | task | `string` | `"/"` | Target Grain deployment identity: forge repository/folder URL (`https://...`) or repo-local path (defaults to `"/"`). |
| `requestor` | task | `string` | *(none)* | Attribution (`Name <email@domain>` or `https://...` user/team/deployment URL or repo-local folder path). |
| `depends_on` | task | `string[]` | `[]` | Prerequisite concrete `.task.grain.md` files that must complete before this task runs (`GF032`). |
| `when` | process | `object` | **Required** | Trigger map: `schedule`, `push`, `pull_request`, `merge`, `workflow`, `manual`, `scenario`. |
| `gate` | process | `boolean` | `false` | Advisory merge-gate signal on `pull_request` / `push` / `merge` / `workflow` triggers. |

### Timestamps, Timezones, and Schedules (`GF020`)
- **RFC 3339 timestamps (`not_before` and timestamp metadata):** MUST be valid RFC 3339 `date-time` strings with an explicit timezone designator — either `Z` (UTC) or a numeric UTC offset `+HH:MM` / `-HH:MM` (`00 <= HH <= 23`, `00 <= MM <= 59`, e.g. `2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`). Naive timestamps without an offset (e.g. `2026-10-25T09:00:00`) and bare calendar dates (`YYYY-MM-DD`) are rejected (`GF020`). All parsed timestamps are converted to and compared in UTC (`Z`).
- **Cron schedules (`when.schedule`):** Accepts either a single `{ cron, timezone? }` object or a non-empty list of `{ cron, timezone? }` objects.
  - `timezone` defaults to `"UTC"` when omitted and MUST be `"UTC"` or a valid IANA Time Zone Database identifier (e.g. `"America/Los_Angeles"`, `"Europe/Berlin"`, `"Asia/Tokyo"`). Numeric offsets (e.g. `"+02:00"`), unknown names, and non-IANA abbreviations (e.g. `"PST"`, `"EST"`, `"CET"`) are rejected (`GF020`).
  - **DST transitions:** For non-UTC IANA zones, cron fields are evaluated against local wall-clock time in that zone; during spring-forward gaps a nonexistent wall-clock minute does not fire (or fires once at the transition boundary per scheduler policy), and during fall-back overlaps an ambiguous wall-clock minute fires only on its first occurrence.

### `when` Trigger Sources (`.process.grain.md` only)
- `schedule`: `{ cron: "0 2 * * *", timezone?: "UTC" }` or a non-empty list of `{ cron, timezone? }` objects.
- `push`, `pull_request`, `merge`: Either a single filter object or a non-empty list of filter objects (`GF020`). Each filter object supports:
  - `repo?: "owner/repo" | "https://github.com/owner/repo"` and/or `repos?: ["owner/repo", ...]` (defaults to the `.process.grain.md` repository; cross-repo triggers require both repositories to belong to the same team or owner).
  - `branches?: ["main", "release/*"]` (branch names or globs).
  - `paths?: ["src/handler.go", "src/auth/", "/src/auth", "schemas/**/*.sql", "https://github.com/acme/payments/tree/main/src/auth"]` (repo-relative file paths, folder paths `dir/` or `/dir` matching `dir/**`, globs, or full forge `https://` file/folder URLs).
  - `ignore?: ["docs/", "**/*.md"]` (excluded file paths, folder paths, or globs).
  - At least one of `repo`, `repos`, `branches`, or `paths` MUST be specified on each filter object (`GF020`).
- `workflow`: Either a single workflow filter object or a non-empty list of workflow filter objects (`GF020`). Fires when a CI workflow run or check suite is triggered, succeeds, or fails:
  - `workflow?: "<name-or-glob-or-path>"` and/or `workflows?: ["<name-or-glob-or-path>", ...]` (e.g. `"grain CI"`, `"e2e-*"`, `".github/workflows/release.yml"`; at least one of `workflow` or `workflows` is required, `GF020`).
  - `events?: ["triggered" | "succeeded" | "failed"]` (aliases `"started"` -> `"triggered"`, `"success"` -> `"succeeded"`, `"failure"` -> `"failed"`; defaults to `["succeeded", "failed"]` when omitted).
  - `repo?: "owner/repo" | "https://github.com/owner/repo"` and/or `repos?: ["owner/repo", ...]` (same-repo by default; cross-repo allowed within the same team or owner).
  - `branches?: ["main", "release/*"]` (branch names or globs matched against the workflow run's head branch).
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
- **Export & import (from/to Git repositories or by parsing issues):** Grainfiles are portable, version-controlled declarative Markdown documents (`.task.grain.md` and `.process.grain.md`), and Grain makes it easy to export and import Grainfiles either from and to Git repositories or by parsing issues.
- **Idempotency & withdrawal:** A `.task.grain.md` executes once; editing it after completion is a no-op (`task_completed`). Deleting an incomplete `.task.grain.md` or setting `draft: true` emits `task_removed` (`action: "removed"`) and cancels any in-flight run (`dependency_completed` with `cancelled`). Deleting a `.process.grain.md` or setting `draft: true` emits `process_removed` (`action: "removed"`).
- **1-to-1 tasks vs 1-to-many processes:** `.task.grain.md` targets a single `recipient` deployment (defaulting to `/`) and has a single terminal `done` state, whereas `.process.grain.md` has no `recipient` and no single completion state — multi-step or cross-task coordination inside a process is expressed in natural language in the `.process.grain.md` prompt itself (where the executing agent can run steps or propose/chain sub-tasks locally).
- **`depends_on` & `capabilities` rules (`GF020`, `GF032`, `GF034`):** `depends_on` is only valid on `.task.grain.md` (`GF020` on `.process.grain.md`), and every `depends_on` target MUST resolve to a concrete `.task.grain.md` file (`GF032`), subject to DAG cycle detection (`GF034`). `capabilities` is valid on both `.task.grain.md` and `.process.grain.md`, and every `capabilities` entry MUST be a valid `https://` capability URL (`<capability-url>[#<method>]`, e.g. `https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key`, noting OCI image URLs only as an optional convention rather than an OCI-specific grammar) either as a URL string or as `{capability: "<capability-url>[#<method>]", with: {<args>}}` (`GF020`).
- **Same-origin reference authority:** A repository can only grant read access to its own files/folders at the Grainfile's pinned commit SHA (`authorized_references`, which includes same-repo `references` and `from_template`). Cross-repo `references` require the executing deployment's own credentials.
- **Full specification:** See `docs/grainfiles-spec.md`.

## 5. Lint Codes (`GF001`–`GF036`)
| Code | Severity | Condition |
| :--- | :--- | :--- |
| `GF001` | error | Missing opening `---` on line 1 or missing closing `---` front-matter delimiter |
| `GF002` | error | Malformed YAML front matter or non-mapping root |
| `GF003` | error | Duplicate YAML mapping key |
| `GF004` | error | Disallowed YAML construct (anchor `&`, alias `*`, merge key `<<`, or tag `!`) |
| `GF005` | warning | Unregistered `.<kind>.grain.md` or `.<kind>.grain.md.jinja2` suffix |
| `GF006` | error | Case-insensitive path collision, or file/front-matter/path size limit exceeded |
| `GF020` | error | Schema validation failure: missing/invalid `grainfile: v1`, missing required field (`title`, `when`), kind-mismatch field, invalid capability URL reference (`<capability-url>[#<method>]`) or `with` arguments, invalid timestamp/timezone (non-RFC 3339 `not_before`, naive timestamp, non-IANA timezone), or invalid trigger filter |
| `GF022` | warning | Unknown top-level front-matter key not prefixed with `x_` |
| `GF030` | error | Unresolvable local `recipient`, `requestor`, `references`, `depends_on`, or `from_template` path or `#<anchor>` in repository tree |
| `GF031` | error | Unresolvable or invalid `references` entry |
| `GF032` | error | Wrong target kind: `depends_on` target is not a concrete `.task.grain.md` file, or deployment identity resolves to a file instead of a repository/folder |
| `GF033` | error | Jinja2 template syntax, AST security, or evaluation error in `.grain.md.jinja2` |
| `GF034` | error | Cycle in `depends_on` graph (`A -> ... -> A`) or forbidden Jinja2 tag (`include`, `extends`, `import`, `from`) |
| `GF035` | error | Invalid or unresolvable reference or `from_template` reference (must target a `.task.grain.md.jinja2` or `.process.grain.md.jinja2` matching the Grainfile's kind) |
| `GF036` | error | Path traversal (`..`) climbing outside repository root in `recipient`, `requestor`, `references`, `depends_on`, or `from_template` |
