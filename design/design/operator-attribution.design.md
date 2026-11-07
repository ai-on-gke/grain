# Operator attribution: recording which person acted

**Owner:** @bwsalmon

**Specified by:** [`operator-attribution.spec.md`](../spec/operator-attribution/operator-attribution.spec.md)

**The core rule:** Every mutating UI and API handler reads the acting `model.Principal` from `ui.RequestActor(ctx)`, crediting a named person or service account only when an `IdentityVerifier` verified the request ([`authentication.design.md`](authentication.design.md)) and marking access-code or loopback actions explicitly as `operator (access code)` or `operator (loopback)`.

## Problem

Without per-caller identity, grain records every action a person takes in the UI, the API or the CLI as the deployment's one human actor, `ui.DefaultActor` (`{human, operator}`, shown as "Filed by: operator (human)"). That covers filing, editing and approving tasks, approving proposals and releases, commenting, closing and reopening, settings edits and pressing merge. When several people share a deployment, nobody can tell who did what. [`authentication.design.md`](authentication.design.md) ([`authentication.spec.md`](../spec/authentication/authentication.spec.md)) makes a verified identity (`ui.VerifiedIdentity`, mapped to `model.PrincipalHuman` or `model.PrincipalService`) available on each request: an IAP assertion (`NewIAPVerifier`), or another signed-header proxy registered through `ui.RegisterIdentityVerifier` (`IdentityVerifier`). A wrapper distribution plugs its signed-proxy verifier into that seam and gets `<username>@<domain>`. [`authentication.design.md`](authentication.design.md) §2 establishes how the request's principal is authenticated; this doc specifies how grain records and renders it across mutations. The `task_event` table (task 1986) and grain's own closes (task 2032) already record who closed or reopened a task. This design builds on them.

## Proposal

- **Every human- or service-initiated write records the request's verified principal**, for example `{human, alice@example.com, iap}` (`ui.PrincipalLabel`: `alice@example.com (via iap)`), instead of `{human, operator}`. There is one place to read it from (`ui.RequestActor(ctx)` in `pkg/ui/request_actor.go`, replacing `Client.actor`), and a route-level test (`TestEveryMutatingRouteIsAttributed`) fails for any mutating route that does not use it. [`operator-attribution.spec.md`](../spec/operator-attribution/operator-attribution.spec.md) lists every write path.
- **The principal comes only from a verified source.** That means the IAP JWT `email`, a verifier registered on `ui.IdentityVerifier` (such as a wrapper distribution's signed-proxy verifier or the `registry` verifier in [`registry.design.md`](registry.design.md)), or a future per-user API token (`Principal.Verified() == true`). Unsigned headers (`PlaintextIdentityHeaders`: `X-Goog-Authenticated-User-Email`, `X-Grain-User`, …) and the retired browser-local `grain.username` are never trusted for attribution (`{#AU-20}` in [`authentication.spec.md`](../spec/authentication/authentication.spec.md)). They are logged and ignored.
- **Callers nobody named keep the operator actor, marked as such.** Access-code, loopback, in-process and break-glass (`kubectl exec … grain`) requests still record `{human, operator}`, now with a `Principal.Source` (`access-code`, `loopback`, `in-process`). They render via `ui.PrincipalLabel` as `operator (access code)` or `operator (loopback)`, never as a named person.
- **Agents stay agents, and say who they act for.** A session or API grant still records the session or task as the actor (`{agent, <task>}`, `{agent, external:<name>, external-token}`). When grain knows the person on whose behalf it is acting, it adds them in the existing `OnBehalfOf` slot. Grain decides that from its own records, for example the person who sent the director the message it is answering. The agent can never claim it.
- **The record is a triple, not a new type.** `model.Principal` carries `Source` (`model.AuthSource`). Every `*_kind`/`*_id` column pair gains a `*_source` column, and old rows read back with an empty source.

This identity also feeds [`attribution.design.md`](attribution.design.md). That doc credits *design contributors* on PRs from git identities and review comments, which is a different question from who pressed a button in grain. The two meet in one place: the verified person who files a task becomes its `Requested-by:` trailer and the grainfile `requestor`. The person who approves it becomes its `Approved-by:` trailer.

## UI

Today `GET /api/config` and `GET /api/auth/user` (`AuthUserResponse` in `pkg/ui/auth.go`) surface `authMethod` and `user`/`actor` so the desktop header (`PageHeader.jsx`), the phone navigation drawer (`Sidebar.jsx`), and `Settings → Access` (`AccessSection.jsx`) display the verified email and `Log out` ([`authentication.design.md`](authentication.design.md) §2). A dedicated `GET /api/whoami` endpoint (`{actor, source, allowed}`) is planned to feed richer actor badges without a typed-in name, while the free-text username dialog stays retired. Everywhere an actor is displayed (Filed by, comments, the timeline and transitions, pipeline changes, the audit views) shows the principal's label (`ui.PrincipalLabel`). A principal with an `OnBehalfOf` shows both: `"director for alice@example.com"`.

## Non-goals

Authentication and admission themselves ([`authentication.design.md`](authentication.design.md), [`authentication.spec.md`](../spec/authentication/authentication.spec.md), and a wrapper distribution's username extraction); authorization and roles, which belong to `rbac.design.md` (task 2764; see [`authentication.design.md`](authentication.design.md) §3 for today's single-tenant operator equivalence and dangerous-scope minting guard); rewriting existing rows.

## Rollout

1. **Done** (tasks 2310, 2311): `model.Principal.Source` (`model.AuthSource`), `Principal.Verified()`, `ui.PrincipalLabel`, the `*_source` database columns, `ui.RequestActor(ctx)`, `TestNothingOutsideRequestActorReadsTheActor`, and `TestEveryMutatingRouteIsAttributed`.
2. **Done** (task 2309): `extractPrincipal` (`pkg/ui/principal.go`) preserves verified `PrincipalHuman` and `PrincipalService` callers from `IdentityVerifier` implementations ([`authentication.design.md`](authentication.design.md) §2), and mutating routes record them.
3. **Done** (task 2312): `GET /api/whoami` (`OA-5`) and full `PrincipalLabel` rendering across remaining UI surfaces.
4. **Done** (task 2313): `OnBehalfOf` population for console sessions and API grants. The agent API gate derives it server-side (`agentOnBehalfOf`, `pkg/ui/on_behalf_of.go`) and `RequestActor` returns it, so every write the gate admits carries it. A task view names a session's agent by its title ("director for alice@example.com"). A wrapper distribution registers its signed-proxy `IdentityVerifier`, and attribution follows with no change here.
5. **Done** (task 2797): an external agent token's on-behalf-of (`ExternalAgentToken.OwnerPrincipal`, spec §2.4).

## Open questions

- Should the UI show the short username (`alice`) or the full email? The proposal is the full email everywhere it is stored, and a short form in dense UI only when the domain matches the deployment owner's.
- Should old rows be backfilled? The proposal is no: history keeps saying "operator", because nobody knows who it was.
- Should there be a per-deployment display-name map (`alice@example.com → Alice Liddell`), held in `grain/config`?
- ~~Which human does a director turn act for when several people talk to one session?~~ Answered by spec §2.4: the author of the most recent person's message the turn has been shown (`Observation.DeliveredCommentID`, or what was in the conversation when its run started), and nobody when that author is not verified.
