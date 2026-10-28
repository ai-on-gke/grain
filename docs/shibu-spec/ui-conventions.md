# Shibu UI conventions, v1

*Standard distribution: the `ui` contract renders these. Kernel: the
endpoints and labels they are built on. The conventions themselves are
names and schemas on kernel objects; the kernel stores them and,
except where this document says so, doesn't interpret them*
([design 2.6](../shibu.md#26-the-ui-and-the-sysui-conventions)).

Agents and components never talk to a particular UI. They publish
standard endpoints and labels, and any UI conforming to
[`ui/v1`](extension-points.md#uiv1) renders them, as the CLI does
([`cli.md`](cli.md)).

## Contents

1. [The list of agents](#the-list-of-agents)
2. [`messages` types](#messages-types)
3. [Artifacts](#artifacts)
4. [Requests](#requests)
5. [Agent-served panes](#agent-served-panes)
6. [The pane bridge](#the-pane-bridge)

---

## The list of agents

A UI lists every agent the person can see or talk to: agents in the
subtree of the namespace it is acting as, and agents whose `messages`
are granted to it. It draws each from the agent's [status
labels](agents.md#status-labels), `title`, `status` and `phase`, and
sorts and filters on `phase`:

```
list(path, selector="kind:agent", include_grants=true)
list(path, selector="kind:agent,phase:waiting-on-human", include_grants=true)
```

and keeps it current with `watch` on the same selector, which delivers
`agent.status`, `object.labeled` and `message.posted` events.

---

## `messages` types

The message shape is in [`agents.md`, `messages`](agents.md#messages).
How a UI renders each type
([design 2.6.2](../shibu.md#262-messages-types)):

| Type | A terminal shows | A rich UI may show | Answered by |
| --- | --- | --- | --- |
| `text` | the text | formatted text (Markdown) | |
| `question` | the text, and `choices` as a numbered list | buttons for the choices, and a free-text box | a `reply` with `choice` or `text`; the agent blocks until then |
| `approval` | the text, the subject, and a prompt for `approve` or `deny` | Approve and Deny buttons, and a reason box | a `reply` with `decision` and `reason` |
| `progress` | the text, and the fraction as a percentage | a progress bar | |
| `attachment` | the name, media type and link | a preview | |
| `reply` | the answer, under what it answers | the same, threaded | |

- `messages` is an `http` endpoint: a UI reads it with `GET
  <agent>/messages` and answers with `POST <agent>/messages`, and
  answering needs `write` (or `POST`) on it. A UI shows the answer
  controls only if the person has it.
- An `attachment` naming a volume or an endpoint is a reference. A UI
  fetches it through the web proxy as the viewer, and shows it as
  unavailable if the viewer can't reach it.
- **A UI never asks for a credential in a chat**, whatever a message
  says. A request for a secret is answered in the UI's own form
  ([Requests](#requests)).

---

## Artifacts

An agent's `artifacts` endpoint lists what it produced
([`agents.md`, `artifacts`](agents.md#artifacts),
[design 2.6.4](../shibu.md#264-artifacts)). A UI shows them beside the
agent's chat, by kind: a pull request with its state (from
`/sys/forge` if the viewer can reach it), a link, a file it can
preview, or a volume.

**Any namespace may serve one.** A service or component that wants its
outputs shown the same way serves an `http` endpoint named `artifacts`
in its namespace, answering `GET artifacts?since=<seq>` with the same
shape:

```json
{"artifacts": [{"id": "…", "seq": 1, "at": "…", "artifact_kind": "url",
                "title": "Preview of change 17", "ref": "https://shibu.example.com/factory/rel/preview-17/web-public"}],
 "next_since": 1}
```

---

## Requests

Requests for quota, a grant or a secret stay in the API
([design 2.6.5](../shibu.md#265-requests-stay-in-the-api),
[F14](../shibu.md#f14-inbox-requests-need-structure)). There are two
ways to make one:

- an `approval` message to the agent's creator, on its `messages`; or
- a call to an endpoint the party that can grant exposes, by
  convention named **`requests`**.

**The `requests` convention.** Nothing in the kernel depends on it. A
namespace that takes requests serves an `http` endpoint named
`requests`:

| Method and path | Request → response |
| --- | --- |
| `POST requests` | `{"want": {"object": "/factory/svc/db", "ops": ["tunnel"], "limits": {...}} \| {"secret": "anthropic", "adapter": "claude-code"}, "for": "/factory/lead/workers/w17", "lease": "8h", "why": "…"}` → `{"id": "31", "state": "open"}` |
| `GET requests?state=open` | the requests the caller may see |
| `GET requests/{id}` | one request, with its `answer` once answered |
| `POST requests/{id}/answer` | `{"decision": "grant", "grant_id": "gr_…"}` or `{"decision": "deny", "reason": "…"}` |

The requester is the caller's namespace, from the caller assertion.

**Answering.** A UI only displays requests, and answers them as its
principal. An answer is a `grant` (possibly smaller, or with a lease
or a limit the request didn't ask for), or a `deny` with a reason. A UI
can approve nothing the namespace it acts as couldn't grant through the
API. `shibu approve <ns>/requests/<id>` does the same from the CLI: it
calls `grant` and then posts the answer.

**A request for a secret** is answered in the UI's own
manifest-driven form, never in chat or in a pane. The person stores
the value with `put_secret` (or `shibu secret put`), then grants
`read` on it to whatever will use it for the agent (its runner,
`/sys/model` or `/sys/cred`), leased and with `regrant: false`, and
answers with the secret's path, a reference. The agent never sees the
value, and holds no grant on it.

---

## Agent-served panes

Any agent or component can add a pane to the UI by serving it from its
own endpoints ([design 2.6.6](../shibu.md#266-agent-served-panes)).

**Declaration.** A namespace lists its panes at `<ns>/ui/panes`:

```
GET <ns>/ui/panes →
{"panes": [
  {"id": "pipeline", "title": "Pipeline status", "path": "<ns>/ui/pipeline",
   "icon": "chart", "placement": "tab"}
]}
```

| Field | Meaning |
| --- | --- |
| `id` | Matches `[a-z0-9-]{1,63}`. |
| `path` | An `http` endpoint in the namespace's own folder, by convention `<ns>/ui/<id>`. |
| `icon` | A name from the UI's icon set. A UI that doesn't know it uses a default. |
| `placement` | A hint: `tab` (a tab on the agent's page), `sidebar` (beside its chat) or `full` (the whole content area, still inside the UI's frame). |

**Visibility is a grant.** The UI fetches the declaration and the pane
through the web proxy as the viewer. A person sees a pane only if they
can reach its endpoint: by the tree, as the agent's creator or an
ancestor, or by a grant:

```
grant(<ns>/ui/<pane>, to=/users/bob, ops=[read])
grant(<ns>/ui/panes,  to=/users/bob, ops=[read])   # so the viewer's UI has the title
```

A UI finds panes in each visible agent's `ui/panes`, and in the
viewer's handles on `*/ui/*`. A pane whose declaration the viewer can't
read is shown under its id.

**Isolation (required).** Pane content is untrusted, whoever serves it.
A conforming UI must:

- render every pane in an iframe on the pane origin
  `https://<ns-id>.panes.<shibu>/<id>/`, served by the web proxy
  ([`web-proxy.md`, Pane origins](web-proxy.md#pane-origins));
- sandbox the iframe with `allow-scripts allow-forms allow-same-origin`
  and nothing that lets it navigate the top window or open popups that
  escape the sandbox (no `allow-top-navigation`,
  `allow-popups-to-escape-sandbox` or `allow-modals`);
- draw its own frame around the iframe, outside it, naming the
  namespace and agent serving the pane. A pane can't draw over it, and
  `full` keeps it;
- never place a credential field in or over a pane;
- place a pane in main navigation only if its namespace is in the
  `/sys` spec's `ui.nav`. Untrusted panes appear only under their own
  agent or namespace. Being trusted changes nothing about isolation.

**CLI fallback.** `shibu open <ns>/<pane>` opens the pane origin's URL
in a browser, as a top-level page, with no bridge. A UI that doesn't
support panes lists them as links that do the same.

---

## The pane bridge

A pane talks to the UI only through `window.parent.postMessage`. The UI
accepts a message only if its `source` is the pane's own iframe and its
`origin` is the pane's origin, and ignores anything that isn't one of
these:

```json
{"shibu": "pane/v1", "type": "resize", "height": 640}
{"shibu": "pane/v1", "type": "navigate", "to": "/factory/lead/workers/w17"}
{"shibu": "pane/v1", "type": "navigate", "to": "/factory/lead/ui/pipeline"}
{"shibu": "pane/v1", "type": "open-messages"}
{"shibu": "pane/v1", "type": "post", "message": {"type": "approval", "text": "Ship release 42?",
  "subject": {"summary": "release 42"}}}
```

| `type` | What the UI does |
| --- | --- |
| `resize` | Resizes the pane, within limits the UI sets. |
| `navigate` | Goes to another agent or pane, by Shibu path. Never to a URL. |
| `open-messages` | Opens the serving agent's `messages`. |
| `post` | Shows the viewer the message exactly as it will be sent, to the serving agent's `messages`. If the viewer confirms, the UI sends it **as the viewer**, with `POST <agent>/messages`, which needs the viewer's own `write` on it. |

The UI may answer with
`{"shibu": "pane/v1", "type": "posted", "message_id": "msg_…"}` or
`{"shibu": "pane/v1", "type": "error", "code": "permission_denied"}`.
It sends nothing else into a pane: no identity, session, or data the
pane couldn't fetch itself.
