// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

// ReviewChecklist returns the shared correctness checklist (T2-T8) carried by
// stacked reviews (orchestrator.reviewTaskBody) and this repository's gating
// pull-request review template (.grain/templates/pr-review.yml).
//
// Each bullet targets a recurring escaped-bug class from the review
// effectiveness study: callers and consumers outside the diff, external
// contracts, multi-host and edition paths, nil optional dependencies and
// nightly test flags, task-lifecycle edges, idempotent migrations and stored
// data, and docs or messages that point somewhere.
func ReviewChecklist() string {
	return reviewChecklist
}

const reviewChecklist = "" +
	"- **Beyond the diff.** For every function, type, flag, route, schema or message\n" +
	"  the change alters (on an incremental re-review, in the new commits only),\n" +
	"  find who uses it outside the diff (`git grep -n '<name>'`, including tests,\n" +
	"  `ui/`, `ui-ng/`, `docs/`, `design/spec/`, scripts and `.grain/`) and check\n" +
	"  each caller or consumer still works, or is updated, with the new behaviour --\n" +
	"  including image tags, artifacts or metadata keys a workflow or script reads\n" +
	"  that a publisher really emits. A caller left broken outside the diff is still\n" +
	"  this change's bug.\n" +
	"- **External contracts.** If the change alters anything another program parses\n" +
	"  or calls -- an MCP tool's input schema, an agent-API or REST body, a CLI flag\n" +
	"  or exit code, a forge API call (GitHub or another git host, including the\n" +
	"  edition's command-backed forge), a model provider request, a VM's metadata\n" +
	"  keys, or the host agent wire protocol -- check it against what that consumer\n" +
	"  accepts, not only grain's own tests: every JSON-schema `array` has `items`,\n" +
	"  `object` properties are typed, and `enum`/`oneOf` are valid for the strictest\n" +
	"  provider (Gemini rejects a whole tool list for one bad schema); every\n" +
	"  documented or wrapped spelling of a field or flag still decodes; keys match\n" +
	"  what the consumer reads (case, prefix, snake_case); and errors come back as\n" +
	"  the documented error rather than a raw decode message.\n" +
	"- **Multi-host and edition paths.** Tests here run one local host, github.com,\n" +
	"  SQLite or local Postgres, and the open-source build. If the change touches\n" +
	"  hosts, sandboxes, repositories, forges or registries, check what it does with\n" +
	"  a remote or dialling-in host that is slow, lost or not yet connected (never a\n" +
	"  synchronous dial on a request or poll path, and bounded retries), a\n" +
	"  repository on a non-default git host (is the host kept end to end?), the\n" +
	"  kubernetes-granule backend, a hosted VM, and seams the edition layer\n" +
	"  registers through `capability.Register` or `pkg/change`.\n" +
	"- **Optional dependencies and test shape.** For each new use of a dependency\n" +
	"  that can be absent -- a nil store, client, forge, hub, namespace or edition\n" +
	"  hook -- check the code handles it absent without panicking or silently\n" +
	"  reading nothing, and that a test builds the server or orchestrator with it\n" +
	"  unset. A new test must pass under nightly's `-count=2 -shuffle=on`: reset\n" +
	"  package-level state and restore `log`'s writer in `t.Cleanup` (you may run\n" +
	"  that single test with `-count=2 -shuffle=on`). If the change alters an\n" +
	"  output, grep for tests pinning the old one; if it adds a check or probe,\n" +
	"  confirm the test exercises the real path rather than a stub that always\n" +
	"  passes.\n" +
	"- **Task lifecycle.** If the change reads or sets task state, deferral, holds,\n" +
	"  wakes, links or comments, walk it through every state the task can be in --\n" +
	"  proposed, queued, running, awaiting_reply, awaiting_submit, completed,\n" +
	"  closed, and reopened from closed -- and the parked ones: deferred,\n" +
	"  `resume_later` on CI, a task or a time, or waiting on a question. Check that\n" +
	"  what it sets is cleared on every exit (early wake, verdict, close, reopen),\n" +
	"  that anything posted while the task was closed or parked still reaches its\n" +
	"  next run, and that two reconcilers cannot both act on the same task.\n" +
	"- **Migrations and stored data.** If the change edits\n" +
	"  `pkg/model/migrations.go`, a `*_migration.go`, `pkg/staterepo` or a stored\n" +
	"  type: check the migration is idempotent on both SQLite and Postgres (it may\n" +
	"  meet a column, row or value that already exists); that rows written by\n" +
	"  earlier versions still pass validation, state check and import after\n" +
	"  migrating; that dump and import carry every new field and declaration key;\n" +
	"  that a renamed or aliased value never expands into a retired one; and that\n" +
	"  `SchemaVersion` and `pkg/staterepo/dump.go`'s import threshold moved\n" +
	"  together. A migration that cannot be re-run safely needs\n" +
	"  `request_human_review`.\n" +
	"- **Docs and messages that point somewhere.** When an error, refusal, UI hint,\n" +
	"  CLI help string or doc tells someone to visit a route or page, set a key,\n" +
	"  pass a flag, run a command or edit a file, check that target exists and\n" +
	"  actually grants or changes what the message says (for example, a workflow\n" +
	"  file vs a repository settings page), and that `docs/`, `design/spec/` and CLI\n" +
	"  help move with any behaviour they describe."
