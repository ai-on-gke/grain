# Built-in gate documentation examples

Run from the repository root:

```sh
python3 design/spec/gates/validate.py
```

Dependency: Python 3 with `jsonschema` (Debian `python3-jsonschema`). This is a documentation checker, not a deployed gate validator or runtime test suite. It uses local JSON Schema 2020-12 references with date-time checks; no schema network access. `manifest.json` declares each named `$defs` entry, expected schema verdict, expected semantic verdict and denial reason. All nullable fields are explicit. Hashes, task/resource IDs, actors and source revisions are illustrative, not installed authority.

`catalogue-contract.json` demonstrates all twelve gate types; production tasks normally use a smaller relevant set. Each `*-attachment.json` validates one exact typed configuration. `repository-policy.json` shows required merge guards; `report-template-defaults.json` binds template attachments and lifecycle policy together; `evaluation-context.json` shows the internal derived context. `checks-{met,unmet,pending,unknown,error}.json` covers all result statuses. `complete-negative-evidence.json` illustrates a complete empty health/alert set with collection dependencies, not positive proof from an arbitrary empty response. Merge/CL requests, successful/uncertain receipt, scoped completion waiver, action override and acknowledgment activity have separate examples.

`invalid-*` files distinguish structural errors (unknown config/action/type, optional required guard, missing error/outcome, integration guarding its own producer) from shape-valid semantic denials (wrong subject, unauthorized legacy selection, shadow authority, incomplete/dirty/mixed-head evidence, duplicate bindings, absent completion requirements, wrong action target and excessive exception expiry). Semantic fixture checks cover documented admission invariants only. They do not authenticate actors, compute actual artifact/policy/evidence hashes, resolve inherited coverage, prove source completeness, enforce database locks or execute a forge effect. Those remain runtime requirements and [qualification scenarios](../acceptance.md).

## Operator and internal API assets

The [HTTP/tool specification](../http-api.spec.md) and
[internal registry/evaluator specification](../internal-api.spec.md) complete the
callable interfaces. The [operation index](../operations.json) classifies every
existing/changed/proposed route and exact generated tool, scope/person restrictions,
conditional authority, request/response schema and success status. Shared routes
use the explicit view=gates profile; they are not deployed by these examples.

* [api.schema.json](../api.schema.json): complete operation, controller state,
  evidence facts, error and additive compatibility projection definitions, with
  local references to the canonical contracts schema.
* [api-examples.json](../api-examples.json): a full success and authorization denial
  exchange for each operation. Exchanges are independent snapshots, not one
  chronological run. Request objects are generated-tool arguments; only body is
  sent as the HTTP JSON body.
* [api-context-example.json](../api-context-example.json): the exact aggregate
  context-set hashing payload, including actor, observation scope and source pins.
* [api-evidence-examples.json](../api-evidence-examples.json): all fourteen typed
  evidence kinds; a trustworthy failed child or incomplete pipeline observation
  is valid evidence data without being proof of a met predicate.
* [api-flow-examples.json](../api-flow-examples.json): exact-head override issue,
  executing/uncertain/completed protected operations and approval/queue enrollment
  with no_effect rather than an integration receipt.
* [api-error-examples.json](../api-error-examples.json) and
  [api-invalid-examples.json](../api-invalid-examples.json): status/recovery
  exchanges and structural/semantic rejection cases, including mixed head
  expectations, missing evidence fence, unbound override, unknown coverage,
  remote config schema, wrong fact kind and historical met invalidation.

The single validate.py command now runs [validate_api.py](../validate_api.py) too:
operation-index shape/reference checks, complete per-operation exchanges, typed
evidence/async flows, semantic denials, claimed current route/tool/scope mappings
and local links. There is no schema network access. On Debian with the packaged
dependency, use /usr/bin/python3 design/spec/gates/validate.py if another Python
installation shadows /usr/bin/python3.

The Go snippets were also extracted into a temporary package and type-checked with
Go (only declaration-only Register/APIError.Error signatures receive inert bodies
for that check). Existing shared API generation checks are:
go test ./pkg/ui -run
'Test(AngularRouteTableInSync|RouteScopesAreTotal|GeneratedToolsMatchTheRouteTable|EveryScopedRouteIsDescribed|ToolSchemasTypeBodiesAndQueries)$'.
No runtime route/generated Angular file is changed by this documentation PR.

See the [gap audit, coverage matrix and implementation blockers](../api-readiness.md)
before treating schema coverage as runtime or enforcement readiness.

The [history folder](../history/README.md) retains the old MCP page/round/action-profile examples with an explicit deferred status. They are not templates for initial built-in admission.


The initial read-only rollout may expose optional gate_observations on the ordinary
task read without implementing the full future mutation/enforcement API.
[api-observation-example.json](../api-observation-example.json) defines the complete
observation-only sidecar; [api-observation-task-example.json](../api-observation-task-example.json)
shows the optional full gate-read profile with separate diagnostic results and
empty authoritative/action slots. The fixture's monitor choice is illustrative,
not a selection of the rollout's two gate types. Legacy controls remain
authoritative; no elapsed-time cutover or effect is enabled by these examples.
