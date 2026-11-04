# Run record: Cloud Run direct IAP qualification on `main` with SA-A on the database allowlist, 2026-11-04

Task 2970 ran this with the unmodified `scripts/iap-e2e/run.sh` (no `--image`, so the script's default, `ghcr.io/bwsalmon/grain/grain:main`), as `grain-049f1e09-iap-test-1@bsalmon-sandbox.iam.gserviceaccount.com`, which holds the gcp-key permission set `iap-test` (`roles/editor`, `roles/iap.admin` and `roles/run.admin`). It reruns `2026-11-04-iap-e2e-cloud-run-database-allowlist.md` against a published image instead of task 2942's branch binary. The machine-readable report is `2026-11-04-iap-e2e-cloud-run-main.json`.

Notes on this run:

- **What was deployed.** `ghcr.io/bwsalmon/grain/grain:main` was the index `sha256:a3fadb4dc305ccc8f4953beeb69f983f18ca868e5b970f388ae322829413ff10`. Cloud Run's audit log shows it deployed the `linux/amd64` manifest `ghcr.io/bwsalmon/grain/grain@sha256:fc2083043eb7cfb570806229b4af98ba369a2c238ebc0fb9fbde6c32d8465f72` at all three deploys. That manifest's `org.opencontainers.image.revision` is `1e33e823` (`v3.2.0-dev` from `main`), which includes task 2942's merge `6797020f` (PR #652). The run waited for this image: until about 07:45Z, `:main` still pointed at images built from `dc96d10d` and then `6f52ca9d`, both older than that merge. Nothing was swapped into the container: the daemon, and the `grain access allow` it ran, were the image's own binary. The CLI used in case 1 was built from the same commit, `1e33e823`.
- **How SA-A got on the list.** At each of the three deploys (revisions `grain-iap-e2e-00001-68x`, `-00002-p5c` and `-00003-wvz`), the Cloud Run logs show the one-time migration dropping the `-ui-allowed-principals` entry (`dropped "serviceAccount:grain-iap-e2e-sa-a@..." from -ui-allowed-principals during migration`). Under a second later, the container's `grain access allow serviceAccount:grain-iap-e2e-sa-a@... --reason "iap e2e"` added it over loopback with the access code (`grain access: operator (access code) (access-code) allowed serviceAccount:grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com (iap e2e)`). So SA-A was admitted through the database allowlist, not through the deprecated `-ui-allowed-principals` flag. Setup did not hit the "grain refused SA-A" blocker.
- **Attribution.** Case 8 recorded SA-A's task, comment and close as principal kind `service`, not `operator`. Case 1's `/api/whoami` reported `kind=service`, `source=iap`.
- **Teardown.** Afterwards, `gcloud run services list` and `gcloud iam service-accounts list` showed no `grain-iap-e2e*` resources left in `bsalmon-sandbox`.

## Cloud Run Direct IAP End-to-End Qualification Report

- **Result:** **PASS** (8/8 passed)
- **Mode:** `cloud-run-iap`
- **Image:** `ghcr.io/bwsalmon/grain/grain:main`
- **Project / Region / Service:** `bsalmon-sandbox` (`112895876707`) / `us-central1` / `grain-iap-e2e`
- **Service URL:** `https://grain-iap-e2e-v3tesd4k5q-uc.a.run.app`
- **IAP Assertion Audience (`-ui-iap-audience`):** `/projects/112895876707/locations/us-central1/services/grain-iap-e2e`
- **Programmatic Caller JWT Audience:** `https://grain-iap-e2e-v3tesd4k5q-uc.a.run.app/*` (RS256 service-account JWT in `Authorization: Bearer`)
- **Identities:** `SA-A` = `grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com` (allowed), `SA-B` = `grain-iap-e2e-sa-b@bsalmon-sandbox.iam.gserviceaccount.com` (not on allowlist)
- **Window:** `2026-11-04T07:45:51Z` -> `2026-11-04T07:48:59Z` (`188s`)
- **Estimated Cost:** `1 vCPU + 1 GiB Cloud Run min-instance for ~165s in us-central1 (~$0.0044 USD list rate, <$0.01)`
- **Teardown:** `clean (Cloud Run service, IAP bindings, service accounts, and local keys deleted)`

### Cases

| # | ID | Case | Result | Evidence |
| :-: | --- | --- | :-: | --- |
| 1 | `sa-a-admitted` | SA-A admitted to API and CLI through IAP with no access code | **PASS** | GET /api/auth/user -> 200 (authenticated=true, user=grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com, authMethod=iap);GET /api/whoami -> 200 (principal=grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com, kind=service, source=iap);grain CLI (-server https://grain-iap-e2e-v3tesd4k5q-uc.a.run.app, GRAIN_AUDIENCE=https://grain-iap-e2e-v3tesd4k5q-uc.a.run.app/*, no access code) -> exit 0, JSON array |
| 2 | `sa-b-refused-by-grain` | SA-B admitted by IAP and refused by grain (403 JSON + HTML refusal page) | **PASS** | GET /api/tasks as SA-B -> 403 JSON from grain naming grain-iap-e2e-sa-b@bsalmon-sandbox.iam.gserviceaccount.com;GET / (Accept: text/html) as SA-B -> 403 HTML refusal page ('Signed in as grain-iap-e2e-sa-b@bsalmon-sandbox.iam.gserviceaccount.com (verified by iap)') |
| 3 | `no-token-refused-by-iap` | Unauthenticated requests refused at IAP edge before reaching grain | **PASS** | GET /api/tasks (no token) -> 302 with X-Goog-IAP-Generated-Response: true;GET / HTML (no token) -> 302 with X-Goog-IAP-Generated-Response: true (Location: https://accounts.google.com/o/oauth2/v2/auth) |
| 4 | `bad-token-refused` | Expired and wrong-audience tokens refused with 401 | **PASS** | expired SA-A JWT (exp=now-600s) -> 401 at IAP edge;wrong-audience SA-A JWT (aud=https://wrong-audience.example.com/*) -> 401 at IAP edge;missing-wildcard SA-A JWT (aud=https://grain-iap-e2e-v3tesd4k5q-uc.a.run.app) -> 401 at IAP edge |
| 5 | `forged-iap-assertion-rejected` | Forged X-Goog-IAP-JWT-Assertion stripped by IAP and rejected by grain | **PASS** | unauthenticated + forged X-Goog-IAP-JWT-Assertion -> 302 at IAP edge;SA-B + forged X-Goog-IAP-JWT-Assertion claiming SA-A -> stripped by IAP, refused 403 as grain-iap-e2e-sa-b@bsalmon-sandbox.iam.gserviceaccount.com;direct-to-grain forged X-Goog-IAP-JWT-Assertion -> 401 signature rejected by grain verifier |
| 6 | `require-iap-refuses-access-code` | Redeploy with -ui-require-iap refuses access code over the network | **PASS** | baseline without -ui-require-iap accepted seeded access code (200 + grain_access cookie);GET /api/auth/access-code -> 200 (requireIAP=true, accessCodeAccepted=false);POST /api/auth/access-code with valid code -> 403 (-ui-require-iap);SA-B + valid grain_access cookie -> 403 (cookie ignored under -ui-require-iap);direct network Bearer <access-code> -> 401 at Cloud Run ingress (never reaches grain);direct network X-Grain-Access-Code -> 401 (-ui-require-iap);direct network Cookie grain_access -> 401 (-ui-require-iap);direct network GET / HTML -> 401 IAP-required page (no access-code form);loopback break-glass covered by the simulated suite (task 2510) |
| 7 | `csrf-cross-origin-refused` | Cross-origin state-changing requests refused with 403 | **PASS** | POST /api/tasks with Origin: https://evil.example -> 403;Sec-Fetch-Site: cross-site -> 403;Origin: null -> 403;Cookie-bearing POST with no Origin/Sec-Fetch-Site -> 403;same-origin POST (Origin: https://grain-iap-e2e-v3tesd4k5q-uc.a.run.app) -> 201 |
| 8 | `attribution-sa-a` | Task filed by SA-A recorded as service SA-A, not operator | **PASS** | task #3: author=grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com (authorKind=service), comment.author=grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com (service), close transition.by='service grain-iap-e2e-sa-a@bsalmon-sandbox.iam.gserviceaccount.com' (daemon -as=operator@example.com not used) |
