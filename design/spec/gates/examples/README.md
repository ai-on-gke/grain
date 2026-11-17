# Built-in gate documentation examples

Run from the repository root:

```sh
python3 design/spec/gates/validate.py
```

Dependency: Python 3 with `jsonschema` (Debian `python3-jsonschema`). This is a documentation checker, not a deployed gate validator or runtime test suite. It uses local JSON Schema 2020-12 references with date-time checks; no schema network access. `manifest.json` declares each named `$defs` entry, expected schema verdict, expected semantic verdict and denial reason. All nullable fields are explicit. Hashes, task/resource IDs, actors and source revisions are illustrative, not installed authority.

`catalogue-contract.json` demonstrates all twelve gate types; production tasks normally use a smaller relevant set. Each `*-attachment.json` validates one exact typed configuration. `repository-policy.json` shows required merge guards; `evaluation-context.json` shows the internal derived context. `checks-{met,unmet,pending,unknown,error}.json` covers all result statuses. `complete-negative-evidence.json` illustrates a complete empty health/alert set with collection dependencies, not positive proof from an arbitrary empty response. Merge/CL requests, successful/uncertain receipt, scoped completion waiver, action override and acknowledgment activity have separate examples.

`invalid-*` files distinguish structural errors (unknown config/action/type, optional required guard, missing error/outcome, integration guarding its own producer) from shape-valid semantic denials (wrong subject, unauthorized legacy selection, shadow authority, incomplete/dirty/mixed-head evidence, duplicate bindings, absent completion requirements, wrong action target and excessive exception expiry). Semantic fixture checks cover documented admission invariants only. They do not authenticate actors, compute actual artifact/policy/evidence hashes, resolve inherited coverage, prove source completeness, enforce database locks or execute a forge effect. Those remain runtime requirements and [qualification scenarios](../acceptance.md).

The [history folder](../history/README.md) retains the old MCP page/round/action-profile examples with an explicit deferred status. They are not templates for initial built-in admission.
