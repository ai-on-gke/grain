# Operator attribution: recording which person acted

## Problem

Grain records every action a person takes in the UI, the API or the CLI as the deployment's one human actor, `ui.DefaultActor` (`{human, operator}`, shown as "Filed by: operator (human)"). That covers filing, editing and approving tasks, approving proposals and releases, commenting, closing and reopening, settings edits and pressing merge. When several people share a deployment, nobody can tell who did what. `authentication.design.md` makes a verified identity available on each request: an IAP assertion, or another signed-header proxy through its `IdentityVerifier` seam. The Google layer plugs its signed-proxy verifier into that seam and gets `<username>@<domain>`. Its "Attribution" section says that principal should be credited, but not how. This doc says how. The `task_event` table (task 1986) and grain's own closes (task 2032) already record who closed or reopened a task, using today's coarse actor. This design builds on them.

## Proposal

- **Every human-initiated write records the request's verified principal**, for example `{human, alice@example.com, via iap}`, instead of `{human, operator}`. There is one place to read it from (`ui.RequestActor(ctx)`, replacing `Client.actor`), and a route-level test fails for any mutating route that does not use it. The spec lists every write path.
- **The principal comes only from a verified source.** That means the IAP JWT `email`, a verifier registered on `IdentityVerifier` (the signed-proxy verifier in the Google build), or a future per-user API token. Unsigned headers (`X-Goog-Authenticated-User-Email`, `X-Grain-User`, …) and the browser-local `grain.username` are never trusted for attribution. They are logged and ignored, as they are today.
- **Callers nobody named keep the operator actor, marked as such.** Access-code, loopback, in-process and break-glass (`kubectl exec … grain`) requests still record `{human, operator}`, now with a source (`access-code`, `loopback`). They render as "operator (access code)", never as a person.
- **Agents stay agents, and say who they act for.** A session or API grant still records the session or task as the actor (`{agent, <task>}`). When grain knows the person on whose behalf it is acting, it adds them in the existing `OnBehalfOf` slot. Grain decides that from its own records, for example the person who sent the director the message it is answering. The agent can never claim it.
- **The record is a triple, not a new type.** `model.Principal` gains a `Source`. Every `*_kind`/`*_id` column pair gains a `*_source` column, and old rows read back with an empty source.

This identity also feeds `attribution.design.md`. That doc credits *design contributors* on PRs from git identities and review comments, which is a different question from who pressed a button in grain. The two meet in one place: the verified person who files a task becomes its `Requested-by:` trailer and the grainfile `requestor`. The person who approves it becomes its `Approved-by:` trailer. Both trailers are written today, but they always say `operator`.

## UI

The sidebar user tile comes back, fed by `GET /api/whoami` (`{actor, source, allowed}`) rather than by a typed-in name. It shows "alice@example.com" for a verified sign-in, or "operator (access code)". The free-text username dialog stays retired. Everywhere an actor is displayed (Filed by, comments, the timeline and transitions, pipeline changes, the audit views) shows the principal's label. A principal with an on-behalf-of shows both: "director for alice@example.com".

## Non-goals

Authentication itself (`authentication.design.md` and the Google layer's username extraction); authorization and roles, since every admitted person can still do everything; rewriting existing rows.

## Rollout

1. `Principal.Source`, the source columns and `RequestActor`. No visible change: everything is still operator, now marked `access-code`.
2. Once `authentication.design.md` step 2 stops `extractPrincipal` dropping verified humans, the write paths record them, behind the route test.
3. `GET /api/whoami`, the user tile and actor labels in the UI.
4. On-behalf-of for console sessions and API grants. The Google layer registers its signed-proxy verifier, and attribution follows with no change here.

## Open questions

- Should the UI show the short username (`alice`) or the full email? The proposal is the full email everywhere it is stored, and a short form in dense UI only when the domain matches the deployment owner's.
- Should old rows be backfilled? The proposal is no: history keeps saying "operator", because nobody knows who it was.
- Should there be a per-deployment display-name map (`alice@example.com → Alice Liddell`), held in `grain/config`?
- Which human does a director turn act for when several people talk to one session?
