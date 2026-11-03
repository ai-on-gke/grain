# Controller MCP token cost, before and after — 2026-11-02

What PR #511 (`feat(mcp): serve single controller MCP endpoint at
/api/mcp`) changed in what a run pays for grain's controller API, in
tokens, measured in-process against the surface it replaced. The
measurement is `TestControllerMCPTokenComparison` and
`BenchmarkControllerMCPTokens` in
[`pkg/ui/mcp_token_benchmark_test.go`](../../pkg/ui/mcp_token_benchmark_test.go);
this page is what they said on the branch, and what was changed because of
it.

**Result:** for the callers the endpoint was built for -- a steward or a
Director holding several scopes -- the fixed six-tool roster cuts the
schema a run carries on every turn by 86–97%, and a short triage ends with
82–94% less in context, discovery call included. For a task holding one
narrow scope (nine routes) the new surface costs slightly *more*: the
break-even is a roster of about ten routes. Two findings were fixed on the
branch before merge (F1, F2); one is left as written (F3).

## Method

Both surfaces still exist in `pkg/ui`, so both are measured against one
seeded store (twelve tasks with a long body and twelve comments each, the
weight a real deployment's tasks carry) and the same grants:

- **legacy**: `ui.AgentAPITools(...)` -- one generated `api_*` tool per
  reachable route, registered in an `mcp.Registry` exactly as
  `grain mcpserver` used to, and its `tools/list` + `initialize` read off
  that registry.
- **controller**: `POST /api/mcp` -- `initialize` (with its workflow
  guide) + `tools/list`, read as a harness reads them.

Three profiles, by what the acting task holds:

| profile   | grants                                                        | legacy roster |
|-----------|---------------------------------------------------------------|--------------:|
| ci-reader | `api:ci:read`                                                 |       9 tools |
| steward   | `api:tasks:read`, `api:tasks:write`, `api:ci:read`, `api:dispatch:read` |      50 tools |
| director  | every scope of the vocabulary (`model.APIScopes()`)           |     290 tools |

Each profile replays one short piece of work on both surfaces, in each
surface's own vocabulary, charging the controller for the
`grain_explore_scope` call it needs before it can `grain_invoke` a route:

- **ci-triage** (ci-reader): list the failing CI runs, compact.
- **task-triage** (steward, director): list the queue in summary, read
  three tasks, leave a note on a fourth.

Sizes are bytes on the wire. Tokens are bytes / 4
(`model.PromptBytesPerToken`), the same estimate the rest of grain
measures prompts with -- JSON schemas tokenize somewhat denser than prose,
so the absolute numbers read low and the ratios are what to compare.

- *schema*: what sits in context on every turn.
- *results*: the tool answers of the trajectory.
- *ctx-at-end*: schema + results -- roughly what a cached provider bills once.
- *uncached*: Σ over turns of (schema + results so far) -- what a provider
  with no prompt caching bills for the whole trajectory.

## Results (branch head, after F1 and F2)

```
profile    surface                 tools schema~tok  calls  results~tok ctx-at-end~tok   uncached~tok
ci-reader  legacy api_* roster         9       1866      1           13           1879           1879
ci-reader  controller /api/mcp         6       1958      2         1406           3365           6717
             change                 -33%        +5%  +100%      +10517%           +79%          +257%
steward    legacy api_* roster        50      14294      5        10746          25040         102466
steward    controller /api/mcp         6       1958      6         2466           4425          18358
             change                 -88%       -86%   +20%         -77%           -82%           -82%
director   legacy api_* roster       290      65866      5        10746          76612         360328
director   controller /api/mcp         6       1958      6         2466           4425          18358
             change                 -98%       -97%   +20%         -77%           -94%           -95%
```

Per call (steward; director is the same to within a few bytes):

```
legacy api_* roster:
  api_list_tasks                                 1601 bytes ~  400 tok
  api_get_task                                  12427 bytes ~ 3106 tok
  api_get_task                                  12436 bytes ~ 3109 tok
  api_get_task                                  12436 bytes ~ 3109 tok
  api_add_comment                                4084 bytes ~ 1021 tok
