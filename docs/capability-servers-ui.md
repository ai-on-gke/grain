# Adding a capability server from the UI

This is the operator's walk through **Settings → Capabilities → Capability
servers** and **Capability policy**: adding a capability server, trying it
before trusting it, configuring it, editing it, deciding who may use it, and
removing it. What a capability server is, and the `mcpServers` shape these
pages edit, is in [capabilities.md](capabilities.md#expose-it-in-grain-grainconfig-mcpservers--settings-ui--cli);
the protocol is [capabilities-spec.md](../design/spec/capabilities/capabilities.spec.md) (§3.1 entries,
§4.2.2 policy, §4.5 configuration).

Everything on these pages is stored in `grain_config` and exported to
`grain/config` as plain text, except secrets: a value typed into a secret
field goes only to the encrypted secrets store, and the entry keeps a
`$secret:<name>` reference to it.

## Reading the page

A **capability** is something a task is given for the length of one attempt:
a short-lived cloud key, a checkout of another repository, an extra set of
tools for its agent. A **capability server** is the program that hands
capabilities out: grain asks it for one as an attempt starts and tells it when
the attempt is over, so it can take it back. A task holds a capability when it,
its template, its schedule or a CI job grants it as
`<capability ID>#<method>`, and the Capability policy allows it.

**Capability servers** is a list, one full-width row per server: its name,
what it gives a task, and four chips, each of which explains itself when
hovered or focused:

- **built-in** or **custom** — part of grain itself, or added here.
- where it runs — **beside grain** (a command grain starts in the daemon's own
  container or machine; run location `container`), **on the host** (a command
  started on the bare machine through grain hostd; run location `host`), or
  **remote (HTTP)** (a server grain reaches at its URL).
- **granted by N** — how many open tasks, templates, schedules and
  Settings-declared workflow jobs grant one of its methods.
- its state — **attached** (connected, methods listed), **pending** (saved,
  connecting within seconds), **needs configuring** (attached but waiting for
  server settings), **failed** (could not connect; the reason is on its
  page), **disabled**, or **not attached** (the daemon behind the page has not
  tried it yet).

A row opens the server's own page at `/settings/capabilities/<capability ID>`,
which can be linked to and survives a reload. The page is in sections, each
with a one-line caption saying what it covers:

- **What it gives a task** — the server's methods, one capability each.
- **Connection** — how grain starts or reaches the server: command or URL, run
  location, environment or headers. **Edit connection** changes it; this is
  the server's registration entry, as exported to `grain/config`. **Bind a
  secret** stores a credential and adds an env entry or header referring to
  it as `$secret:<name>`.
- **Server settings** — options the server defines for itself (accounts,
  scripts, chat spaces). Shown only for a server that declares them; **Edit
  server settings** changes them.
- **Granted by** — what grants this server's methods.
- **Check and manage** — **Test**, **Disable**/**Enable** and **Remove**, each
  with a sentence saying what it does and what happens to tasks that grant
  the server.

Both forms ask before you leave them with unsaved edits, as the rest of
Settings does.

## Built-in servers

grain's own in-tree capability servers (`gcp`, `git`, `model-proxy`,
`self-debug`, `skill`, `host-script`, ...) are listed first on every deployment
without anyone adding them
([capabilities.md](capabilities.md#in-tree-capability-servers-grain-capability-name)).
Each row carries the **built-in** chip; its page is titled `<name> server`,
with its canonical ID, `https://grain.dev/capabilities/<name>`.
- **Edit server settings**, **Test**, **Edit connection** (arguments, env, run
  location) and **Disable** work as for any server. A built-in has no
  **Remove** and its ID cannot be edited: disable one you do not want, and it
  stays disabled across restarts.
- A built-in waiting for its configuration (`gcp` with no permission set,
  `host-script` with no script) shows **needs configuring** and the reason,
  not an error. Fill in its **Server settings**.
- When grain adopted a hand-added `grain capability <name>` entry, the page
  shows a one-time notice naming the old ID. Grants naming that old ID keep
  working through an alias. **Dismiss** clears the notice.
- On the gcp server, **Test** warns when one of its permission sets mints
  from accounts the in-process `gcp-key` also uses
  ([gcp-key-permission-sets.md](gcp-key-permission-sets.md#running-alongside-the-built-in-gcp-key)).

Your own servers follow in the list as **custom** rows.

## 1. Add the server

Select **+ Add server** above the list (it opens **Add a capability server**):

1. **Capability ID** — the server's `capability_id`, a URL
   (`https://github.com/my-org/capabilities/cloud`). Grants name methods as
   `<capability_id>#<method>`, so pick it once; renaming later breaks
   existing grants (Settings warns, below).
2. **Transport**
   - **stdio** — **Command** (a name on `PATH`, a clean absolute path, or a
     path relative to the root of `grain/config`), **Arguments** one per
     line, **Env** as `NAME=VALUE` lines.
   - **HTTP** — **Server URL** and **Headers** as `NAME=VALUE` lines.
   Write a credential as `$secret:<name>`, or bind it after saving with
   **Bind a secret**, which stores the value and writes the reference.
3. **Run location** (stdio only) — **container** runs the command beside
   the daemon. **host** runs it on the bare machine through grain hostd,
   and is offered only when hostd is running beside the daemon and its
   allowlist has a `capabilities` entry for exactly this command. When it
   is not offered, the field says why: no hostd (as on Kubernetes), or no
   matching entry, with the entries it does have.
4. **Test** — before saving, runs `initialize`, `tools/list` and
   `configure` against the unsaved values in a session of its own and saves
   nothing. It shows each method with its description, its argument table
   and whether it is *stateful* (has a release), the server's configuration
   fields, and any error verbatim. A schema keyword grain would not enforce
   is named in that error; such a server cannot be granted until it stops
   using it.
5. **Add server**. Its page opens and shows *pending* until the daemon
   attaches it on its next tick (seconds), then *attached* or *failed* with
   the reason.

## 2. Set its server settings

A server that declares a `configure` tool (§4.5) gets a **Server settings**
section, with an **Edit server settings** button, once grain has seen its
schema. The form is drawn from that schema:
enums are menus, patterns and bounds are checked, and fields the schema
marks `writeOnly` or `format: password` are **secret fields**:

- they are write-only inputs — nothing typed is ever shown again;
- the chip beside each says **set** (the store holds it), **missing** (the
  entry references a secret the store does not hold) or **not set**;
- typing a value replaces the stored one; leaving it empty keeps it.

**Test configuration** tries the form's unsaved values, typed secrets
included, without storing them. **Save server settings** validates the
configuration against the schema first; a refused value is shown on its
field. Secret values are stored under the secret the field already
references, or `capability-server-<hash>/<field>`.

A server without `configure` keeps its settings in env or headers.

## 3. Edit its connection in place

**Edit connection** opens the same fields as Add, filled in. **Save
connection** replaces the entry
in place; a disabled server stays disabled and its configuration is kept.
Changing the **Capability ID** renames the entry in the same write: before
it does, Settings lists everything that still grants the old ID (see
*Usage*) and asks to confirm. Those grants are not rewritten.

**Test**, under **Check and manage**, tries the saved entry as it stands.

## 4. Grant it

Once attached, its methods appear under **Capability server methods**
wherever grants are edited — New task, the task pane, templates, schedules
(see [capabilities.md, Granting from the UI](capabilities.md#granting-from-the-ui)).
`.grain/workflows` jobs name them under `capabilities:`.

## 5. Decide who may use it: Capability policy

The **Capability policy** card edits `grain_config.capability_policy`
(§4.2.2), rule by rule:

- **Effect** — *Allow only*: once any allow rule names a method, only runs
  some allow rule admits may hold it. *Deny*: refuses it to every run the
  rule covers, ahead of any allow rule.
- **Capability** — `<capability_id>#<method>`, where `*` matches any run of
  characters (`https://github.com/my-org/capabilities/cloud#*`).
- **Run kinds** — *Agent tasks*, *Processes*, *CI jobs*; none ticked covers
  every kind.
- **Untrusted CI (forks)** (allow rules) — a run triggered by a pull request
  from a fork or an author without write access holds no server method
  unless an allow rule opts it in here.
- **Events** — CI trigger events (`push`, `pull_request`, `schedule`,
  `release`, `workflow_dispatch`); empty covers every event. Scheduled CI
  runs are the `schedule` event.
- **Repositories** — `owner/name` patterns; empty covers every repository.

A method no allow rule names may be held by any trusted run.

**Save policy** first checks the change against every existing grant — on
open tasks, templates, inline schedules and the jobs of workflows declared
in Settings — and, if any would be newly refused, lists them with the reason
and waits for **Save anyway**. A repository's own `.grain/workflows` files
are not read for this preview; their jobs are judged when they run.

Rules match the method reference, not its `with:` arguments.

## 6. Usage, and removing a server

Each row and page shows **granted by N**, counting grants of any alias the entry carries: the open tasks, templates, schedules and
Settings-declared workflow jobs that grant one of its methods
(`GET /api/capability-servers/usage`). **Remove** (not offered on a built-in) lists them before it
detaches the server; once removed, those grants are refused at admission
until they are changed or the server is added back under the same ID.

## Legacy capabilities

**Settings → Legacy capabilities** is a different thing: the capabilities built
into grain itself before capability servers, each granted by a bare name such
as `gcp-key` or `self-debug` and handed over by grain directly rather than by a
server ([ci-capabilities.md](ci-capabilities.md)). That tab lists them one
full-width row each, with **Ready** or **Not ready** and chips such as **CI
only** and **Default**, each explaining itself as a tooltip. A row opens the
capability's page: what it is short of, its own credentials, and **Test
credential** where it holds a standing one. Under the list, **Deployment-wide
settings** holds the GitHub host and the GCP project and service accounts that
several of them read, and the secrets no capability claims. Which capabilities
every new task starts with is chosen in **Settings → Tasks**, under **Default
capabilities**.

## The API behind these pages

| Page action | Route |
| --- | --- |
| list servers | `GET /api/capability-servers` |
| add, edit, configure, rename (`renameFrom`) | `PUT /api/capability-servers` |
| Test | `POST /api/capability-servers/test` |
| a host script's Test (a server advertising `test_script`) | `POST /api/capability-servers/test-script` |
| a gchat space's usage, and its Send test message (an entry running grain's gchat server: the built-in or a `grain capability gchat` instance; the routes refuse any other id) | `GET /api/capability-servers/gchat-usage?id=`, `POST /api/capability-servers/gchat-test` |
| run location | `GET /api/capability-servers/host-entries` |
| usage | `GET /api/capability-servers/usage` |
| enable, disable, remove, bind a secret, dismiss a built-in's notice, rotate the secrets grain manages (a data-plane token) | `POST /api/capability-servers/{enable,disable,remove,bind-secret,dismiss-notice,rotate-managed}` |
| policy | `GET` / `PUT /api/capability-policy` |
| policy preview | `POST /api/capability-policy/preview` |
