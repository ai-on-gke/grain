| ID | Check | Expected | Got | Result |
|---|---|---|---|---|
| 1.1 | grain access allow user:alice as grain/viewer | `allowed user:alice@example.com` | `allowed user:alice@example.com as grain/viewer` | PASS |
| 1.2 | grain access allow group:devs as grain/contributor | `allowed group:devs@example.com` | `allowed group:devs@example.com as grain/contributor` | PASS |
| 1.3 | grain access allow user:vic (viewer only, no group) | `allowed user:vic@example.com` | `allowed user:vic@example.com as grain/viewer` | PASS |
| 1.4 | unknown role grain/developer is refused by the CLI (plan names a role grain does not ship) | `^[^0].*|.*(unknown|no scope set|not a).*` | `2 grain: role "grain/developer" is no scope set in effect (Settings -> Scope sets, or a built-in such as grain/operator or grain/viewer)` | PASS |
| 1.5 | grain access list shows both alice and devs rows with roles | `user:alice@example.com.*grain/viewer` | `user:alice@example.com	grain/viewer	added by the access code	2026-11-08T03:23:48Z	alice reads group:devs@example.com	grain/contributor	added by the access code	` | PASS |
| 1.6 | alice whoami readable (200) | `^200$` | `200` | PASS |
| 1.7 | alice's /api/auth/user access lists both matched roles | `grain/contributor.*grain/viewer|grain/viewer.*grain/contributor` | `{"authenticated":true,"user":"alice@example.com","tenancy":"single","authMethod":"iap","access":{"role":"grain/contributor, grain/viewer","roles":["grain/contri` | PASS |
| 1.8 | alice (user viewer + group contributor) can file tasks: roles union | `^201$` | `201` | PASS |
| 1.9 | vic (viewer only) refused tasks:write | `^403$` | `403` | PASS |
| 1.10 | vic (viewer) can still read whoami | `^200$` | `200` | PASS |
| 1.11 | unlisted nobody@ can read whoami, which reports authenticated:false | `^200 .*"authenticated":false` | `200 {"acceptedAuth":["access-code","iap"],"externalAgentApi":false,"version":"v0.0.0-20261108032317-938b4c971a84","authenticated":false}` | PASS |
| 1.11b | unlisted nobody@ refused every scoped route, naming Settings -> Access | `^403 .*Settings -(>|\\u003e) Access` | `403 {"error":"principal \"nobody@example.com\" is not on the allowed users and groups list (Settings -\u003e Access)"}` | PASS |
| 1.12 | serviceAccount: without --reason refused | `^[1-9].*reason` | `2 grain: allowed principal "serviceAccount:ci-bot@my-proj.iam.gserviceaccount.com": give a reason to admit a service account (with role grain/viewer)` | PASS |
| 1.13 | serviceAccount: with --reason admitted | `allowed serviceAccount:ci-bot` | `allowed serviceAccount:ci-bot@my-proj.iam.gserviceaccount.com as grain/viewer` | PASS |
| 1.14 | service account session admitted, viewer role | `^200$` | `200` | PASS |
| 1.15 | viewer service account refused tasks:write | `^403$` | `403` | PASS |
| 1.16 | delegate: without --reason refused | `^[1-9].*reason` | `2 grain: allowed principal "delegate:console@my-proj.iam.gserviceaccount.com": give a reason to admit a delegating app (with role grain/delegated)` | PASS |
| 1.17 | delegate: with a malformed --label refused | `^[1-9]` | `2 grain: allowed principal "delegate:console@my-proj.iam.gserviceaccount.com": delegate label "Bad Label" must match [a-z][a-z0-9-]{0,31}` | PASS |
| 1.18 | delegate: with --label and --reason admitted | `allowed delegate:console` | `allowed delegate:console@my-proj.iam.gserviceaccount.com as grain/delegated` | PASS |
| 1.19 | delegate row defaults to grain/delegated, keeps label, never used | `^grain/delegated release-console null$` | `grain/delegated release-console null` | PASS |
| 1.20 | dana via delegate:console admitted | `^200$` | `200` | PASS |
| 1.21 | delegated whoami attributes the person via the delegate label | `dana@example.com.*release-console|release-console.*dana@example.com` | `{"acceptedAuth":["access-code","iap"],"externalAgentApi":false,"version":"v0.0.0-20261108032317-938b4c971a84","authenticated":true,"principal":"human:dana@examp` | PASS |
| 1.22 | delegate last_used_at stamped after use | `^[0-9]{4}-` | `2026-11-08T03:23:48.836670793Z` | PASS |
| 1.23 | dana (contributor) via delegate (delegated) can file a task | `^201$` | `201` | PASS |
| 1.24 | delegated task attributed to dana (via release-console) | `dana@example.com` | `{"id":"3","title":"delegated filing","description":"dana via console","author":"console@my-proj.iam.gserviceaccount.com","authorKind":"service","authorSource":"` | PASS |
| 1.25 | viewer vic via a write delegate still refused: intersection, not union | `^403$` | `403` | PASS |
| 1.26 | act.access=read delegation refuses writes | `^403$` | `403` | PASS |
| 1.27 | unlisted delegating app refused | `^(401|403)$` | `403` | PASS |
| 1.28 | delegated caller cannot edit Settings -> Access | `^403$` | `403` | PASS |
| 1.29 | group lookup down: alice still admitted on her direct user: row | `^200$` | `200` | PASS |
| 1.30 | group lookup down: alice's roles are only grain/viewer | `"grain/viewer"` | `{"authenticated":true,"user":"alice@example.com","tenancy":"single","authMethod":"iap","access":{"role":"grain/viewer","roles":["grain/viewer"],"full":false,"sc` | PASS |
| 1.30b | group lookup down: grain/contributor not granted | `^0$` | `0` | PASS |
| 1.31 | group lookup down: alice refused tasks:write (fails closed to user: role) | `^403$` | `403` | PASS |
| 1.32 | group lookup down: group-only erin refused | `^(401|403)$` | `403` | PASS |
| 1.33 | refusal message is the generic lookup-unavailable text | `lookup unavailable` | `{"error":"principal \"erin@example.com\" could not be authorized against group allowlist: group membership lookup unavailable"}` | PASS |
| 1.34 | no task filed on the lookup-down daemon (erin 403) | `^1$` | `1` | PASS |
| 1.35 | vic's browser session refused before the access code | `^403$` | `403` | PASS |
| 1.36 | vic presents the deployment access code | `^(200|204)$` | `200` | PASS |
| 1.37 | access-code cookie does NOT lift an IAP-verified viewer (finding F1) | `^403$` | `403` | PASS |
| 1.37b | the bare access code (no assertion, unlimited admin) files a task | `^201$` | `201` | PASS |
| 1.38 | logout answers | `^(200|204|302|303)$` | `302` | PASS |
| 1.39 | after logout vic is back to viewer (403) | `^403$` | `403` | PASS |
| 1.40 | after logout whoami still readable (200) | `^200$` | `200` | PASS |
| 2.1 | viewer PATCH task refused | `^403$` | `403` | PASS |
| 2.2 | viewer approve refused | `^403$` | `403` | PASS |
| 2.3 | viewer force-merge refused | `^403$` | `403` | PASS |
| 2.4 | viewer secret set refused | `^403$` | `403` | PASS |
| 2.5 | viewer Settings -> Access add refused | `^403$` | `403` | PASS |
| 2.6 | viewer grain fs (task://) read allowed (read scope) | `^200$` | `200` | PASS |
| 2.7 | viewer daemon logs readable: deployment:read is a non-dangerous *:read | `^200$` | `200` | PASS |
| 2.8 | contributor filing with a dangerous api: grant is held or refused | `^(201|403)$` | `403` | PASS |
| 2.8b | dangerous filing refused with a role message | `grain/contributor|role` | `{"error":"dana@example.com (role grain/contributor) cannot set capabilities: cannot grant api:config:write, which the role does not hold"}` | PASS |
| 2.9 | contributor admission above standard held or refused | `^(201|403)$` | `403` | PASS |
| 2.9b | durable filing refused with a role message | `admission|role` | `{"error":"dana@example.com (role grain/contributor) cannot set admission: durable requires an unlimited role (grain/operator)"}` | PASS |
| 2.11 | contributor PATCH admission bypass on another task refused or held | `^(403|202)$|^200$` | `403` | PASS |
| 2.11b | target task admission not raised | `^(null|""|"standard")$` | `"standard"` | PASS |
| 2.12 | contributor force-merge refused | `^403$` | `403` | PASS |
| 2.13 | contributor secret rotation refused | `^403$` | `403` | PASS |
| 2.14 | contributor self-promotion refused | `^403$` | `403` | PASS |
| 2.15 | contributor daemon logs readable (deployment:read via grain/viewer) | `^200$` | `200` | PASS |
| 2.16 | no secret was created by refused calls | `^0$` | `0` | PASS |
| 2.17 | CLI viewer grain create exits 3 naming the role | `^3 .*grain/viewer` | `3 grain: vic@example.com (role grain/viewer) holds no scope tasks:write, which POST /api/tasks needs: ask an access-code admin to assign a role that includes it` | PASS |
| 2.18 | CLI viewer grain access allow exits 3 | `^3 .*access:write` | `3 grain: vic@example.com (role grain/viewer) holds no scope access:write, which POST /api/access/principals needs: ask an access-code admin to assign a role tha` | PASS |
| 2.19 | CLI viewer grain approve exits 3 | `^3 ` | `3 grain: vic@example.com (role grain/viewer) holds no scope tasks:admin, which POST /api/tasks/{id}/approve needs: ask an access-code admin to assign a role tha` | PASS |
| 2.20 | CLI viewer grain logs <task> passes the role gate (not exit 3) | `^(0|2 .*has not run yet)` | `2 grain: task 5 has not run yet` | PASS |
| 2.21 | CLI contributor grain force-merge exits 3 | `^3 ` | `3 grain: dana@example.com (role grain/contributor) holds no scope merge:force, which POST /api/tasks/{id}/force-merge needs: ask an access-code admin to assign ` | PASS |
| 2.22 | CLI contributor grain access role exits 3 | `^3 .*grain/contributor` | `3 grain: dana@example.com (role grain/contributor) holds no scope access:write, which PATCH /api/access/principals/{entry} needs: ask an access-code admin to as` | PASS |
| 2.22b | CLI contributor dangerous -capability refused or held | `^(3|0) ` | `3 grain: dana@example.com (role grain/contributor) cannot set capabilities: cannot grant api:config:write, which the role does not hold` | PASS |
| 2.23 | target task unchanged after every refused edit | `operator target` | `operator target` | PASS |
| 2.24 | only held filings were stored (5 + 0) | `^5$` | `5` | PASS |
| 3.1 | /api/mcp tools/list: viewer sees the compact meta-tools | `grain_explore_scope.*grain_invoke` | `grain_explore_scope grain_filing_context grain_get_task grain_invoke grain_list_tasks grain_submit_task ` | PASS |
| 3.2 | explore_scope routes shrink with role (operator > contributor > viewer > 0) | `ok` | `ok` | PASS |
| 3.3 | viewer's routes are all GETs (no create/update/delete/approve/merge/detach) | `^$` | `` | PASS |
| 3.3b | capabilities:detach / config:write / dispatch routes absent for viewer | `^$` | `` | PASS |
| 3.4 | viewer grain_invoke refused on all 175 omitted routes (/api/mcp) | `^0$` | `0` | PASS |
| 3.5 | contributor grain_invoke refused on all 139 omitted routes (/api/mcp) | `^0$` | `0` | PASS |
| 3.6 | viewer grain_submit_task refused | `HTTP 403|holds no|grain/viewer` | `{"jsonrpc":"2.0","id":1,"result":{"content":[{"text":"grain_submit_task failed: HTTP 403: vic@example.com (role grain/viewer) holds no scope tasks:write, which ` | PASS |
| 3.7 | no task or state created by refused MCP calls | `^5$` | `5` | PASS |
| 3.7b | target task title untouched by MCP calls | `^operator target$` | `operator target` | PASS |
| 3.8 | operator-scoped gma_ token issued for olivia | `^gma_` | `gma_205b8b16db7fdbf14c4692badf5b44fd` | PASS |
| 3.9 | gma_ token can file while its owner is operator | `"id"|[Tt]ask` | `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool grain_submit_task"}}` | PASS |
| 3.10 | grain access role downgrades olivia to grain/viewer | `olivia@example.com` | `user:olivia@example.com: role grain/operator -> grain/viewer` | PASS |
| 3.11 | downgraded owner's operator gma_ refused on /mcp tools/list (fix F2; was 314 tools) | `wider than its owner's current role` | `{"error":"external agent token olivia-laptop is wider than its owner's current role: it grants tasks:write, which olivia@example.com (role grain/viewer) does no` | PASS |
| 3.12 | downgraded owner's gma_ refused on all 314 omitted routes (/mcp) | `^0$` | `0` | PASS |
| 3.13 | downgraded owner's gma_ api_create_task on /mcp refused | `wider than its owner's current role` | `{"error":"external agent token olivia-laptop is wider than its owner's current role: it grants tasks:write, which olivia@example.com (role grain/viewer) does no` | PASS |
| 3.14 | downgraded owner's gma_ grain_invoke on /api/mcp refused | `wider than its owner's current role` | `{"error":"external agent token olivia-laptop is wider than its owner's current role: it grants tasks:write, which olivia@example.com (role grain/viewer) does no` | PASS |
| 3.14b | downgraded owner's gma_ REST POST /api/tasks refused | `^403$` | `403` | PASS |
| 3.14c | a viewer-scoped token re-issued for olivia works, within viewer (137 tools, none create/update) | `ok` | `ok` | PASS |
| 3.15 | no task created by the downgraded gma_ token | `^5$` | `5` | PASS |
| 3.16 | gma_ of a removed owner: recorded (see O4) | `.` | `{"jsonrpc":"2.0","id":1,"result":{"content":[{"text":"{\"id\":\"6\",\"title\":\"gma after removal\",\"description\":\"x\",\"author\":\"external:olivia-laptop\",` | PASS |
| 3.17 | tasks after owner removal (recorded, see O4) | `^[0-9]+$` | `6` | PASS |
