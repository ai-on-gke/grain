# The Grainfile Format (`grainfile: v1`)

**Status:** draft for review (bsalmon@google.com). Normative.

This document specifies the Grainfile Format (`grainfile: v1`): a portable, version-controlled declarative Markdown format for authoring discrete **tasks** (`<name>.task.grain.md`) and standing **processes** (`<name>.process.grain.md`) that Grain deployments execute directly, and that Grain imports from and exports to Git repositories or issues (§9). It defines the two grainfile kinds (`**/*.grain.md`), their YAML front matter (`grainfile: v1`) and Markdown prompt bodies, repository source authority, task deployment identities (`recipient`, defaulting to `/`) and optional attribution metadata (`requestor`), draft and deferral controls (`draft`, `not_before`), Streamable HTTP MCP capability URL references (`capabilities`), cross-repository and repo-local references (`references`, `depends_on`), structured process triggers (`when`) and merge gating (`gate`), the execution, import, and export model, security invariants, and the lint rules that validate a repository tree.

It is **not** an engine's internal database schema. Live run state, sandbox orchestration, queue ranking, and execution logs are each deployment's own concern (§2.3); deployments watch or import `.task.grain.md` and `.process.grain.md` files addressed to them and execute them directly (§9).

A concise quick-reference summary for agents is in [`docs/grainfile-v1.md`](grainfile-v1.md). Where the summary ever disagrees with this specification, **this document wins**. This specification is maintained in [`bwsalmon/silo`](https://github.com/bwsalmon/silo) and mirrored here unchanged; the machine-readable schemas ([`schemas/grainfiles/`](https://github.com/bwsalmon/silo/tree/main/schemas/grainfiles)), the reference linter ([`tools/grainfiles/grainlint.py`](https://github.com/bwsalmon/silo/blob/main/tools/grainfiles/grainlint.py)), the design rationale ([`docs/grainfiles.md`](https://github.com/bwsalmon/silo/blob/main/docs/grainfiles.md)), and the worked example of Appendix A ([`examples/grainfiles/`](https://github.com/bwsalmon/silo/tree/main/examples/grainfiles)) live in that repository, not in this one.

Where this specification makes a normative design choice, the choice is marked **Decision:**, and every decision is summarised in Appendix B.

## Contents

1. Conventions
2. Terminology, scope and conformance classes
3. Versioning and extension
4. Files and names
5. Grainfile syntax
6. Identities and common fields
7. The kinds (`task` and `process`)
8. References and graphs
9. Execution, import, and export
10. Errors and lint
11. Security considerations
12. Registries of identifiers
- Appendix A. The worked example
- Appendix B. Decisions made by this specification

## 1. Conventions

**Use Case & Overview:** Grainfiles are portable, version-controlled, declarative Markdown documents (`.task.grain.md` and `.process.grain.md`) checked into Git repositories that coordinate one-shot tasks and standing event-driven processes across Grain deployments. A `.task.grain.md` directs a single unit of work (optionally gated by `depends_on`, `not_before`, and requested `capabilities`) to a `recipient` deployment (defaulting to `"/"` for the source repository's deployment, or a forge repository/folder URL `https://...` or repo-local folder path), while a `.process.grain.md` defines a reusable automation workflow or merge gate triggered by repository, CI workflow, schedule, or manual events (`when`). Capabilities are requested by `<capability_id>[#<method>]` reference, optionally with `with:` arguments (§6.6); what a capability server is and how a deployment attaches to one is specified in [`docs/capabilities-spec.md`](capabilities-spec.md). Grain deployments watch or import Grainfiles from Git repositories or issues (§9) and run matching tasks and processes when their triggers, dependencies, and capabilities are satisfied.

1.1. The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY" and "OPTIONAL" are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

1.2. Grammars use ABNF (RFC 5234). `ALPHA`, `DIGIT` and `HEXDIG` are RFC 5234's core rules; `LOWER = %x61-7A`.

1.3. "Front matter" is the YAML mapping at the head of a grainfile (§5). A field is written `name`, a nested field `parent.child`.

1.4. **Timestamps:** Every timestamp in a grainfile (`not_before`) MUST be a valid **RFC 3339 (`date-time`)** timestamp (for example, `2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`), normalized to UTC (`Z`) for comparison and storage. Any value that is not a valid RFC 3339 `date-time` timestamp is a validation error (GF020).

1.5. Lint codes (`GF001`…) are defined in §10. "Is an error (GF030)" means a linter MUST report that code, and every other conformance class MUST treat the condition as §10.2 specifies.

## 2. Terminology, scope and conformance classes

### 2.1 Terms

**grainfile**
: A portable, version-controlled declarative UTF-8 Markdown file named `<name>.task.grain.md` or `<name>.process.grain.md` (§4.1, glob `**/*.grain.md`), with YAML front matter and an optional Markdown body (§5). Plain `.grain.md` files are 100% literal with zero template evaluation.

**grainfile template**
: A self-contained Jinja2 template file named `<name>.task.grain.md.jinja2` or `<name>.process.grain.md.jinja2` (§4.1, §5.7, glob `**/*.grain.md.jinja2`). Rendering a `.grain.md.jinja2` template in a sandboxed Jinja2 environment with a variable map `vars` (or its `| default(...)` fallback values) produces a `<name>.task.grain.md` or `<name>.process.grain.md` document.

**task**
: A `<name>.task.grain.md` grainfile (§7.1): a discrete unit of work to run once per canonical `(repo_url, file_path)` on the repository's default or protected branch (or imported by parsing an issue). Its Markdown body is the prompt given directly to the run.

**process**
: A `<name>.process.grain.md` grainfile (§7.2): a standing, repeatable process with structured triggers (`when`) and an optional `gate` flag. Its Markdown body is the prompt dispatched whenever the process fires.

**deployment**
: An execution engine (such as a Grain deployment) identified by the plain `https://` URL of a git repository or folder that it controls (§6.2.2).

**requestor**
: Optional attribution metadata on a `.task.grain.md` recording who requested or owns the task: either a user identity (`Name <email>`) or a plain `https://` URL / deployment identity (§6.2). The authoritative source of a grainfile is the URL of the repository/folder where the file itself lives (§6.2.3, §11.1).

**recipient**
: The deployment identity (`https://...` repository or folder URL, or repo-local folder reference) on a `.task.grain.md` intended to execute the task (§6.2.2). When omitted on a `.task.grain.md`, `recipient` defaults to `"/"` (the root of the enclosing repository). A `.process.grain.md` does not declare `recipient`: any number of deployments may subscribe to or import a `.process.grain.md` (by repository, folder, or `.process.grain.md` URL).

**asset**
: Any file in a repository that is not a grainfile: source code, design documents, images, configuration files, or `README.md`.

### 2.2 In scope

The format defines:
- the two grainfile kinds, `task` (`<name>.task.grain.md`) and `process` (`<name>.process.grain.md`), and their self-contained Jinja2 template counterparts (`<name>.task.grain.md.jinja2` and `<name>.process.grain.md.jinja2`) (§4.1, §5.7, §7);
- the mandatory format version field `grainfile: v1` (required in every Grainfile; `GF020` when omitted) and programmatic version mapping to `docs/grainfile-v1.md`, `docs/grainfiles-spec.md`, and `schemas/grainfiles/` (§3);
- source authority from the repository/folder URL where the grainfile lives, task `recipient` deployment identities (defaulting to `"/"` when omitted), and optional task `requestor` attribution (`Name <email>` or plain `https://` URL) (§6);
- work-in-progress drafting (`draft: true`), deferred execution/activation (`not_before`), and capability grants (`capabilities`: `<capability_id>[#<method>]` or `{capability, with}`) (§6.3, §6.4, §6.6);
- plain `https://` remote references (including standard forge `/blob/<ref>/<path>` and `/tree/<ref>/<path>` URLs) and repo-local references in `references` and task-only `depends_on` (§8);
- structured process triggers (`when`: `schedule`, `push`, `merge`, `pull_request`, `workflow`, `manual`, `scenario`) and commit/pull-request gating (`gate: true`) (§7.2);
- the execution, import, and export model by which deployments watch repositories, import grainfiles from repositories or issues, export them, and execute matching grainfiles directly (§9);
- the validation rules, lint codes (§10), and security invariants (§11).

### 2.3 Out of scope

The following are each deployment's internal concern and are not represented in grainfiles:
- internal task queues, priority ranking, and live run state;
- sandbox VM provisioning, model selection, and credential storage;
- execution history storage and internal audit tables.

### 2.4 Conformance classes

| Class | What it does | Must implement |
| --- | --- | --- |
| **reader** | Parses `.task.grain.md` and `.process.grain.md` files (from repositories or parsed issues) and resolves references. | §3–§8 |
| **writer** | Creates, exports, or edits grainfiles (to Git repositories or from parsed issues) while preserving unknown fields and formatting. | reader; §5.5, §9 |
| **executor** | A deployment: watches or imports from repositories and issues for grainfiles matching its `recipient` identity or process triggers and executes them directly. | reader; §9, §11 |
| **linter** | Reports the conditions of §10 for a repository or tree of repositories. | reader; §10 |

The reference linter is `tools/grainfiles/grainlint.py`; where it differs from this text, this text wins.

## 3. Versioning and extension

3.1. **Kind by suffix and mandatory version by `grainfile`.** A grainfile's kind is determined solely by its filename suffix: `.task.grain.md` (`task`) or `.process.grain.md` (`process`) (§4.1). Front matter on every `.task.grain.md` and `.process.grain.md` file (and on the rendered output of every `.task.grain.md.jinja2` and `.process.grain.md.jinja2` template) MUST declare `grainfile: v1` (string). Omitting `grainfile`, providing a non-string `grainfile` value, or specifying an unrecognised version string is a validation error (GF020).

3.1.1. **Version mapping.** Version `"v1"` maps to this specification, its quick reference `docs/grainfile-v1.md`, and the JSON Schemas `schemas/grainfiles/common.schema.json`, `task.schema.json`, and `process.schema.json` (maintained in `bwsalmon/silo`; see the note above the Contents). Tools that index grainfiles SHOULD report which version they validated against.

3.2. **Compatible additions.** A later revision of this specification MAY add optional fields, kinds, or trigger sources (§12).

3.3. **Unknown fields.** A front-matter field this specification does not define for the file's kind is *unknown*:
- A reader MUST ignore an unknown field when making execution decisions.
- A writer that edits an existing grainfile MUST preserve unknown fields (§5.5).
- A writer MUST NOT add an unknown field unless its name begins with `x_` (an extension field). Extension fields (`x_*`) are reserved for local or experimental metadata and are never defined by this specification.
- A linter MUST report an unknown field that does not begin with `x_` as a warning (GF022), and MUST NOT warn on `x_*` fields.

The JSON Schemas set `additionalProperties: false` and allow `^x_` via `patternProperties`; the linter maps non-`x_` additional properties to GF022.

3.4. **Unknown trigger sources fail closed.** A `.process.grain.md` whose `when:` mapping contains an unknown trigger source MUST NOT fire and is a validation error (GF020), because silently ignoring a trigger condition would alter when a process or gate runs.

## 4. Files and names

### 4.1 Grammar

```abnf
grainfile-name          = stem "." kind ".grain.md"
grainfile-template-name = stem "." kind ".grain.md.jinja2"
stem                    = ( ALPHA / DIGIT ) *( ALPHA / DIGIT / "." / "-" / "_" )
kind                    = %s"task" / %s"process"            ; registered in §12.1
```

4.1.1. A filename is parsed from the right: the trailing `.grain.md` (or `.grain.md.jinja2` for a Jinja2 template; §5.7), then the kind suffix immediately preceding it (`.task` or `.process`), and everything before that as the `stem`.

4.1.2. The **name** of a grainfile (or grainfile template) is its stem: `2026-10-16-widget-schema-v2` is the name of `2026-10-16-widget-schema-v2.task.grain.md`, and `rollout-region` is the name of `rollout-region.task.grain.md.jinja2`.

4.1.3. A stem MUST match `^[A-Za-z0-9][A-Za-z0-9._-]*$` (GF001): upper-case and lower-case ASCII letters, digits, `.`, `-`, and `_`, starting with an ASCII letter or digit.

4.1.4. Each path segment MUST be at most 255 bytes, a repository-relative path at most 1024 bytes, and two paths in the same repository MUST NOT differ only in ASCII case (GF006).

### 4.2 Placement, kinds and assets

4.2.1. Grainfiles and grainfile templates MAY be placed in any directory of a git repository (`**/*.grain.md` and `**/*.grain.md.jinja2`; by convention under `.grain/` in a source code repository, or in top-level directories such as `tasks/` and `processes/` in a dedicated repository).

4.2.2. A file is a concrete grainfile if and only if its name ends in `.<kind>.grain.md` for a kind registered in §12.1 (`.task.grain.md` or `.process.grain.md`). A file is a grainfile template if and only if its name ends in `.<kind>.grain.md.jinja2` for a registered kind (`.task.grain.md.jinja2` or `.process.grain.md.jinja2`; §5.7). Every concrete grainfile—and the rendered output of every grainfile template—MUST start with YAML front matter (GF003).

4.2.3. Every other file in the repository is an asset. A `.grain.md` or `.grain.md.jinja2` file whose kind segment is not a registered kind (for example, `notes/x.widget.grain.md`) is treated as an asset, and a linter SHOULD emit a warning (GF005) to catch misspelled kind suffixes. Ordinary `.md` files that do not end in `.grain.md` (or `.grain.md.jinja2`) are normal Markdown assets and MUST NOT trigger GF005.

4.2.4. A grainfile MUST NOT be stored as a Git LFS pointer (GF003). Assets MAY use Git LFS.

### 4.3 Limits

A reader MUST support at least, and MAY refuse beyond (GF006): a grainfile of 1 MiB in total size and 64 KiB of YAML front matter.

## 5. Grainfile syntax

5.1. **Encoding.** UTF-8 without a byte-order mark (BOM), using LF or CRLF line endings (writers SHOULD emit LF). NUL bytes are forbidden (GF003).

5.2. **Delimiters.** A grainfile MUST begin with a line that is `---` (optional trailing spaces allowed). Front matter consists of every following line up to the next line that is `---` (optional trailing spaces allowed). The **body** is everything after that closing delimiter line. A grainfile without front matter or without a closing delimiter is malformed (GF003).

5.3. **The YAML subset.** Front matter is YAML 1.2 restricted to a single document whose top-level value is a mapping, with plain string keys, no duplicate keys at any level, no YAML anchors (`&`), aliases (`*`), merge keys (`<<`), or explicit tags (`!`) (GF004). Scalar values are strings, integers, booleans (`true`/`false`), `null`, or floats where a schema permits `number`. **Decision:** Process triggers use the key `when` rather than `on` so that legacy YAML 1.1 parsers never misparse the trigger key as boolean `true`.

5.4. **The body (optional).** The Markdown body (CommonMark with GitHub-Flavoured Markdown tables) after the closing `---` front-matter delimiter is the **prompt** given directly to the run when a deployment executes the `.task.grain.md` or `.process.grain.md` (§9, §11.5). The Markdown body is **optional and MAY be empty** (for example, when `title`, `from_template`, `references`, or `when` already conveys the full intent of the task or process).

5.5. **Round-trip editing.** A tool that modifies an existing grainfile MUST preserve the ordering, comments, unknown fields, and body text of everything it did not intend to change.

5.6. **Malformed files.** A grainfile that violates §5.1–§5.3 is unreadable (GF003 or GF004). Readers MUST NOT execute it, MUST report the error, and MUST continue processing other files in the repository.

5.7. **Plain `.grain.md` vs. self-contained `.grain.md.jinja2` templates.**
- **Plain `<name>.task.grain.md` and `<name>.process.grain.md` are 100% literal:** Zero template evaluation is performed on `.grain.md` files; any `{{ ... }}`, `{% ... %}`, or `{# ... #}` sequences inside Markdown prose or code blocks are preserved verbatim as literal text.
- **`<name>.task.grain.md.jinja2` and `<name>.process.grain.md.jinja2` apply sandboxed Jinja2 to the whole file:**
  - Each `.task.grain.md.jinja2` or `.process.grain.md.jinja2` is a self-contained single-file template evaluated in a sandboxed Jinja2 environment with no template loader (the reference implementation uses Jinja2's `ImmutableSandboxedEnvironment(loader=None)`; renderers MUST use Jinja2 3.1.6 or later, which closes the known sandbox escapes).
  - External template inclusion and inheritance tags (`{% include %}`, `{% extends %}`, `{% import %}`, `{% from %}`) are forbidden (GF034).
  - Jinja2 syntax or rendering errors are rejected (GF033). Both the raw `.jinja2` template and the rendered output MUST NOT exceed the file size limits of §4.3 (GF006), and a renderer MUST bound rendering time (RECOMMENDED: abort after two seconds and report GF033).
  - Only templates read from a repository are rendered (§11.1); a template supplied in an issue or any other untrusted channel MUST NOT be rendered.
  - Undeclared template variables are discovered from the Jinja2 AST in deterministic sorted order (`template_vars`).
  - Rendering `<name>.task.grain.md.jinja2` or `<name>.process.grain.md.jinja2` with a variable map `vars` (or its `| default(...)` fallback values when linting/indexing) produces a `<name>.task.grain.md` or `<name>.process.grain.md` document (parameterizing both YAML front-matter values and the Markdown body), which MUST then pass the standard `v1` Grainfile parser and validator.

## 6. Identities and common fields

### 6.1 Common fields

The entire Grainfile `v1` front-matter vocabulary consists of **12 fields** (plus `x_*` extensions):
- **Shared (7):** `grainfile` (`"v1"`, **required**), `title` (**required**), `draft`, `not_before`, `capabilities`, `references`, `from_template`
- **`.task.grain.md` only (3):** `recipient` (optional, default `"/"`), `requestor` (optional), `depends_on` (optional; targets must resolve to concrete `.task.grain.md` files, never `.process.grain.md` and never unrendered `.jinja2` templates)
- **`.process.grain.md` only (2):** `when` (required), `gate` (optional, default `false`)

Every `.task.grain.md` and `.process.grain.md` shares the following base fields and MAY include `x_*` extension fields (§3.3):

| Field | Type | Req. | Default | Meaning |
| --- | --- | --- | --- | --- |
| `grainfile` | `"v1"` | **Req.** | `"v1"` | Mandatory Grainfile specification version (§3.1, GF020). |
| `title` | single-line string | **Req.** | | Non-empty one-line summary of the task or process, 1–200 characters (GF020). |
| `draft` | boolean | Opt. | `false` | When `true`, marks the grainfile as a work-in-progress draft that deployments MUST ignore until `draft` is `false` or omitted (§6.3, §9). |
| `not_before` | RFC 3339 `date-time` (§1.4) | Opt. | | Earliest activation timestamp (`2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`, normalized to UTC) at which the task may execute or the process may fire (§6.4, §9). |
| `capabilities` | list of *capability-ref* (§6.6) | Opt. | `[]` | Requested capability grants (`<capability_id>[#<method>]` or `{capability: "<capability_id>[#<method>]", with: {<args>}}`; §6.6, GF020). |
| `references` | list of *ref* (§8) | Opt. | `[]` | References to relevant documents, code paths, design docs, or grainfiles provided as context to the run. |
| `from_template` | *ref* to `.grain.md.jinja2` (§6.5) | Opt. | | Optional provenance link to the `.task.grain.md.jinja2` or `.process.grain.md.jinja2` template from which this grainfile was rendered or materialized (GF035). |
| `x_*` | any | Opt. | | Extension fields (§3.3). |

**Decision:** There is no creation-timestamp field, because git commit history already records creation and modification authoritatively, and `depends_on` is restricted exclusively to `.task.grain.md` because a `.task.grain.md` has a single identity `(repo_url, file_path)`, a single `recipient` deployment, and a single terminal `done` state, whereas a `.process.grain.md` is a standing 1-to-many trigger with no `recipient` and no single completion state. Multi-step or cross-task coordination inside a process is expressed in natural language in the `.process.grain.md` prompt itself.

### 6.2 Source authority and identities (`recipient` and `requestor`)

**Decision:** The authoritative, authenticated source of a `.task.grain.md` or `.process.grain.md` is the canonical URL of the repository and folder where the grainfile itself lives—governed by that repository's branch protection, merge policy, and `CODEOWNERS` reviews (§11.1). `.task.grain.md` is a 1-to-1 work item directed to a single `recipient` deployment (defaulting to `"/"`, the root of the enclosing repository, when omitted) with optional `requestor` attribution, whereas `.process.grain.md` is a 1-to-many reusable/standing process that any number of Grain deployments can subscribe to or import (by repository, enclosing folder, or `.process.grain.md` URL) and therefore does not take `recipient` or `requestor`. Grainfiles use two kinds of identity—**user identities** and **deployment / URL identities**—rather than forge-specific `@handle` strings or custom principal namespaces:

#### 6.2.1 User identity

A **user identity** uses standard RFC 5322 / git author notation `Name <email>`:

```abnf
user-identity = display-name 1*WSP "<" local-part "@" domain ">"
display-name  = non-angle *( non-angle / WSP )
```

- `display-name` MUST be non-empty and MUST NOT contain `<`, `>`, or line breaks.
- The angle-bracketed address MUST be a valid `<local@domain>` email address with at least one `.` in `domain` and no whitespace or angle brackets (GF020).
- Example: `Bruce Salmon <bsalmon@google.com>`.

#### 6.2.2 Deployment identity

A **deployment identity** is a reference (§8.1) naming the git repository or folder that the deployment controls:
- Across repositories, a plain `https://` repository or folder URL, such as `https://github.com/acme/grain-eu` or `https://github.com/acme/deployments/tree/main/grain-eu` (or `file://` in local test suites).
- Within the same repository, a repo-local relative path (such as `.` or `../grain-eu`) or a `/`-rooted path from the repository root (such as `/` or `/grain-eu`).
- When used in `requestor`, a plain `https://` URL MAY also name a user profile, team, or deployment URL.
- A deployment identity MUST NOT carry a `#<anchor>` suffix or query parameters (GF035) and MUST NOT resolve to a file (GF032). When the target repository is present in the linted tree, a repo-local referenced repository or folder MUST exist (GF030).

#### 6.2.3 Rules for `recipient` and `requestor`

- **`recipient`** (OPTIONAL on `.task.grain.md` only, default `"/"`) MUST be a deployment identity when present (GF020). When omitted on a `.task.grain.md`, `recipient` defaults to `"/"` (resolving to the root of the repository where the grainfile lives). A user identity or empty string in `recipient` is an error (GF020). `recipient` is not permitted on `.process.grain.md` (GF020).
- **`requestor`** (OPTIONAL on `.task.grain.md` only) MAY be omitted; when present, it MUST be either a user identity (`Name <email>`) or a plain `https://` URL / deployment identity (GF020, GF035). `requestor` is not permitted on `.process.grain.md` (GF020).

### 6.3 Drafts (`draft`)

- **`draft`** (OPTIONAL on both `.task.grain.md` and `.process.grain.md`, boolean, default `false`) marks a grainfile as a work-in-progress draft. A non-boolean value in `draft` is an error (GF020).
- When `draft: true`, recipient deployments MUST ignore the grainfile: they MUST NOT queue or execute a draft `.task.grain.md`, and MUST NOT register or fire a draft `.process.grain.md` trigger or merge gate (§9), until a commit sets `draft` to `false` or removes `draft`.
- A linter MUST still validate a `draft: true` grainfile normally (syntax, schema, identities, references, and dependency/trigger graphs) so authors receive full lint feedback while drafting.

### 6.4 Deferred execution (`not_before`)

- **`not_before`** (OPTIONAL on both `.task.grain.md` and `.process.grain.md`, string) specifies the earliest instant at which the grainfile takes effect (§1.4): it MUST be a valid **RFC 3339 (`date-time`)** timestamp (such as `2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`), which is converted to and stored in UTC (`Z`). Any value that is not a valid RFC 3339 `date-time` timestamp is a validation error (GF020).
- On a `.task.grain.md`, the recipient deployment holds the task as deferred and MUST NOT execute it before `not_before` (in addition to waiting for any `depends_on`; §9.2).
- On a `.process.grain.md`, when present, the process MUST NOT fire before `not_before` (§9.2).

### 6.5 Template provenance (`from_template`)

- **`from_template`** (OPTIONAL on both `.task.grain.md` and `.process.grain.md`, string) records the source Jinja2 Grainfile template from which the grainfile was rendered or materialized.
- **Value and validation (GF035):** Must be a non-empty string (`https://` URL or repo-relative `/path` or relative `./path`, normalized with the same rules as `references` / `depends_on`, without `#<anchor>`). On a `.task.grain.md` it MUST end with `.task.grain.md.jinja2`; on a `.process.grain.md` it MUST end with `.process.grain.md.jinja2` (GF035). When the target repository is in the linted tree, the referenced template file MUST exist (GF030).
- **Same-repo reference and cross-repo provenance:** If `from_template` is in the same repository as the grainfile, its canonical URL pinned at the grainfile's `commit_sha` is included in `authorized_references` (authorizing on-demand read access at that commit). If `from_template` is cross-repo, the canonical URL is recorded as provenance metadata on the indexed grainfile and events without granting cross-repo file read access.
- **Auto-population on template rendering:** When a deployment or tool renders a `.grain.md.jinja2` template, it automatically populates `from_template` with the canonical URL of the `.grain.md.jinja2` template if not already set.

### 6.6 Capabilities (`capabilities`)

- **Capability model:** A capability is provided by an operator-run capability server and requested by reference; the reference grammar, the `with:` argument validation, the single-method shorthand (omitting `#<method>` when the server's last path segment names it), and the sub-task privilege rules are specified normatively in [`docs/capabilities-spec.md`](capabilities-spec.md) §2 and summarised in [`docs/capabilities.md`](capabilities.md). A Grainfile linter validates the reference syntax and entry shape below; argument validation against the server's schema happens in the executing deployment.
- **Entry forms (GF020):** On both `.task.grain.md` and `.process.grain.md`, `capabilities` is an optional list where each entry is either:
  1. A string `<capability_id>[#<method>]` when no arguments are passed (e.g. `https://github.com/bwsalmon/grain/capabilities/self-debug` or `https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key`), or
  2. An object `{capability: "<capability_id>[#<method>]", with: {<args>}}` when passing structured arguments to the capability method (`with` must be a mapping when present, and unknown keys on the entry object are rejected with `GF020`):
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
           root: true
       - capability: https://github.com/bwsalmon/grain/capabilities/github#git-repo
         with:
           repo: bwsalmon/silo
           mode: read
           root: false
     ```
- **Reference grammar (GF020):** A capability reference MUST satisfy `capability-ref` of [`docs/capabilities-spec.md`](capabilities-spec.md) §2.1: an `https://` URL with a non-empty host and at least one path segment, no query string, userinfo, `.`/`..` segments, or trailing `/`, followed by an optional `#<method>` where `<method>` matches `^[a-z0-9][a-z0-9_-]{0,62}$`. The schema `common.schema.json#/$defs/capabilityRef` encodes the same grammar.
- **No secrets in `with:`:** Grainfiles are committed to repositories; `with:` values select named server-side configuration (`account: infra-staging`) and MUST NOT carry secret material.
- **One root working directory:** a capability that checks a repository out (`#git-repo`) says in its `with:` whether that checkout becomes the run's root working directory (`root: true`, the directory the run starts in) or an additional repository cloned beside it (`root: false`). A run may list any number of `#git-repo` grants but at most one with `root: true`; the executing deployment refuses a run that claims the root twice ([`docs/capabilities-spec.md`](capabilities-spec.md) §5.5, §6.3).

## 7. The kinds

Each subsection defines one of the two grainfile kinds. The corresponding JSON Schemas are `schemas/grainfiles/task.schema.json` and `schemas/grainfiles/process.schema.json`.

### 7.1 `task` (`<name>.task.grain.md`)

A `task` grainfile describes a discrete unit of work to be executed once by its `recipient` deployment. Its Markdown body is the prompt handed directly to the run.

| Field | Type | Req. | Default | Meaning |
| --- | --- | --- | --- | --- |
| `grainfile` | `"v1"` | **Req.** | `"v1"` | Mandatory specification version (§3.1, GF020). |
| `title` | string | **Req.** | | One-line summary. |
| `requestor` | *user identity* or *deployment identity* | Opt. | | Optional attribution: who requested the task (§6.2). |
| `recipient` | *deployment identity* | Opt. | `"/"` | Deployment that executes the task (§6.2.2); defaults to `"/"` (enclosing repo). |
| `draft` | boolean | Opt. | `false` | When `true`, recipient deployments ignore the task until `draft` is `false` or omitted (§6.3, §9). |
| `not_before` | RFC 3339 `date-time` | Opt. | | Earliest RFC 3339 timestamp (`2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`, normalized to UTC) at which the task may execute (§6.4, §9). |
| `depends_on` | list of *ref* to `task` | Opt. | `[]` | `.task.grain.md` files that must finish before this task runs (§8.3). |
| `capabilities` | list of *capability-ref* | Opt. | `[]` | Capability grants (`<capability_id>[#<method>]` or `{capability, with}`) requested by this task (§6.6, GF020). |
| `references` | list of *ref* | Opt. | `[]` | Relevant files, folders, repositories, design docs, or grainfiles made available to the run. |
| `from_template` | *ref* to `.task.grain.md.jinja2` | Opt. | | Optional provenance link to the source `.task.grain.md.jinja2` template (§6.5, GF035). |
| `x_*` | any | Opt. | | Extension fields (§3.3). |

**Decision:** `recipient` and `requestor` are restricted to `.task.grain.md` only, and a `.task.grain.md`'s `depends_on` entries MUST resolve only to `.task.grain.md` files (GF032; §8.3).

Example (`2026-10-16-widget-schema-v2.task.grain.md`):

```markdown
---
grainfile: v1
title: Add version header to the widget binary schema
requestor: Bruce Salmon <bsalmon@google.com>
recipient: https://github.com/acme/grain-eu
capabilities:
  - https://github.com/bwsalmon/grain/capabilities/self-debug
  - capability: https://github.com/bwsalmon/grain/capabilities/gcp#gcp-key
    with:
      account: infra-staging
references:
  - /docs/widget-schema.md#schema-version-header
  - /src/widget.go
---
Update `src/widget.go` so encoded widgets begin with the two-byte v2
schema header described in `docs/widget-schema.md`, while keeping the
decoder compatible with v1 payloads.
```

### 7.2 `process` (`<name>.process.grain.md`)

A `process` grainfile describes a standing, repeatable 1-to-many process that any number of Grain deployments can subscribe to (by repository, enclosing folder, or `.process.grain.md` URL). Whenever triggered via `when`, each subscribed deployment dispatches a run with the `.process.grain.md` Markdown body as the prompt.

| Field | Type | Req. | Default | Meaning |
| --- | --- | --- | --- | --- |
| `grainfile` | `"v1"` | **Req.** | `"v1"` | Mandatory specification version (§3.1, GF020). |
| `title` | string | **Req.** | | One-line summary. |
| `draft` | boolean | Opt. | `false` | When `true`, subscribed deployments ignore the process until `draft` is `false` or omitted (§6.3, §9). |
| `not_before` | RFC 3339 `date-time` | Opt. | | Earliest RFC 3339 timestamp (`2026-10-25T09:00:00Z` or `2026-10-25T09:00:00+02:00`, normalized to UTC) at which the process may fire (§6.4, §9). |
| `capabilities` | list of *capability-ref* | Opt. | `[]` | Capability grants (`<capability_id>[#<method>]` or `{capability, with}`) requested by this process (§6.6, GF020). |
| `references` | list of *ref* | Opt. | `[]` | Relevant files, folders, repositories, design docs, or grainfiles made available to each run. |
| `from_template` | *ref* to `.process.grain.md.jinja2` | Opt. | | Optional provenance link to the source `.process.grain.md.jinja2` template (§6.5, GF035). |
| `when` | mapping of source → options | **Req.** | | Structured triggers specifying when the process fires (§7.2.1). |
| `gate` | boolean | Opt. | `false` | When `true`, gates the triggering commit or pull request on this process passing (§7.2.2). |
| `x_*` | any | Opt. | | Extension fields (§3.3). |

A `.process.grain.md` MUST specify `grainfile: v1` and `when` (GF020) and MUST NOT specify `recipient`, `requestor`, or `depends_on` (GF020).

#### 7.2.1 Trigger sources (`when`)

`when` is a non-empty mapping from one or more registered trigger sources (§12.2) to their options. An unknown trigger source key is an error (GF020, §3.4).

- **`schedule: {cron, timezone} | [{cron, timezone}, ...]`**
  - Either a single schedule object `{cron, timezone?}` or a non-empty list of schedule objects (allowing multi-timezone or multi-cadence schedules on one process).
  - `cron` (REQUIRED, string): A five-field POSIX `crontab(5)` expression (`minute hour day-of-month month day-of-week`). A `cron` string that does not consist of five whitespace-separated fields is an error (GF020).
  - `timezone` (OPTIONAL, string, default `"UTC"`): MUST be `"UTC"` or a valid IANA Time Zone Database identifier (such as `"America/Los_Angeles"`, `"Europe/Berlin"`, or `"Asia/Tokyo"`). Numeric offsets (`"+02:00"`), unknown zone names, and non-IANA abbreviations (`"PST"`, `"EST"`, `"CET"`) are rejected (GF020).
  - **DST evaluation semantics:** For non-UTC IANA zones, cron fields are evaluated against local wall-clock time in that zone; during spring-forward transitions a skipped local wall-clock minute does not fire (or fires once at the transition boundary per scheduler policy), and during fall-back transitions an ambiguous repeated local wall-clock minute fires only on its first occurrence.
- **`push`, `merge`, `pull_request: <filter> | [<filter>, ...]`**
  - Each of `when.push`, `when.pull_request`, and `when.merge` MAY be either a single filter object or a non-empty list of filter objects (so a single process or merge gate can specify different branches/paths per repository).
  - Each filter object supports:
    - `repo` (OPTIONAL, string) and/or `repos` (OPTIONAL, non-empty list of strings): Target repository URL(s) (`https://github.com/owner/repo`) or `owner/repo` slug(s). When omitted, defaults to the repository containing the `.process.grain.md`. Cross-repository triggers only fire when the `.process.grain.md`'s repository and the triggering repository belong to the same team or owner.
    - `branches` (OPTIONAL, non-empty list of strings): Branch names or glob patterns matched against the target branch (e.g. `["main", "release/*"]`).
    - `paths` (OPTIONAL, non-empty list of strings): File paths, folder paths, path globs, or full forge file/folder URLs:
      - A repo-relative file path (`src/handler.go` or `/src/handler.go`), folder path (`src/auth/` or `/src/auth`, which matches any edited file under `src/auth/**`), or glob (`schemas/**/*.sql`).
      - A full forge file or folder URL (e.g. `https://github.com/acme/payments/tree/main/src/auth` or `https://github.com/acme/payments/blob/main/schemas/v2.sql`), which matches edits to that file or folder in that repository (and branch, if `/blob/<branch>/` or `/tree/<branch>/` is given).
    - `ignore` (OPTIONAL, list of strings): File paths, folder paths (`docs/`), or globs excluded from matching.
    - At least one of `repo`, `repos`, `branches`, or `paths` MUST be specified on each filter object (GF020).
- **`workflow: <workflow-filter> | [<workflow-filter>, ...]`**
  - Fires when a CI workflow run or check suite is triggered, succeeds, or fails (e.g., to investigate CI failures, gate releases on a qualification suite passing, or run post-build analysis).
  - Accepts either a single workflow filter object or a non-empty list of workflow filter objects (GF020). Each filter object supports:
    - `workflow` (OPTIONAL, non-empty string) and/or `workflows` (OPTIONAL, non-empty list of non-empty strings): CI workflow name(s), glob pattern(s), or workflow file path(s) (e.g. `"grain CI"`, `"e2e-*"`, `".github/workflows/release.yml"`). At least one of `workflow` or `workflows` MUST be specified on each filter object (GF020).
    - `events` (OPTIONAL, non-empty list of `"triggered"` | `"succeeded"` | `"failed"`): Lifecycle transitions that fire the process. Defaults to `["succeeded", "failed"]` when omitted. Aliases `"started"` -> `"triggered"`, `"success"` -> `"succeeded"`, and `"failure"` -> `"failed"` are accepted and normalized to the canonical names; any other value is an error (GF020).
    - `repo` (OPTIONAL, string) and/or `repos` (OPTIONAL, non-empty list of strings): Target repository URL(s) or `owner/repo` slug(s) (same-repo by default; cross-repo allowed within the same team or owner).
    - `branches` (OPTIONAL, non-empty list of strings): Branch names or globs matched against the workflow run's head branch (e.g. `["main", "release/*"]`).
  - *Note (non-normative):* forge CI webhooks map onto these three transitions; on GitHub, `workflow_run` / `check_suite` with `action: "requested" | "in_progress"` is `"triggered"`, `action: "completed"` with `conclusion: "success"` is `"succeeded"`, and `action: "completed"` with `conclusion: "failure" | "timed_out" | "startup_failure"` is `"failed"`.
- **`manual: {}`**
  - Empty mapping `{}`. Allows the process to be triggered on demand by a human or operator.
- **`scenario: <string> | {description: <string>}`**
  - Either a non-empty string or a mapping `{description}` with a non-empty `description` string, describing a specific scenario or condition under which the process applies (for example, `"When cutting a release candidate branch"`).

#### 7.2.2 Merge gating (`gate`)

When `gate: true`, the process acts as a gate on the commit or pull request that triggered it (such as a security review, architectural check, or release verification). The deployment holds merge approval for that commit or pull request until the run dispatched from the `.process.grain.md` completes and approves/passes.

#### 7.2.3 Multi-step coordination inside processes

**Decision:** Because a `.process.grain.md` is a standing 1-to-many trigger (`when`, `gate`, `references`, `draft`, `not_before`) with no `recipient` and no single completion state, `depends_on` is not permitted on `.process.grain.md` (GF020). Multi-step or cross-task coordination inside a process is expressed in natural language in the `.process.grain.md` prompt itself (where the executing agent can run steps or propose/chain concrete `.task.grain.md` sub-tasks locally) and via `references` pointing to companion process or design documents.

Example (`schema-security-review.process.grain.md`):

```markdown
---
grainfile: v1
title: Security review for widget schema and parser changes
when:
  pull_request:
    branches: ["main"]
    paths: ["src/**"]
    ignore: ["**/*.md"]
gate: true
references:
  - /docs/widget-schema.md
  - /src/widget.go
---
Review the pull request's changes to `src/` against the binary framing
rules in `docs/widget-schema.md`. Leave inline review comments on any
bounds-checking or downgrade issue.
```

## 8. References and graphs

### 8.1 Reference forms

```abnf
reference = ( remote / rooted / relative ) [ "#" anchor ]
remote    = ( %s"https://" / %s"file://" ) host "/" repo-or-forge-path
rooted    = "/" [ sub-path ]
relative  = sub-path                    ; may start with "./" or "../"
anchor    = 1*( ALPHA / DIGIT / "-" / "_" / "." )
```

8.1.1. **Remote references** are plain `https://` URLs (and `file://` in local tests), optionally followed by `#<anchor>`:
- **Standard forge file and tree URLs:**
  - GitHub / Gitea: `https://<host>/<owner>/<repo>/blob/<ref>/<sub-path>` (file) and `https://<host>/<owner>/<repo>/tree/<ref>[/<sub-path>]` (directory)
  - GitLab: `https://<host>/<repo-path>/-/blob/<ref>/<sub-path>` (file) and `https://<host>/<repo-path>/-/tree/<ref>[/<sub-path>]` (directory)
  - Gitiles: `https://<host>/<repo-path>/+/<ref>[/<sub-path>]`
- **Direct repository URLs:** `https://<host>/<owner>/<repo>` (naming the repository root at its default branch).

Examples:

```
https://github.com/acme/widgets/blob/main/docs/widget-schema.md#schema-version-header
https://github.com/acme/widgets/tree/main/src
https://github.com/acme/grain-eu/blob/main/processes/smoke.process.grain.md
https://github.com/acme/grain-eu
```

**Decision:** Only plain `https://` URLs (and `file://` in tests; an executor MUST refuse `file://` outside tests) are allowed for remote references (GF035). Legacy `git::` getter prefixes, `//` subdirectory separators, `?` query parameters, unencrypted `http://`, and SSH URLs (`git@...`, `ssh://`) are errors (GF035).

8.1.2. **Local references** name paths within the same repository at the same commit:
- **Rooted references** begin with `/` and are resolved relative to the **repository root** (`/` alone names the repository root).
- **Relative references** do not begin with `/` (they MAY begin with `./` or `../`) and are resolved relative to the directory containing the referring grainfile. A relative reference MUST NOT climb above the repository root (GF036).

8.1.3. **Anchors.** `#<anchor>` is split off at the first `#` before parsing the path. On a Markdown file (a grainfile or a `.md` asset), `<anchor>` names a heading's GitHub-style slug (lower-cased heading text with punctuation removed and spaces replaced by `-`). On a folder reference, an anchor is not allowed (GF030).

### 8.2 Resolution

To resolve a reference from a grainfile in repository `R`:
1. Split off any `#<anchor>` (§8.1.3).
2. Parse the reference according to §8.1 (reporting GF035 if malformed).
3. For a remote reference, locate the target repository at `<ref>` (or its default branch for a direct repository URL). In offline linting, a `stubs.yaml` file at the top of the linted workspace maps repository URLs to local subdirectories; unmapped remote repositories are treated as external and not resolved further.
4. For a local reference, resolve `/`-rooted paths from `R`'s root and relative paths from the referring file's directory, reporting GF036 if the normalised path climbs above `R`'s root.
5. Check that the target file or directory exists in the target repository (GF030 if missing). A `/blob/<ref>/<sub-path>` reference MUST resolve to a file, and a `/tree/<ref>[/<sub-path>]` reference, a reference with a trailing `/`, or a direct repository URL MUST resolve to a directory (GF030 if it does not exist).
6. If `#<anchor>` was given on a Markdown file, verify that a heading with that slug exists in the file's body (GF030 if absent).

### 8.3 Target kinds

References in fields that expect a specific kind of target MUST resolve to that target kind when the target repository is available (GF032):
- **`depends_on` (valid on `.task.grain.md` only; GF020 on `.process.grain.md`):** each entry MUST resolve only to a concrete `.task.grain.md` (`task`) grainfile. A `.task.grain.md` (or rendered `.task.grain.md.jinja2`) whose `depends_on` targets a `.process.grain.md`, an unrendered `.jinja2` template (`.task.grain.md.jinja2` or `.process.grain.md.jinja2`), an asset file, or a directory is an error (GF032), because a standing process has no single terminal completion and an unrendered `.jinja2` file is a template rather than a concrete task instance.
- **`recipient` (and `requestor` when it is a deployment or URL identity on a `.task.grain.md`):** MUST resolve to a repository or directory, never a file (GF032).
- **`references`:** MAY resolve to any grainfile, grainfile template, asset file, directory, or repository.

### 8.4 Dependency graphs

8.4.1. **`depends_on` graph.** The directed graph whose edges run from each grainfile to the grainfiles it lists in `depends_on` MUST be acyclic. Any cycle in `depends_on` is an error (GF034).

### 8.5 Canonical form

The **canonical reference** of a repository, folder, or grainfile is its plain `https://<host>/<repo-path>` URL (or `https://<host>/<repo-path>/tree/<ref>/<sub-path>` for a folder and `https://<host>/<repo-path>/blob/<ref>/<sub-path>` for a file), with `<host>` lower-cased, default port `:443` and trailing `.git` stripped from `<repo-path>`, and `.` or `..` segments normalised. Deployments compare `recipient` identities, source repository/folder authority, and grainfile identities using canonical form.

## 9. Execution, import, and export

9.1. **Sources, import, export, and drafts.**
- **Repositories:** A deployment watches its attached repositories for `.task.grain.md` files whose resolved `recipient` (defaulting to `/`) matches the deployment's own canonical identity, and for `.process.grain.md` files it subscribes to by repository, enclosing folder, or `.process.grain.md` URL. The repository's branch protection and review policy are what authenticate such a grainfile (§11.1).
- **Issues:** A deployment MAY also import a `.task.grain.md` or `.process.grain.md` from an issue, either by extracting YAML front matter from the issue body or by synthesizing `grainfile: v1` front matter and a Markdown prompt from the issue's title, description, and labels. An issue-sourced grainfile has no branch protection behind it, so its principal is the issue author and it is admitted under the rules of §11.1 (write access or allowlist, no automatic approval of `capabilities`, `recipient` confined to the issue's own repository, nothing invented by synthesis). The deployment MUST re-validate and re-park the task if the issue is edited after approval.
- **Export:** A deployment MAY export a task or standing process as a `.task.grain.md` / `.process.grain.md` document to a repository or an issue. Because a `.task.grain.md` on a default or protected branch is executed (§9.2), an export to a repository MUST land on a non-default branch (for example, as a pull request) or carry `draft: true`; it MUST NOT write to a default or protected branch. An exporter MUST record the originating task's identity (deployment URL and task identifier) in an `x_*` extension field of its choosing and MUST recognise that marker on re-import so that a round trip does not create a second task. Exported prompt bodies and `with:` arguments are repository content: the exporter MUST NOT include secrets or credential material.
- **Drafts:** When `draft: true` (§6.3), deployments MUST ignore the grainfile—neither queuing a `.task.grain.md` nor registering or firing a `.process.grain.md` trigger or merge gate—until `draft` is set to `false` or omitted.

9.2. **No translation step.** Non-draft grainfiles are executed directly without an intermediate template-rendering or task-compilation agent:
- **Executing a `.task.grain.md`:** `.task.grain.md` files execute only from default or protected branches (§11.1), never from unmerged pull-request heads. If `not_before` is set (§6.4), the deployment holds the task as deferred and MUST NOT execute it before `not_before`. Once `not_before` (if any) has been reached and every `.task.grain.md` in `depends_on` has completed successfully, the deployment queues and runs the task directly. The `.task.grain.md`'s `title` is the task title, its Markdown body is the prompt, its `references` are provided as context to the run (structurally delimited from the prompt; §11.5), and the source `.task.grain.md` URL (along with optional `requestor` attribution) is recorded on the task.
- **Executing a `.process.grain.md`:** Subscribed deployments register the `.process.grain.md` directly as a standing trigger (and merge gate when `gate: true`). If `not_before` is set (§6.4), the process does not fire before `not_before`. Once active, whenever any condition in `when` matches, each subscribed deployment dispatches a run using the `.process.grain.md`'s Markdown body as the prompt and `references` as context. When `gate: true`, the deployment holds the triggering commit or pull request until the run succeeds.

9.3. **Lifecycle: identity, idempotency, edits, deletion, and `draft: true` withdrawal.**
- **Canonical identity:** A grainfile's identity is its canonical `(repo_url, file_path)` pair on the repository's default/protected branch.
- **Idempotency after completion:** Once a `.task.grain.md` identified by `(repo_url, file_path)` has completed (`done` / `completed` / `success`), subsequent pushes or edits to that file path MUST NOT re-queue or re-run the task.
- **Editing an incomplete task:** Editing a `.task.grain.md` while it is still queued or deferred (not yet completed) updates its indexed definition (`title`, prompt body, `not_before`, `depends_on`, `references`, `recipient`) in place.
- **Deleting or drafting a grainfile:** Deleting an active `.task.grain.md` or updating it from `draft: false` to `draft: true` withdraws the task: if it has not yet completed, any queued or running run is cancelled and tasks that `depends_on` it see it as cancelled rather than completed. Deleting a `.task.grain.md` that has already completed preserves its completion record so downstream `depends_on` consumers stay satisfied. Deleting an active `.process.grain.md` or updating it to `draft: true` withdraws the process, and subscribed deployments MUST unregister its trigger and gate immediately.

## 10. Errors and lint

10.1. A linter reports each finding with its lint code, severity (`error` or `warning`), file path, and human-readable message.

10.2. **Severity.** An *error* fails validation and causes a deployment to refuse the broken grainfile. A *warning* reports a non-fatal condition (such as an unknown non-`x_` front-matter field or a file with an unregistered kind suffix) without failing validation.

| Code | Severity | Condition |
| --- | --- | --- |
| GF001 | error | A grainfile stem violates §4.1 (`<stem>.<kind>.grain.md[.jinja2]` ASCII naming grammar `^[A-Za-z0-9][A-Za-z0-9._-]*$`). |
| GF003 | error | Malformed file: missing or unclosed `---` front matter, invalid UTF-8, NUL byte or BOM, Git LFS pointer, or unparseable YAML (§4.2.4, §5.1, §5.2). |
| GF004 | error | Front matter outside the YAML subset: not a mapping, non-string key, duplicate key, or uses YAML anchors, aliases, merge keys, or tags (§5.3). |
| GF005 | warning | A Markdown file has an unregistered `.<kind>.grain.md` (or `.<kind>.grain.md.jinja2`) suffix (§4.2.3). |
| GF006 | error | A path or file size limit of §4.1.4 or §4.3 is exceeded (including rendered `.jinja2` output), or two paths in a repository differ only in case. |
| GF020 | error | Schema or field validation failure: missing, unsupported, or non-string `grainfile` version (`"v1"` required), missing required field (`grainfile`, `title`, or `when` on `process`), prohibited field (`recipient`/`requestor`/`depends_on` on a `process`, or `when`/`gate` on a `task`), wrong field type (such as non-boolean `draft`), multiline `title` or `title` outside 1–200 characters, invalid `requestor` or `recipient` identity, invalid `capabilities` reference (`<capability_id>[#<method>]`, §6.6) or `with` mapping, invalid RFC 3339 (`date-time`) timestamp in `not_before`, unknown `when` trigger source, or invalid `schedule.cron` / `schedule.timezone` (non-IANA timezone) / event or workflow filter (§3.1, §6, §7). |
| GF022 | warning | Unknown front-matter field not beginning with `x_` (§3.3). |
| GF030 | error | A reference does not resolve: missing file, missing directory, or missing Markdown heading `#<anchor>` (§8.2). |
| GF032 | error | A reference resolves to the wrong kind: `task` `depends_on` targets a `process` or an unrendered `.jinja2` template instead of a concrete `.task.grain.md`, `depends_on` target is not a grainfile, or a deployment identity resolves to a file instead of a repository or folder (§6.2.2, §8.3). |
| GF033 | error | Jinja2 template syntax or evaluation error in a `.task.grain.md.jinja2` or `.process.grain.md.jinja2` file (§5.7). |
| GF034 | error | Cycle in `depends_on` (§8.4), or forbidden external-inclusion Jinja2 tag (`include`, `extends`, `import`, `from`) in a `.grain.md.jinja2` template (§5.7). |
| GF035 | error | Malformed reference or `from_template`: not a valid `https://` (or `file://`) URL or local path, uses a `git::` prefix, `//` path separator, unsupported scheme, or query parameter, attaches `#<anchor>` to a deployment identity or `from_template`, or `from_template` does not end with `.task.grain.md.jinja2` (on a task) / `.process.grain.md.jinja2` (on a process) (§6.2.2, §6.5, §8.1). |
| GF036 | error | A relative reference climbs above its repository root (§8.1.2). |

## 11. Security considerations

11.1. **Who may put work in front of a deployment.** The authenticated source of a repository-backed `.task.grain.md` or `.process.grain.md` is the repository and folder URL where the file lives — governed by that repository's branch protection, merge policy, and reviews — not its optional `requestor` attribution field (§6.2).
- `.task.grain.md` files in Git repositories MUST execute only from default or protected branches, never from unmerged pull-request heads or non-default feature branches.
- On `when: {pull_request: ...}`, the `.process.grain.md` definition (its prompt body, `when`, `gate`, `capabilities`, and `references`) MUST be read from the target base branch, never from the untrusted pull-request head commit. A run dispatched for a pull request whose head comes from a fork, or whose author lacks write access to the repository, is handling untrusted input: it MUST receive no capability grants unless the repository's policy explicitly opts a capability in for that trigger, and it MUST NOT be given write credentials for the repository by default.
- An **issue-sourced grainfile** (§9.1) is authenticated by nothing but its author. A deployment MUST validate it against the `grainfile: v1` schema and then: execute it only if the issue author holds write (or maintain) access to the repository or is on the repository's configured allowlist, and otherwise file it unapproved for a human; never auto-approve its `capabilities`, whatever the author's standing; refuse a `recipient` that does not resolve to the issue's own repository; and, when synthesizing front matter, never invent `capabilities`, `references`, `recipient`, or `depends_on` the issue text did not state.
- A **template** (`.grain.md.jinja2`) is rendered only when it comes from a repository; templates carried in issues or other untrusted channels are never rendered (§5.7).

11.2. **Repository, recipient, and reference authorization.** A deployment MUST verify that it is authorized for the repository of any `recipient`, `repo`, or `folder` URL that it watches, imports, fetches references for, or records completion for, preventing an unauthorized deployment or issue author from claiming work, reading private references, or spoofing completion for another team's repository or deployment. Authorization of the *deployment* is not authorization of the *grainfile*: a cross-repository reference (§8.1.1) is fetched with the deployment's credentials at the request of whoever can commit to the source repository, so a deployment MUST materialize a cross-repository reference only when the target repository is public, or belongs to the same team or owner as the source repository (the rule cross-repository triggers already follow, §7.2.1), or the pair is explicitly allowed by deployment policy. Otherwise the reference is listed to the run as unavailable and not fetched.

11.3. **Commit-SHA pinning and regular-file/tree check on reference fetches.** When a grainfile is indexed or imported from a repository at a commit SHA, its `authorized_references` MUST be pinned to that exact commit SHA, and content fetches MUST fetch referenced files or directory trees at that pinned commit SHA rather than a mutable branch ref. Repository content fetchers MUST verify that fetched objects are regular files or directories and reject `symlink` and `submodule` git objects.

11.4. **Bounded `depends_on` evaluation.** When evaluating `depends_on` graphs across processes and tasks, evaluators MUST maintain a visited set of grainfiles and enforce cycle detection (`GF034`) to prevent cyclic denial-of-service.

11.5. **Structural separation of prompt and references.** Executors MUST structurally delimit the Grainfile Markdown body (trusted instructions from the base branch) from materialized `references` and pull-request diffs (untrusted data), tagging each materialized reference and diff with its source URL and pinned commit SHA so the executing agent never confuses untrusted reference contents with trusted grainfile instructions.

11.6. **Host-scoped credentials and no local file URLs in production.** References never carry credentials (§8.1.1). When fetching a cross-repository reference, a deployment MUST only send credentials configured for that reference's host. `file://` references exist only for local test suites; an executor MUST refuse `file://` references outside tests (§8.1.1).

11.7. **Resource limits.** Readers enforce the file, front-matter, and path-length bounds of §4.1.4 and §4.3 before parsing YAML or dispatching runs, and renderers enforce the template time and size bounds of §5.7.

11.8. **Export safety.** Exports never target a default or protected branch and never carry secrets (§9.1), so an exported grainfile cannot execute, or leak, merely by being written.

## 12. Registries of identifiers

12.1. **Kinds:** `task`, `process`.

12.2. **Trigger sources:** `schedule`, `push`, `merge`, `pull_request`, `workflow`, `manual`, `scenario`.

12.3. **Versions:** `v1` (`docs/grainfile-v1.md`, `docs/grainfiles-spec.md`, `schemas/grainfiles/`).

12.4. **Adding identifiers** requires a revision of this specification updating this section, `docs/grainfile-v1.md`, `schemas/grainfiles/`, and `tools/grainfiles/grainlint.py` together.

## Appendix A. The worked example

[`examples/grainfiles/`](../examples/grainfiles/README.md) contains two example repositories mapped via `stubs.yaml`; `python3 tools/grainfiles/grainlint.py examples/grainfiles` validates them all:

- **`widgets/`** (`https://github.com/acme/widgets`): a code repository with design notes (`docs/widget-schema.md`), Go source (`src/widget.go`), three discrete tasks and one Jinja2 task template in `.grain/tasks/` (`2026-10-16-widget-schema-v2.task.grain.md`, `2026-10-16-widget-schema-v2-migration.task.grain.md` omitting optional `requestor` and using `depends_on` and `not_before: "2026-10-25T09:00:00Z"`, `2026-11-01-drop-widget-v1-support.task.grain.md` with `draft: true` and `not_before: "2026-11-01T00:00:00Z"`, and `rollout-region.task.grain.md.jinja2` parameterizing `recipient`, `requestor`, `title`, and the rollout prompt), and two reusable processes plus one Jinja2 process template in `.grain/processes/` (`schema-security-review.process.grain.md` with `gate: true` on pull requests and pushes under `when:`, `release-cut-check.process.grain.md` with `scenario` and `manual` triggers under `when:`, and `branch-audit.process.grain.md.jinja2`).
- **`grain-eu/`** (`https://github.com/acme/grain-eu`): a deployment repository with a two-step nightly qualification suite in `processes/qualification/`: `smoke.process.grain.md` (with `when.schedule` in `Europe/Berlin` and cross-repo `https://` `/tree/` and `/blob/` `references`) and `report.process.grain.md` (with `when.schedule` and `when.manual`, referencing `./smoke.process.grain.md` and `./criteria.md#pass-thresholds` in `references`).

Broken fixtures exercising the lint codes of §10 are under `tools/grainfiles/testdata/bad/`, generated by `tools/grainfiles/testdata/mkfixtures.py` and checked by `python3 tools/grainfiles/grainlint.py --self-test`.

## Appendix B. Decisions made by this specification

1. **Two directly executed kinds (`<name>.task.grain.md` and `<name>.process.grain.md`) and self-contained `.grain.md.jinja2` templates.** Discrete one-off tasks are `<name>.task.grain.md` and standing/repeatable processes are `<name>.process.grain.md` (`**/*.grain.md`, 100% literal), while `<name>.task.grain.md.jinja2` and `<name>.process.grain.md.jinja2` apply sandboxed Jinja2 to the whole file to produce a standard `.task.grain.md` or `.process.grain.md` (§2.1, §4.1, §5.7, §7, §9).
2. **Mandatory version field (`grainfile: v1`) and agent quick-reference.** Front matter on every `.task.grain.md` and `.process.grain.md` (and rendered `.grain.md.jinja2` output) MUST declare `grainfile: v1` (omission or unsupported version fails with `GF020`), programmatically mapped to `docs/grainfile-v1.md`, `docs/grainfiles-spec.md`, and `schemas/grainfiles/` (§3.1).
3. **Mixed-case ASCII stems (`^[A-Za-z0-9][A-Za-z0-9._-]*$`) and case-collision detection (`GF006`).** Grainfile stems use ASCII letters, digits, `.`, `-`, and `_` (starting with an ASCII letter or digit) and reject case-insensitive path collisions within a repository (`GF006`) so filenames remain portable across case-insensitive file systems (§4.1.3).
4. **Finalized orthogonal front-matter vocabulary (`grainfile`, `title`, `draft`, `not_before`, `capabilities`, `references`, `from_template`, `recipient`, `requestor`, `depends_on`, `when`, `gate`).** Process triggers use `when` instead of `on` to avoid YAML 1.1 boolean misparsing (§5.3, §7.2.1); there is no creation-timestamp field (§6.1); `capabilities` are grant references (`<capability_id>[#<method>]` or `{capability, with}`) whose grammar is owned by `docs/capabilities-spec.md` §2.1 (§6.6); and `recipient` (defaulting to `"/"`), `requestor`, and `depends_on` are restricted to `.task.grain.md` because tasks are 1-to-1 with a single completion state while processes are standing 1-to-many triggers (§6.1, §6.2.3, §7, §8.3).
5. **Unknown fields warn; `x_*` extends; unknown trigger sources fail closed.** Unrecognised fields emit a warning (`GF022`) while `x_*` extension fields are silent; unknown `when` trigger sources are an error (`GF020`) (§3.3, §3.4).
6. **Task lifecycle idempotency, withdrawal events, and `depends_on` target kind rules.** A `.task.grain.md`'s identity is `(repo_url, file_path)`: once completed, pushes do not re-run it; editing a queued task updates it; deleting or drafting an active task or process withdraws it and cancels its runs; deleting a completed task is a no-op (§9.3). Only `.task.grain.md` may declare `depends_on` (`GF020` on `.process.grain.md`), and every `depends_on` entry must resolve to a concrete `.task.grain.md` file, never a `.process.grain.md` or an unrendered `.jinja2` template (`GF032`; §8.3).
7. **Import, export, and security invariants.** Grainfiles are imported from repositories or issues and exported to non-default branches or drafts with an origin marker (§9.1); `.task.grain.md` executes only from default/protected branches, `when.pull_request` reads `.process.grain.md` definitions from the base branch, issue-sourced grainfiles are admitted on the issue author's standing with no automatic capability approval, and runs triggered by untrusted pull requests receive no capability grants by default (§11.1); deployments enforce repository and recipient authorization and fetch cross-repository references only where the source repository is entitled to them (§11.2), commit-SHA pinning and regular-file/tree checks on reference fetches (§11.3), `depends_on` DAG cycle detection (§11.4), structural separation between trusted prompts and untrusted references/diffs (§11.5), and bounded rendering of repository-sourced templates only (§5.7, §11.7).
