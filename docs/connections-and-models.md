# Connections and models

Grain keeps two things apart that used to be one record:

- A **connection** is a way to reach a provider. It has a framework
  (Antigravity, Claude, Codex, or a custom command), authentication, the team
  it belongs to, a concurrency limit, and an enabled flag. It *advertises* the
  models it can serve. That list is a capability, not a choice: a connection
  does not decide which model a run on it uses. Connections are stored as
  `agent_backends` in the settings row (`model.AgentBackend`, also named
  `model.Connection`). The advertised list is the `models` field.
  See [Advertised models](#advertised-models) for how that list is kept.
- A **model** is what the deployment or a task chooses. The deployment has a
  default model (`grain_config.default_model`) and per-model settings
  (`grain_config.model_settings`), on the default team's row until schema 22
  moved them ([single-tenant-reset.md](single-tenant-reset.md), 6-ii-b2). Today
  the only per-model setting is the reasoning effort. A task can choose its own
  model (`task.agent_model`). Models and connections are strictly per team, and
  there is no deployment-wide default.

## Deployment connections (single-tenant reset, stage 1)

Task 4680 ([single-tenant-reset.md](single-tenant-reset.md), stage 1)
makes model connections and model choices the deployment's. Where this
document says "the team's" below, read "the deployment's":

- **Every connection serves every task.** Dispatch (`AgentPool`), the
  model proxies, pinning, proposal pins and task moves read every stored
  connection, whichever team saved it (`model.DeploymentAgentBackends`).
  A connection's `team` field is read by nothing: it is kept only so a
  stored connection that carries it round-trips unchanged. Any task may
  pin any connection, and a connection may borrow the credential of any
  other saved connection of the same framework (task 4932 removed the
  last team checks, `BelongsTo` and `TeamAgentBackends`).
- **One default model and one set of per-model settings.** The default
  model and the per-model settings (effort) are the deployment's
  (`grain_config.default_model` and `model_settings` since schema 22; the
  default team's row before that), read for every task whatever its team. Template and schedule previews show the
  same fallback.
- **Per-model pauses** were already keyed by connection and model, not
  team (task 4607). Since every team now spends the same connections, a
  usage limit met by one team's run holds every team's runs on that
  connection and model.

## Seeding connections: `grain connections import`

Stage 2 (task 4681) removes every way of choosing a model outside a
model connection: the deployment-wide `agent_framework`, `gemini_model`,
`gemini_effort`, `claude_model`, `codex_model` and `codex_effort`
settings (and the daemon's `-agent-framework`, `-gemini-model`,
`-gemini-effort`, `-claude-model` and `-codex-model` flags that seeded
them), a connection's legacy `model` and `effort`, and the start-time
migrations that copied the deployment's key and model into connections
(`MigrateModelSelection`, `MigrateTeamAgentBackends`,
`MigrateAgentBackendKeys`). The columns stay in `grain_config` until the
schema reset drops them; nothing reads or writes them. `grain settings`
still accepts those flags and says they no longer set anything.

A headless or scripted install records its model connections the same
way it records git connections:

    grain connections -data-dir DIR import seed.yaml

```yaml
models:                         # matched by id; replaces that connection
  - id: gemini
    framework: antigravity      # antigravity, claude, codex or custom
    models: [gemini-3.1-pro]    # optional pin; empty means discover
    auth_mode: ambient          # optional, as in Settings
    credential_file: /run/key   # optional; stored as this connection's own key
default_model: gemini-3.1-pro   # optional; the deployment's default model
```

The other fields are `name`, `excluded_models`, `command` (a custom
connection's), `cli_path`, `concurrency` and `disabled`. The file can
carry `git:` entries too ([git-hosts.md](git-hosts.md)). Grain never
reads it at runtime, and importing it twice changes nothing.
`scripts/setup.sh` seeds through it once: each of
`GRAIN_GEMINI_API_KEY`, `GRAIN_CLAUDE_CODE_OAUTH_TOKEN` and
`GRAIN_OPENAI_API_KEY` it is given becomes a connection (`gemini`,
`claude`, `codex`) holding its own copy of the key, and the first of
`GRAIN_GEMINI_MODEL`, `GRAIN_CLAUDE_MODEL` and `GRAIN_CODEX_MODEL` that
is set becomes the default model.

## Advertised models

A connection whose provider can be asked (Antigravity, Claude, Codex)
advertises **every model its provider lists, by default, with nothing to
save**. Grain asks the provider (`ui.Client.AdvertiseDiscoveredModels`, using
the same listers as `GET /api/agent-models`) right after the connection or a
credential is saved, and again every ten minutes (`ui.ModelDiscoveryInterval`).
It keeps the answer on the connection as `discovered`: `models`,
`refreshedAt` and `error`. This is a cache that grain owns, not configuration.
A settings save never writes it; the stored value is carried over whatever
the request says. A pass whose answer hasn't changed writes nothing, except
once an hour to move `refreshedAt` on. A model the provider adds turns up in
every picker, and in dispatch, at the next pass. A model it drops stops being
advertised at the same point.

The operator restricts a connection in one of two ways. Both are optional:

- **`excludedModels`**: models not to advertise even though the provider
  lists them. Everything else the provider lists, now or later, is still
  advertised.
- **`models`**: a pin. When it lists anything, the connection advertises
  exactly those models and nothing the provider adds later. `excludedModels`
  is ignored while there is a pin.

So `model.AgentBackend.SupportedModels` is the pin if there is one.
Otherwise it is `discovered.models` minus `excludedModels`. Before the
provider has ever answered it is empty: grain compiles in no model name and
does not guess one. Such a connection shows "no models — set one" (see
[No models](#no-models)). A connection
whose provider stops answering keeps advertising the last list it gave.
`discovered.error` says why it couldn't be refreshed, and the connection's
page shows that error next to the list's time. A custom command can't be
asked, so its `models` is the whole of what it serves, as before.

**Migration.** Before discovery was live, it wrote the provider's list into
`models` once, and nothing recorded that it had. So the first time a
connection is asked under this scheme (it has no `discovered` yet), a saved
`models` that names exactly the same set as the provider reports is treated
as that old fill and cleared, and the connection advertises live from then
on. Any other saved list (a subset, a superset, or a list with an alias)
stays a pin, because widening a hand-curated list without asking is the one
thing migration must not do. The connection's page offers **Advertise all
models** to make it live in one click. A connection is only considered for
migration once, so a list pinned by hand later stays pinned.

## Dispatch

A run asks for a model: the task's own, or else its team's default. Grain
round-robins across every connection that meets all of these:

- it is one of the deployment's connections (since stage 1 of the
  single-tenant reset, every stored connection, whichever team saved it);
- it is enabled;
- it advertises that model;
- it has a free slot under its `concurrency` (0 means no limit);
- it is not cooling off after a model refusal;
- its credential is not paused after a usage limit for the model the run would
  pass it.

### Usage limits

A usage limit pauses **one model on one credential**: the model the run passed
the CLI (the resolved model -- the task's, else its team's default, else the
connection's own), on the connection that owns the key that met it (the one it
borrows a key from with `credentialBackendId`, else itself). Providers often
limit per model, so the connection's other models keep dispatching, and only
runs on the same connection *and* model are stopped. Every connection spending
that key is paused with it for that model, and nothing else is -- not the
team's connections on other keys, and never another team's, since a connection
may only borrow from its own team.

The pause **escalates to the whole connection** -- every model on it paused, and
every run on it stopped -- when:

- the provider's error says the refusal is account- or key-wide: credits or
  billing exhausted (`insufficient_quota`, "credit balance"), an organisation or
  project quota, "all models", or Claude Code's plan limits (its session and
  weekly limits; an "Opus limit" is that model family's). Each framework's
  parser reads these hints into `agent.UsageLimitError.Scope` and `.Model`, and
  a [custom command](custom-agent-backends.md) can say `"scope": "account"`;
- or a second, different model on the same connection meets a limit while the
  first model's pause is still open.

A run grain cannot name the model of, and a run on no connection (the
deployment-level credentials), keep the old scoping: the whole connection, or
the team's runs on that framework.

**"Model default" and `auto`.** A task that chose no model and runs the
provider's own choice (agy's `--model auto`) is paused under the label grain
passed -- `auto` -- unless the CLI's error names the concrete model it was
refused on. Then the pause is on that model, and holds `auto` runs on the
connection too, since the provider is choosing the model it has just said is
spent; tasks that chose another model are not held.

**A paused model is passed over.** The work rotates onto the other
connections serving the model first. What happens when every one of them has it
paused depends on what the task chose:

- **A task that chose nothing runs down the default model list, across
  frameworks.** The deployment's default model may be a list
  (`default_model: gemini-3.1-pro, claude-opus`). Each dispatch tries it first
  model first, and runs the first one with a free connection. Each entry runs on
  its own framework (`model.ModelFramework`: the framework of the connections
  serving it), so a deployment whose Gemini models are all paused carries on on
  `claude-opus`, on the claude CLI. The deployment's `agent_framework` only
  settles ties: it is the framework for a default model whose framework is
  ambiguous, and for the rotation of a deployment with no default model at all.
  There is no stickiness: once the earlier model is free again, the next
  dispatch goes back to it.
- **A task that named a framework** (`task.agent_framework`) is restricted to
  the default models on that framework, and waits when they are all paused.
- **A pin never falls back.** A task that chose a model (`task.model`) or a
  connection (`task.agent_backend_id`) runs on it or waits. Its pane says so:
  "model X is paused after a usage limit on 1 connection ... -- the task is
  pinned to X; it does not fall back".

Selection happens in one place (`daemon.AgentPool.resolve` and
`selectCandidateLocked`), so the admission check
(`orchestrator.Deps.ModelAvailability`) and the build that follows it agree on
the model and the framework.

The banner, `GET /api/pause` and `grain pause` name the model (where the pause
is on one), the connection, and whether and why a pause was escalated.
Settings -> Models & connections marks each paused model in its connection's
list, with its reset time and a Resume button. The deployment's owner lifts one
model's pause with that button, `DELETE /api/pause?connection=<id>&model=<m>`,
or `grain pause -lift -connection <id> -model <m>`; leaving the model off lifts
every pause on the connection, and leaving the connection off lifts everything
the caller may lift. The daemon logs each pause as it opens, expires or is
lifted, saying whether it is model-scoped or escalated and why.

The run is asked for the team's own settings for that model (its effort),
whichever connection serves it. The effort a run passes is decided in this
order:

1. The team's setting for the model. "Model default" (no setting, or one with
   an empty effort) passes no effort at all, and the model uses its own.
2. Nothing else. A connection carries no effort of its own, and there is no
   deployment-wide effort (stage 2).

Some models take no effort, whatever is configured. agy refuses `--effort`
for `auto` and for a catalog name that carries its own effort suffix
(`gemini-3.8-flash-high`), so grain passes none with either. `agy models` does
not say which models take an effort, so `auto` is recognised by its name.

A run that names no model at all passes no effort either: the effort pairs
with a model, so a run left to the provider's default model gets the
provider's default effort.

### The default model

Grain never picks a team's default model on its own, and has no model name
of its own to fall back on:

- If the team's default model is one a connection advertises, tasks that
  chose no model run on it.
- If the team has no default, grain **asks** rather than saving one. Team
  settings shows a "Pick a default model" prompt above the **Default model**
  picker, listing what the connections discovered, and the team's setup
  checklist carries an optional "Pick a default model" step.
- Until someone picks, the rotation spans every enabled connection of the
  team, and each runs its **provider default**
  (`model.AgentBackend.EffectiveModel`). That is the first model the operator
  pinned on the connection, if any; otherwise `auto` for agy (agy picks the
  model itself), and the first model the provider lists for any other
  framework. The UI labels it "provider default (unset)" everywhere a model
  is shown, so nobody mistakes it for a choice.

There is no deployment-wide model behind any of this: a run's model is
the one chosen for it, the default model, or its connection's provider
default, and nothing else.

### No models

A connection that advertises nothing has **no models**. Either its provider
has never answered and grain has no cached list, or it is a custom command
with an empty list. Its card says "Advertises: no models — set one". A task
that would run on it, because it chose no model and its team has no default,
is not dispatched onto a guessed name. It waits with "connection C has no
models -- set one". The wait clears once the provider answers, someone lists
the connection's models, or the team picks a default another connection
serves. A connection whose provider later stops answering keeps its last good
list, so it never drops back to having none.

When no connection can take the run, the task is not dispatched. It waits in
the queue and shows why:

- "no enabled connection of team T serves model X", when nothing enabled in
  the team advertises the model. If the provider behind one of the team's
  connections has stopped listing X, the reason says so ("-- the provider
  behind N of its connections no longer lists it");
- "all connections serving X are busy", when everything that serves the model
  is at its concurrency limit or cooling off;
- "connection C has no models -- set one", when the task chose no model, its
  team has no default, and the connections it would run on advertise nothing
  ([No models](#no-models)).

This reason comes from the admission step (`orchestrator.Deps.ModelAvailability`).
It is recorded the same way a capability wait is (capability `model`), and it
clears when a connection frees up.

## Pinning a connection

A task can also name a specific connection (`task.agent_backend_id`). A pin
bypasses the rotation:

- it is used even when the connection is disabled, which is how a connection
  kept out of the rotation still gets the occasional job;
- it does not wait on the connection's concurrency;
- it does not advance the rotation.

The pinned connection must advertise the task's model. A request that pins a
connection with a model it does not serve is refused when the task is
created. If the provider later stops listing that model, the task waits
with "connection C's provider no longer lists model X" rather than being
dispatched to a model the provider no longer serves.

A task no longer needs to name a framework, because a model implies one.

## Changing a task's model after it is filed

A task's model and pin are not fixed when it is filed. They can be changed in
any state, from **Edit** in the task pane, from **Bulk edit**, from
`PATCH /api/tasks/{id}` (`model`, `agentBackendId`), or from the shell:

```sh
grain update 53 -model gpt-6              # next run asks for gpt-6
grain update 53 -model "" -connection ""  # back to the team default and the rotation
grain update 53 -model gpt-6 -connection codex-day
```

Dispatch reads the task's model again every time it starts a run, so a change
reaches the **next** run. A run that is already going keeps the model and the
connection it started on.

The usual reason to switch is that a model's account met its usage limit. You
don't have to cancel anything first:

1. The run that meets the limit ends as `paused`. That outcome doesn't count
   against the task's retry budget. The model it was on is paused on the
   connection whose credential met the limit (or the whole connection, when the
   limit is account-wide -- see [Usage limits](#usage-limits)), and every other
   run on that credential and model is stopped.
2. The task goes back to the queue. If another connection in the team serves
   the same model, the task moves to it without anyone doing anything.
   Otherwise the task waits there until the window resets.
3. While it waits, change its model (or pin a connection on a different key).
   The next dispatch cycle checks the pause against the new choice and starts
   the task on it.

What the next run keeps depends on how far the change goes:

- **Same framework** (one Claude model to another, or a second Claude account):
  the next run resumes the same agent conversation. The suspended session
  (`model.SuspendedSession`) is keyed by framework, not by model, and the CLI
  is passed the new `--model` on resume.
- **Different framework** (Claude to Codex, say): one CLI can't open another
  CLI's session, so the next run starts a new conversation. It still gets
  everything grain keeps for the task: the task's conversation and handover,
  attempt history and comments, and whatever the last run pushed to the
  task's branch. What is lost is the agent's own in-context reasoning and
  anything the last run left uncommitted in its sandbox. Sandboxes are built
  per run, so that state is lost on every retry anyway.

The same holds when the default model list falls over onto another framework
by itself. An interactive or console-session task whose next run lands on a
different framework from the session its last run left starts fresh from
grain's prompt and the conversation thread, as a new task would; the other
framework's session is never handed to it. Grain notes the switch in the
conversation ("This turn ran on claude-opus (claude) because gemini-3.1-pro is
paused until 14:05 UTC ..."), and the run after it switches back once the
earlier model is free.

## A connection's CLI

A Claude, Codex or Antigravity connection runs a CLI (`claude`, `codex`,
`agy`) as a subprocess of the daemon. Which binary is decided in this order:

1. the connection's own **CLI path** (`cliPath`, `model.AgentBackend.CLIPath`);
2. the deployment flag: `-claude-path`, `-codex-path` or `-agy-path`
   (the installer's `GRAIN_CLAUDE_PATH`, `GRAIN_CODEX_PATH`, `GRAIN_AGY_PATH`);
3. the bare name on the daemon's own `PATH`.

So two connections of one framework can run two builds of its CLI, for example
one pinned to an older `claude` while another tries a new one, and one team's
connection can use a CLI installed somewhere the deployment default is not.
The path is set in Team settings (Models & connections, then the connection,
then **CLI path (optional)**). The field's hint names the deployment default it
falls back to.

`cliPath` is separate from `command`. `command` names a custom backend's
executable, read relative to the state repository. A custom connection has no
`cliPath`, and saving one is refused.

Everything that runs the connection's CLI uses its path: its runs
(`daemon.BuildClaudeFramework`, `buildCodexFramework`,
`BuildAntigravityFramework`), Codex's version check before a run, and model
discovery for that connection (`CodexModelLister`, `AgyModelLister`). Discovery
is per connection, so two connections can report different model lists. Claude
discovery asks Anthropic rather than a binary, so its path doesn't change the
listing. Discovery doesn't read a CLI version, so the page doesn't show one.

Some limits:

- **Validation.** A path is checked when it's saved, by the daemon that will
  run it. It must be absolute, or a bare name the daemon's `PATH` resolves,
  and it must be an executable file the daemon's account may run. Anything
  else is refused with a message naming `cliPath`. A path is checked only
  when it changes. A binary removed later fails that connection's runs and
  discovery with an error naming the connection; it doesn't fall back to the
  deployment's binary.
- **Container mode.** The path must exist inside the daemon's container.
  Unlike `GRAIN_*_PATH`, nothing bind-mounts it, so a host path the image
  doesn't carry is refused when it's saved.
- **Run hosts.** A Claude run sent to a run host execs the path on the run
  pod, as `-claude-path` does, so the binary must be there too.
- **Who may set it.** Setting a CLI path is effectively "run this binary as
  the daemon": the daemon execs it under its own account, with the
  connection's credential. Only the people who may edit the connection's
  credential may set it: a system admin (`PUT /api/settings`) or an admin of
  the connection's team (`PUT /api/teams/{id}/agent-backends/{backendId}`).
  Every change is written to the team's audit trail
  (`GET /api/teams/{id}/audit-logs`, kind `connection`, resource
  `connection/<id>`), with the old path, the new one and who changed it.
  `cliPath` is part of the exported settings row, like `command`. A settings
  file applied from the state repository sets it with the authority of
  whoever may write that repository, and the save-time check doesn't run.

## An Antigravity connection's model-turn deadline

agy retries provider errors (429, 503, `RESOURCE_EXHAUSTED`) without saying so
on its stream, so grain bounds each model turn itself
(`pkg/agent/antigravity/turndeadline.go`). Thinking output doesn't reset the
clock; tool calls pause it. Two limits apply, and both can be set per
connection under **Advanced** in the connection editor:

- **Model retry cutoff** (`modelRetryCutoff`, default `100s`). From this point
  on, grain reads the turn's agy `--log-file` every few seconds. A turn whose
  log shows agy retrying a provider error is cancelled, and the message quotes
  the record. If the log can't be read at all (an older agy, or a format grain
  doesn't recognise), every turn is cut here, and the daemon logs once that
  the log-based signal is unavailable.
- **Model turn timeout** (`modelTurnTimeout`, default `15m`). A turn with no
  provider errors logged runs this long before it's cancelled as a slow
  model.

Both are Go durations from `30s` to `60m`; empty means the default, and the
cutoff can't be longer than the timeout. Each timeout message names the limit
that fired, its value, the connection, and the setting that changes it.
Claude and Codex have no per-turn limit, so the fields appear only for
Antigravity connections.

## Templates and schedules

A template and a schedule can choose a model too (`template.agent_model`,
`schedule.agent_model`). The model of a task filed from a template is
decided in this order:

1. the task's own, when the request names one, or pins a connection;
2. the schedule's, when a schedule fired it and chose one;
3. the template's;
4. otherwise empty, so the team's default model decides at dispatch.

The orchestrator files review, base-branch repair and release triage
tasks from templates, and each of them carries that template's model.
A schedule that runs a suite cannot choose a model, because each template
in the suite decides its own. A template and a schedule name a model,
never a connection. A value that is a configured connection's id is
refused, rather than read as a pin the way the older task spelling is.

Repository declarations accept the same choice as `model:` in
`.grain/templates/*.yml` and `.grain/schedules/*.yml` (docs/declarations.md).
`task.agent_framework` is still read for older tasks.

## Standing sessions

A standing session (the director, or one made with **New session**) can
choose a model as well (`model.ConsoleSession.Model`, stored in
`grain_config.console_sessions`). **New session** opens an empty chat
rather than a form: the session is created, and named, from the first
message sent in it (`POST /api/console-sessions` with `message`), and the
model is the one choice offered beside that first message's Send button.
It can be changed later from the session's own pane, next to its
capabilities. The reconciler carries it onto the session's task, the way
it carries the session's capabilities, and like them it reaches the agent
when the session's next run starts -- a run already open keeps the model
it started with (see [director.md](director.md)). Empty
means the team's default model. Like a template's, a session's model
names a model and never a connection: a configured connection's id is
refused.

A session is deleted from its own pane (**Delete session**, under the
chat) or from the **Sessions** list, where each row carries a delete
control of its own; both ask for confirmation first and make the same
`DELETE /api/console-sessions/{id}`, which stops and closes the session's
task.

## In the UI

**Team settings → Models & connections** is one tab for both, and it is
always one team's. The data stays split as described above, so the tab has
two parts: the team's connections first, since a team has no model to choose
until a connection advertises one, and then its choice of model.

The top part is the team's connections. A team with none is told so there,
just under **New connection**, since its tasks wait until it has one. Each
card says which models the connection advertises, that it advertises every
model its provider lists, or "no models — set one" when the provider hasn't
answered yet and nothing is listed. **Test Connection** asks the
provider and shows the models it discovered. **New connection** and **Edit**
open a connection's own page, which has no model or effort picker. It shows:

- **Discovered models**: what the provider lists right now (`GET
  /api/agent-models`). This is read-only and has a **Refresh** button. Under
  it is when grain last stored the provider's list, and, if the provider
  couldn't be reached since, why, and that dispatch is using the last list.
- **Advertised models**: on a discovering connection with no pin, every
  model the provider lists, with nothing to save. Removing one adds it to
  **Excluded** (`excludedModels`), and removing it from there brings it back.
  **Pin to a fixed list instead** turns what is advertised now into a pin.
  On a pinned connection the field is the editable `models` list, which also
  takes names typed by hand (an alias the listing never shows), and
  **Advertise all models** clears the pin. A custom command only has the
  typed list.

The page never writes a connection's `model` or `effort`. Opening one stored
in the old shape folds its `model` into the front of `models`, the same way
the migration does.

Below the connections is the team's choice of model. It writes only
`defaultModel` and `modelSettings` on `PUT /api/teams/{id}`:

- **Default model** is chosen from the models the team's connections
  advertise. A model served only by disabled connections is marked as such,
  and choosing it warns that tasks will wait. While none is chosen, a "Pick a
  default model" prompt names what each connection runs meanwhile. The empty
  choice reads "None — provider default (unset)". If any enabled connection
  has no models, a warning names it.
- Each model lists every connection that serves it, enabled or disabled, with
  its **Reasoning effort**. Antigravity and Codex models offer their
  framework's efforts, a custom command's model takes free text, and a Claude
  model has none. Going back to "Model default" removes the setting.
- A model the team still names (as its default or in a setting) that no
  connection advertises any more is listed with no connections, so the gap
  is visible.
- When no connection advertises a model yet, this part says so and points
  back up at the connections.

Old links to the separate tabs (`/t/<team>/settings/connections` and
`/t/<team>/settings/models`) both open this tab. Nothing else lives on it:
the commit identity
is on **Git identity**, and the memory agent's notes are on the team's
**Memory** page in the left rail.

The per-framework `claudeModel`, `codexModel`, `geminiModel`, `codexEffort`
and `geminiEffort` are no longer offered.

Every **Model** picker (New task, Edit, Bulk edit, templates, schedules
and the team's **Default model**) is searchable. Type any part of a model's
name, with its characters in order (`gem flash` finds `gemini-3.7-flash`),
and the list narrows to the closest matches.

**New task** has a **Model** picker, which sends `model`. It offers the models
the target team's connections advertise, and "" means the team's default.
**Advanced options → Specific connection** sends `agentBackendId`. It offers
only the connections that advertise the chosen model (or the team's default),
and labels disabled ones "disabled, pin only". Changing the model to one the
pinned connection doesn't serve drops the pin, so the form never sends a pair
that `CreateTask` would refuse. There is no framework picker; the form no
longer sends `agentFramework`.

**Edit** in the task pane has the same two pickers, **Model** and **Specific
connection**, over the task's team. Saving sends both `model` and
`agentBackendId` on `PATCH /api/tasks/{id}`. Changing the target team drops a
model or a pin the new team doesn't have. A pin the task already carries stays
selectable even when it doesn't serve the model. This covers a task pinned
before models and connections were separated, so an unrelated edit doesn't
take the pin off.

**Bulk edit** has one **Model** row holding both pickers, offered where the
selection shares a team that has connections. Ticking it sends `model` and
`agentBackendId` together. An edit that sent only the model would leave a
task's existing pin on a connection that may not serve it.

The task pane's details show **Model** and **Connection** as separate rows.
**Model** shows what dispatch will ask for:

- the model the task carries, marked "(from the template)" or "(from the
  schedule)" when one of those chose it. A task records where its model came
  from when it is filed (`task.agent_model_source`, `modelSource` on the task
  API). Editing the model makes it the task's own. Tasks filed before this
  was recorded show the bare model;
- otherwise, for a pinned task, the connection's first pinned model, marked
  "(the connection's own)". For a connection with no pin it is its provider
  default, marked "(provider default (unset))", or "no models — set one";
- otherwise "team default (X)";
- otherwise "Rotation · provider default (unset)", when the team has no
  default.

The empty choice of a task's **Model** picker reads "Team default (X)", or
"Team default (provider default (unset))" when the team has none.

**Connection** appears only for a task pinned to a connection, or whose team
pins one.

## Migration

There is none. The start-time `MigrateModelSelection` that folded a
connection's old `model` and `effort`, a team's per-framework fields and
the deployment's legacy model into this shape is gone (stage 2), with the
fields it read: a store written before the split is not upgraded, which
is the single-tenant reset's own rule ([single-tenant-reset.md](single-tenant-reset.md)).
Dispatch still folds a team's own per-framework fields
(`Team.EffectiveModels`) until stage 3 removes the team settings, and
still reads a task whose `agent_backend_id` names a model rather than a
connection as that model.

## Exported state

The state repository's `tables/` export carries the new columns as they are:
`task.agent_model` and `team.model_settings` (a JSON list of
`{"model": ..., "effort": ...}`). Both default to the empty string, which
means nothing was chosen. An older settings file that does not mention them
therefore resets nothing anyone set on purpose.

Templates and schedules are exported in the authoring format, so their
column appears as `model:` there. A settings file from before the column
has no `model:` key. `grain state check` warns that applying it puts
`agent_model` back on its default `''`, and applying it does exactly that:
the work moves back to the team's default model. That reverts a
preference, not a grant, because a model only runs on connections the
team already has.
