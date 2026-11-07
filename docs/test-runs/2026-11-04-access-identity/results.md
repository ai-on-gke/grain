| ID | Check | Expected | Got | Result |
|---|---|---|---|---|
| 1.1 | alice@example.com initially refused (403) before allowlist add | `^403$` | `403` | PASS |
| 1.2 | initial refusal message points to Settings -> Access | `Settings -> Access` | `principal "alice@example.com" is not on the allowed users and groups list (Settings -> Access)` | PASS |
| 1.3 | grain access allow user:alice@example.com succeeds | `allowed user:alice@example.com` | `allowed user:alice@example.com as grain/operator` | PASS |
| 1.4 | alice@example.com admitted on very next request with no daemon restart | `^200$` | `200` | PASS |
| 1.5 | alice whoami is verified IAP human | `"label":"alice@example.com".*"verified":true.*"allowed":true` | `{"actor":{"kind":"human","id":"alice@example.com","source":"iap","label":"alice@example.com"},"onBehalfOf":null,"source":"iap","verified":true,"allowed":true}` | PASS |
| 1.6 | grain access deny revokes alice@example.com immediately without restart | `^403$` | `403` | PASS |
| 1.7 | post-deny 403 body points to Settings -> Access | `Settings -> Access` | `principal "alice@example.com" is not on the allowed users and groups list (Settings -> Access)` | PASS |
| 1.8 | bob@example.com refused before API allowlist add | `^403$` | `403` | PASS |
| 1.9 | API POST /api/access/principals adds bob@example.com live | `^201$` | `201` | PASS |
| 1.10 | bob@example.com admitted immediately as grain/viewer | `^200$` | `200` | PASS |
| 1.11 | bob at grain/viewer is refused tasks:write (403) | `^403$` | `403` | PASS |
| 1.12 | API PATCH /api/access/principals/{entry} updates bob role live | `^200$` | `200` | PASS |
| 1.13 | bob promoted to grain/contributor can file tasks immediately without restart (201) | `^201$` | `201` | PASS |
| 1.14 | API DELETE /api/access/principals/{entry} removes bob live | `^200$` | `200` | PASS |
| 1.15 | bob refused (403) immediately after API DELETE | `^403$` | `403` | PASS |
| 1.16 | carol@example.com refused before group:ops@example.com is added | `^403$` | `403` | PASS |
| 1.17 | carol@example.com admitted via group:ops@example.com through Cloud Identity stub | `^200$` | `200` | PASS |
| 1.18 | carol receives group role grain/contributor and can file a task (201) | `^201$` | `201` | PASS |
| 1.19 | non-member dave@example.com still refused (403) by Cloud Identity stub | `^403$` | `403` | PASS |
| 1.20 | Cloud Identity stub group lookup returns groups/ops@example.com | `"name": *"groups/ops@example.com"` | `{"name":"groups/ops@example.com"}` | PASS |
| 2.1 | CLI refuses serviceAccount: entry without --reason | `^[1-9]` | `2` | PASS |
| 2.2 | CLI refusal explains reason is required for serviceAccount: | `reason` | `grain: allowed principal "serviceAccount:release-bot@my-proj.iam.gserviceaccount.com": give a reason to admit a service account (with role grain/contributor)` | PASS |
| 2.3 | API refuses serviceAccount: entry without reason (400) | `^400$` | `400` | PASS |
| 2.4 | API refusal explains reason is required | `reason` | `{"error":"allowed principal \"serviceAccount:release-bot@my-proj.iam.gserviceaccount.com\": give a reason to admit a service account (with role grain/contributor)"}` | PASS |
| 2.5 | API admits serviceAccount:release-bot with reason | `^201$` | `201` | PASS |
| 2.6 | admitted service account passes sign-in (200) | `^200$` | `200` | PASS |
| 2.7 | whoami reports kind=service, source=iap, verified=true | `"kind":"service".*"source":"iap".*"verified":true` | `{"actor":{"kind":"service","id":"release-bot@my-proj.iam.gserviceaccount.com","source":"iap","label":"release-bot@my-proj.iam.gserviceaccount.com"},"onBehalfOf":null,"source":"iap"` | PASS |
| 2.8 | service account files task under grain/contributor | `^201$` | `201` | PASS |
| 2.9 | task filed by SA carries authorKind=service and SA email label | `"author":"release-bot@my-proj.iam.gserviceaccount.com".*"authorKind":"service".*"authorLabel":"release-bot@my-proj.iam.gserviceaccount.com"` | `{"id":"4","title":"Automated release check","description":"Filed by IAP service account","author":"release-bot@my-proj.iam.gserviceaccount.com","authorKind":"service","authorSource` | PASS |
| 2.10 | SQLite stores origin_actor_kind=service, source=iap for SA task | `^service\|release-bot@my-proj\.iam\.gserviceaccount\.com\|iap$` | `service\|release-bot@my-proj.iam.gserviceaccount.com\|iap` | PASS |
| 2.11 | API refuses adding gcp-key pool SA to allowlist | `^400$` | `400` | PASS |
| 2.12 | gcp-key pool SA row is present in SQLite allowed_principal | `^1$` | `1` | PASS |
| 2.13 | gcp-key pool SA planted in SQLite is STILL refused (403) at sign-in | `^403$` | `403` | PASS |
| 2.14 | pool SA refusal explains task keys are never admitted | `never admitted|gcp-key|task` | `{"error":"principal \"grain-sb-agent-0@my-proj.iam.gserviceaccount.com\" is a gcp-key agent service account whose key is materialized into task sandboxes and cannot be admitted"}` | PASS |
| 2.15 | -ui-allowed-principals imported user:seed@example.com once and surfaced dropped entries | `"droppedFlagEntries":\["serviceAccount:dropped-ci@my-proj\.iam\.gserviceaccount\.com","domain:example\.com"\]` | `{"entries":[{"entry":"user:seed@example.com","addedBy":"-ui-allowed-principals","addedAt":"2026-11-06T06:22:56.834873552Z","role":"grain/operator"},{"entry":"user:alice@example.com` | PASS |
| 2.16 | daemon log warns about dropped serviceAccount: flag entry | `WARNING: dropped "serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com" from -ui-allowed-principals during migration` | `2026/11/06 06:22:56 grain daemon: host executor: local -- the daemon runs on this machine (no container marker found) 2026/11/06 06:22:56 grain daemon: agent CLI claude: host (none` | PASS |
| 3.0 | enabled externalAgentApi and declared acme/audit-config-only scope set | `^200\|200$` | `200\|200` | PASS |
| 3.1 | agent without api:access:write cannot grant person-only api:access:write (403) | `^403$` | `403` | PASS |
| 3.2 | escalation refusal names api:access:write | `access:write` | `{"error":"api:access:write is a dangerous api grant: only a person may grant it"}` | PASS |
| 3.3 | person can grant api:access:write and api:settings:write on task creation | `^201$` | `201` | PASS |
| 3.4 | api:access:write is refused (403) on sandbox-cli channel | `^403$` | `403` | PASS |
| 3.5 | sandbox-cli refusal names sandbox CLI restriction | `sandbox` | `{"error":"POST /api/access/principals requires scope access:write, which is not permitted on the sandbox-cli channel"}` | PASS |
| 3.6 | api:access:write without X-Grain-Acting-Reason is refused (403) | `^403$` | `403` | PASS |
| 3.7 | missing-reason refusal asks for reason | `reason` | `{"error":"POST /api/access/principals is destructive (scope access:write): give a reason"}` | PASS |
| 3.8 | AU-6.4 refuses agent write to /api/access/principals even with reason (403) | `^403$` | `403` | PASS |
| 3.9 | AU-6.4 refusal states only a person changes the allowed users and groups list | `only a person changes the allowed users and groups list` | `{"error":"agent 6 may not change who signs in to this deployment: only a person changes the allowed users and groups list"}` | PASS |
| 3.10 | agent PUT /api/settings without X-Grain-Acting-Reason refused (403) | `^403$` | `403` | PASS |
| 3.11 | agent PUT /api/settings with X-Grain-Acting-Reason succeeds (200) | `^200$` | `200` | PASS |
| 3.12 | config_audit API records both allowlist reason and agent X-Grain-Acting-Reason | `enable telemetry check from task run.*release pipeline files tasks through IAP|release pipeline files tasks through IAP.*enable telemetry check from task run` | `[{"id":13,"at":"2026-11-06T06:22:59.05088333Z","actor":"6","actorKind":"agent","actorSource":"sandbox","actorLabel":"task 6","surface":"settings","summary":"PUT /api/settings (enab` | PASS |
| 3.13 | SQLite config_audit stores actor_id, actor_source, and summary with reason for allowlist and agent writes | `alice@example\.com\|iap\|.*release pipeline files tasks through IAP` | `flag-migration\|\|imported 1 allowed principal(s) from deprecated -ui-allowed-principals (dropped unsupported: serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com, domain:exa` | PASS |
| 4.1 | alice mints owned external agent token triage-bot | `^201$` | `201` | PASS |
| 4.2 | alice files task via IAP | `^201$` | `201` | PASS |
| 4.3 | owned external agent posts comment on task | `^20[014]$` | `200` | PASS |
| 4.4 | grain get shows Filed by, pause banner, transitions, and all 4 actor labels | `filed by: *alice@example\.com` | `7 Cross-surface attribution walkthrough state:      queued owner:      alice@example.com paused:     by operator (access code) at 2026-11-06T06:22:59Z (was queued) priority:   5 au` | PASS |
| 4.5 | grain get shows operator (access code), external agent triage-bot for alice@example.com, and grain | `operator \(access code\).*external agent triage-bot for alice@example\.com` | `7 Cross-surface attribution walkthrough state:      queued owner:      alice@example.com paused:     by operator (access code) at 2026-11-06T06:22:59Z (was queued) priority:   5 au` | PASS |
| 4.6 | grain get contains zero raw {human, operator} or unlabelled operator | `^0$` | `0` | PASS |
| 4.7 | sandbox CLI grain task read shows Filed by alice@example.com and all 4 labels | `Filed by: alice@example\.com.*operator \(access code\).*grain \(automation\).*external agent triage-bot for alice@example\.com` | `Task 7: Cross-surface attribution walkthrough State: queued Priority: 5 Filed by: alice@example.com Filed at: 2026-11-06T06:22:59Z  Description:   One task touched by IAP person, a` | PASS |
| 4.8 | grain task read contains zero raw {human, operator} | `^0$` | `0` | PASS |
| 4.9 | MCP grain_get_task (summary) shows identical labels in last events | `grain \(automation\): Paused by operator \(access code\).*external agent triage-bot for alice@example\.com` | `Task 7: Cross-surface attribution walkthrough State: queued Priority: 5 Kind: none Last events:   2026-11-06T06:22:59Z comment #7 by grain (automation): Paused by operator (access ` | PASS |
| 4.10 | MCP grain_get_task (conversation) shows all 4 labels and provenance | `alice@example\.com.*operator \(access code\).*grain.*external agent triage-bot for alice@example\.com.*triage-runner/1\.2` | `Task 7 conversation (7 comment(s)):   #3 [2026-11-06T06:22:59Z] alice@example.com (human):   Verified person comment from IAP.   #4 [2026-11-06T06:22:59Z] operator (access code) (h` | PASS |
| 4.11 | MCP grain_get_task (full) carries authorLabel, byLabel, and paused.byLabel | `"authorLabel":"alice@example\.com".*"byLabel":"operator \(access code\)"` | `{"id":"7","title":"Cross-surface attribution walkthrough","description":"One task touched by IAP person, access code, external agent, and grain.","author":"alice@example.com","auth` | PASS |
| 5.1 | settings:read caller (IAP custom role) reads /api/deployment/config-audit (200) | `^200$` | `200` | PASS |
| 5.2 | settings:read caller gets 403 on /api/deployment/audit-logs (needs secrets:read) | `^403$` | `403` | PASS |
| 5.3 | settings:read caller gets 403 on /api/deployment/secret-audit (needs secrets:read) | `^403$` | `403` | PASS |
| 7.1 | GRAIN_USER=dana@example.com sets -as default (X-Forwarded-User: dana@example.com) | `user=dana@example\.com` | `1      queued          -                        user=dana@example.com` | PASS |
| 7.2 | explicit -as flag overrides GRAIN_USER | `user=explicit@example\.com` | `1      queued          -                        user=explicit@example.com` | PASS |
| 7.2b | post-verb -as flag also overrides GRAIN_USER | `user=post-verb@example\.com` | `1      queued          -                        user=post-verb@example.com` | PASS |
| 7.3 | bad GRAIN_TOKEN exits with code 3 | `^3$` | `3` | PASS |
| 7.4 | bad GRAIN_TOKEN error names $GRAIN_TOKEN as its source | `\$GRAIN_TOKEN` | `grain: the grain daemon at http://127.0.0.1:14310 refused this CLI's credentials (grain server: this daemon is single-tenant and every caller shows its access code: sign in with it` | PASS |
| 7.5 | bad GRAIN_AUTH_TOKEN exits with code 3 and names $GRAIN_AUTH_TOKEN | `^3 \|.*\$GRAIN_AUTH_TOKEN` | `3 \| grain: the grain daemon at http://127.0.0.1:14310 refused this CLI's credentials (grain server: this daemon is single-tenant and every caller shows its access code: sign in wi` | PASS |
| 7.6 | bad -token flag exits with code 3 and names -token | `^3 \|.*-token` | `3 \| grain: the grain daemon at http://127.0.0.1:14310 refused this CLI's credentials (grain server: this daemon is single-tenant and every caller shows its access code: sign in wi` | PASS |
| UI.MAIN | Playwright main browser checks and 1440/390 light/dark screenshot suite | `^0$` | `0` | PASS |
| 2.17 | on restart with non-empty DB allowlist, -ui-allowed-principals is ignored | `^403$` | `403` | PASS |
| 2.18 | DB allowlist entries persist across daemon restart | `^200$` | `200` | PASS |
| 6.1 | API returns 503 Service Unavailable when verifier JWKS endpoint is down | `^HTTP/1\.1 503 ` | `HTTP/1.1 503 Service Unavailable` | PASS |
| 6.2 | 503 API response carries Retry-After: 5 header | `Retry-After: 5` | `HTTP/1.1 503 Service Unavailable Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: http: https:; font-src` | PASS |
| 6.3 | 503 API response is sanitized upstream_unavailable JSON with correlation ref | `"code":"upstream_unavailable".*"ref":"[0-9a-f]+"` | `{"error":"an upstream service could not be reached (ref 3d06596616f8)","code":"upstream_unavailable","ref":"3d06596616f8"}` | PASS |
| 6.3b | daemon log records correlation ref and underlying JWKS fetch error (callers receive 503) | `ref=3d06596616f8.*fetching IAP public keys|could not verify X-Goog-IAP-JWT-Assertion yet.*callers receive 503` | `2026/11/06 06:23:22 grain daemon: host executor: local -- the daemon runs on this machine (no container marker found) 2026/11/06 06:23:22 grain daemon: agent CLI claude: host (none` | PASS |
| 6.4 | access code does not bypass 503 when verifier is down | `^503$` | `503` | PASS |
| UI.6 | Playwright verifier-down 503 browser check and screenshot | `^0$` | `0` | PASS |