controller /api/mcp:
  grain_list_tasks                               1601 bytes ~  400 tok
  grain_get_task                                  680 bytes ~  170 tok
  grain_get_task                                  728 bytes ~  182 tok
  grain_get_task                                  728 bytes ~  182 tok
  grain_explore_scope(tasks, tool=comment)       1998 bytes ~  499 tok
  grain_invoke(api_add_comment)                  4084 bytes ~ 1021 tok
  (for reference: grain_explore_scope(tasks) whole group   20903 bytes ~ 5225 tok)
```

What the table says:

1. **The schema is a constant.** 1958 tokens for every caller, against a
   legacy roster that grew with the grants: 1.9k for one scope, 14k for
   four, 66k for all of them. A Director session used to spend a third of
   a 200k window on tool schemas before its first turn.
2. **`grain_get_task` is the other half of the saving.** A task's full
   record is ~3.1k tokens; its summary is ~180. Three reads is where a
   triage's bytes went on the old surface.
3. **Discovery is a real cost and has to be aimed.** A whole group's
   schemas (`grain_explore_scope(scope="tasks")`) is 5.2k tokens, more
   than the three full task records it was meant to save; narrowed to the
   route wanted (`tool="comment"`) it is 0.5k. See F2.
4. **A narrow caller gains nothing.** ci-reader's nine-route roster was
   already about the size of the fixed six plus the guide, and it now
   pays a discovery call on top. The win is for the many-scoped sessions
   the endpoint was built for; the break-even is a roster of about ten
   routes (F3).

## Findings

**F1 — scope detail was pretty-printed (fixed).** `grain_explore_scope`
answered with `json.MarshalIndent`, so a scope's nested schemas carried
their indentation to the model: the `ci` group was 11.9 KB indented
against 7.4 KB for the legacy `tools/list` of the *same nine routes*.
Compact JSON brings it to 8.0 KB (−33%); the overview and
`grain_filing_context` were made compact with it.

**F2 — a whole group was the only unit of discovery (fixed).** An agent
wanting one route had to read every schema in its group: 20.9 KB for
`tasks`, which is 40 routes. `grain_explore_scope` now takes `tool`, an
exact route tool name or a word of it or of its path
(`scope="tasks", tool="comment"` → the two comment routes, 2.0 KB), and
the workflow guide tells the agent to name the tool whenever it knows what
it is after. On the branch before F1 and F2 the steward triage read 27.0k
uncached tokens; after, 18.4k.

**F3 — the fixed roster is not free (left as written).** Six tools plus a
workflow guide is ~7.8 KB, about one nine-route legacy roster. A task
granted one narrow scope pays that, and a discovery call, for a surface
that gives it nothing it did not have. The alternative -- serving a
narrow caller its routes directly as before, and the fixed roster only
past some size -- is two surfaces with two sets of tool names for the
same work, and the census, prompts and guides for both. Not worth it for
~2k tokens a turn; noted so the trade is a stated one.

## Not measured

- **Real tokenizers.** Bytes / 4 throughout. A run against a provider's
  own count (`input_tokens`, `cache_read_input_tokens` from a claude
  transcript; `promptTokenCount` from agy's) would move every absolute
  number, probably upward for the schema-heavy rows, and none of the
  ratios much.
- **What a harness does with the list.** Some harnesses defer MCP tool
  schemas or search them rather than sending all of them each turn; the
  legacy column is the cost for one that sends everything, which is what
  the `antigravity/alwaysLoad` meta on the six tools asks for, and what
  claude does. A wire capture against a stubbed model API
  (`pkg/agent/claude/live_resume_stub_test.go`'s `stubMessagesAPI`) would
  measure a specific harness; not done here.
- **Multi-turn sessions that call nothing.** A Director turn that only
  chats still carries the schema. That cost is the *schema* column times
  the turns, and needs no replay to read.

## Reproducing

```sh
go test -run '^TestControllerMCPTokenComparison$' -v ./pkg/ui   # the table, under -v
go test -run '^$' -bench ControllerMCPTokens -benchtime 3x ./pkg/ui   # benchstat-able
```
