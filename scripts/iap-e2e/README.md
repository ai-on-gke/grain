# Cloud Run Direct IAP End-to-End Qualification (`scripts/iap-e2e/`)

`scripts/iap-e2e/run.sh` is grain's real Google Cloud Identity-Aware Proxy (IAP) qualification script ([Task #2774](/tasks/2774), replacing [Task #2512](/tasks/2512)). Unlike the local simulator in `tests/iap/`, it deploys `grain daemon` to **Cloud Run with IAP enabled directly on the service (`--iap`)** over Cloud Run's default `*.run.app` domain — with no load balancer, managed certificate, DNS zone, or custom OAuth brand — and exercises real Google-issued tokens and real `X-Goog-IAP-JWT-Assertion` ES256 headers verified against `https://www.gstatic.com/iap/verify/public_key`.

## How Cloud Run Direct IAP Authenticates Programmatic Callers

Cloud Run's native `--iap` integration uses Google's managed OAuth client rather than a project-owned OAuth 2.0 client ID. That distinction determines both the token format a programmatic caller sends and the assertion `grain` verifies upstream:

| Hop / Token | Header | Issuer (`iss`) | Audience (`aud`) | Notes |
| --- | --- | --- | --- | --- |
| **Caller -> Cloud Run IAP edge** | `Authorization: Bearer <jwt>` | `<sa>@<project>.iam.gserviceaccount.com` (`sub` = same SA email) | **`https://<service-host>/*`** (trailing `/*` is **required**) | RS256 **service-account self-signed JWT** signed with the SA's key (`kid` = `private_key_id`). Standard `accounts.google.com` OIDC ID tokens (`gcloud auth print-identity-token --audiences=...`) are rejected with `401 Invalid JWT audience` unless a custom same-org OAuth client ID is registered in `iap.settings.accessSettings.oauthSettings.programmaticClients`. |
| **IAP edge -> Cloud Run ingress** | `Authorization: Bearer <invoker-token>` | Google IAM | Cloud Run service URL | IAP replaces the caller's `Authorization` header with an invoker token for the **IAP Service Agent** (`service-<PROJECT_NUMBER>@gcp-sa-iap.iam.gserviceaccount.com`), which must hold `roles/run.invoker` (`run.routes.invoke`) on the Cloud Run service. |
| **IAP edge -> `grain daemon`** | `X-Goog-IAP-JWT-Assertion: <es256-jwt>` | `https://cloud.google.com/iap` | **`/projects/<PROJECT_NUMBER>/locations/<REGION>/services/<SERVICE>`** | Verified by `grain` against `https://www.gstatic.com/iap/verify/public_key` (`-ui-iap-audience`). IAP strips any caller-supplied `X-Goog-*` headers before injecting its own assertion. |

### `Authorization` vs. `Proxy-Authorization`

- Send the service-account JWT in **`Authorization: Bearer <jwt>`**.
- Sending `Proxy-Authorization: Bearer <jwt>` **alone** (with no `Authorization` header) is rejected by IAP with `401 Invalid IAP credentials: empty token`: IAP only consults `Proxy-Authorization` when `Authorization` is simultaneously occupied by an application-level credential. On Cloud Run Direct IAP, IAP consumes `Authorization` at the edge and replaces it upstream with the IAP service agent's Cloud Run invoker token anyway.
- `grain` CLI supports Cloud Run Direct IAP natively when `GOOGLE_APPLICATION_CREDENTIALS` points to a service-account JSON key file and `-audience` / `$GRAIN_AUDIENCE` is set to `https://<service-host>/*`:

  ```bash
  GOOGLE_APPLICATION_CREDENTIALS=/path/to/sa.json \
  GRAIN_AUDIENCE="https://<service-host>/*" \
    grain -server "https://<service-host>" list
  ```

## Required GCP Project & Permissions

- **Target project:** `bsalmon-sandbox` (or `--project <id>`).
- **APIs enabled idempotently by `run.sh`:**
  - `run.googleapis.com`
  - `iap.googleapis.com`
  - `artifactregistry.googleapis.com` (`secretmanager.googleapis.com` is not needed; Cloud Run pulls `ghcr.io/bwsalmon/grain/grain:<tag>` directly).
- **Required IAM roles on the runner's `gcp-key` permission set (`iap-test`):**
  - `roles/editor` — enables APIs, deploys Cloud Run revisions, and creates/deletes the two ephemeral test service accounts (`<service>-sa-a` and `<service>-sa-b`) and their temporary keys.
  - `roles/run.admin` — grants `roles/run.invoker` (`run.services.setIamPolicy`) on the ephemeral Cloud Run service to the IAP Service Agent (`service-<PROJECT_NUMBER>@gcp-sa-iap.iam.gserviceaccount.com`) so IAP can invoke Cloud Run after authenticating a caller (`roles/editor` omits `run.services.setIamPolicy`).
  - `roles/iap.admin` — binds `roles/iap.httpsResourceAccessor` on the Cloud Run IAP web resource (`gcloud iap web add-iam-policy-binding --resource-type=cloud-run`) without needing project-level `resourcemanager.projects.setIamPolicy`.
- **Out of scope for this grant (by design):** project-level IAM bindings (`resourcemanager.projects.setIamPolicy`), Workload Identity Federation, and Cloud Identity `group:` lookups (covered by `tests/iap/walkthrough.sh`).

## Qualification Cases

| # | Case ID | What It Verifies |
| :-: | --- | --- |
| 1 | `sa-a-admitted` | `SA-A` reaches `/api/auth/user` (`authenticated=true`, `authMethod="iap"`), `/api/whoami` (when exposed), and `grain` CLI (`list`) over IAP with **no access code**. |
| 2 | `sa-b-refused-by-grain` | `SA-B` passes IAP (`roles/iap.httpsResourceAccessor`) and reaches `grain`, which refuses with HTTP `403` JSON naming `SA-B` on API calls and the `403` HTML refusal page (`Signed in as <SA-B> (verified by iap)`) on browser navigation. |
| 3 | `no-token-refused-by-iap` | Requests with no token are rejected at Google's IAP edge (`401` on `/api/tasks`, `302` redirect to `accounts.google.com` on `GET /` HTML, with `X-Goog-IAP-Generated-Response: true`) and never reach `grain`. |
| 4 | `bad-token-refused` | Expired (`exp < now`), wrong-audience (`https://wrong-audience.example.com/*`), and missing-wildcard (`https://<host>`) JWTs are rejected at the IAP edge with `401`. |
| 5 | `forged-iap-assertion-rejected` | Caller-injected `X-Goog-IAP-JWT-Assertion` (plus spoofed `X-Goog-Authenticated-User-Email`, `X-Forwarded-User`, `X-Grain-User`) is rejected when unauthenticated, stripped/overwritten by IAP when sent by `SA-B` (preventing escalation to `SA-A`), and rejected with `401` by `grain`'s real Google JWK/PEM verifier when sent directly without IAP. |
| 6 | `require-iap-refuses-access-code` | Redeploying with `-ui-require-iap` flips `/api/auth/access-code` to `requireIAP: true, accessCodeAccepted: false`, refuses a valid seeded access code on `POST /api/auth/access-code` (`403`), ignores a valid `grain_access` cookie, and refuses direct network access-code (`X-Grain-Access-Code`), cookie and HTML access (`401` from grain). A direct `Authorization: Bearer <access-code>` is refused by Cloud Run's ingress before it reaches grain: Cloud Run checks every bearer as a Google ID token, even with `allUsers` as invoker. Loopback break-glass is recorded as covered by the simulated suite ([Task #2510](/tasks/2510)). |
| 7 | `csrf-cross-origin-refused` | State-changing `POST /api/tasks` requests authenticated via IAP are refused (`403`) when `Origin: https://evil.example`, `Sec-Fetch-Site: cross-site`, `Origin: null`, or cookie-bearing without `Origin`/`Sec-Fetch-Site`, while same-origin (`Origin: <service-url>`) succeeds (`201`). |
| 8 | `attribution-sa-a` | Even with `-as=operator@example.com` set on `grain daemon`, a task created, commented on, and closed by `SA-A` records `author: "<SA-A>"`, `authorKind: "service"`, and transition `by: "service <SA-A>"` — never `operator`. |

## Running the Qualification Script

```bash
# Default: current unpromoted vX.Y.Z-rc.N image if present, else ghcr.io/bwsalmon/grain/grain:main
./scripts/iap-e2e/run.sh \
  --project bsalmon-sandbox \
  --region us-central1 \
  --out /tmp/grain-iap-e2e

# Explicit release-candidate image:
./scripts/iap-e2e/run.sh \
  --image ghcr.io/bwsalmon/grain/grain:v3.2.0-rc.1 \
  --project bsalmon-sandbox \
  --out /tmp/grain-iap-e2e
```

Outputs written to `--out`:
- `report.json` — machine-readable case verdicts, target metadata, runtime, cost estimate, and teardown status.
- `report.md` — human-readable Markdown report ready to archive under `docs/test-runs/`.

Exit code is `0` if and only if all 8 cases pass; any case or setup failure exits non-zero after running full teardown (`Cloud Run` service, IAP bindings, temporary service accounts, and local key files). Typical wall-clock runtime is **3–6 minutes** at **<$0.01 USD** per run (`1 vCPU` / `1 GiB` Cloud Run instance during the test window).

### Images that keep the allowlist in the database

Since `992714cd` (`feat(access): keep allowed users and groups in the database`, on `main` from 2026-11-03), grain holds its allowlist in the database (Settings -> Access) and drops `serviceAccount:` entries from `-ui-allowed-principals`. The operator decided (task 2942) that service accounts may be on that list again, but only when someone adds each one explicitly with a reason (`grain access allow serviceAccount:<email> --reason "<why>"`). The gcp-key pool and gcp permission-set accounts are always refused. See `docs/access-code.md`.

`run.sh` therefore starts the daemon and then, once it answers, adds SA-A over loopback with the seeded access code: `grain access allow serviceAccount:<SA-A> --reason "iap e2e"`. It does this both in the Cloud Run container and in `--local-sim`. It still passes `-ui-allowed-principals=serviceAccount:<SA-A>` so that `v3.1.x` images, which have no `grain access`, keep working. Database-allowlist images record that flag entry as dropped, and Settings -> Access shows the warning. Images built between `992714cd` and this change refuse `serviceAccount:` entries outright, so on them `run.sh` stops at setup with a `BLOCKER` before any case runs. Use the default image, or any image cut after this change.

### Local Self-Test (No GCP Credentials Required)

```bash
./scripts/iap-e2e/run_test.sh
```

## Operator Manual Browser Check (3-Line Checklist)

When inspecting a live Cloud Run `--iap` deployment in a browser (`--keep`, with `user:<you>` granted `roles/iap.httpsResourceAccessor` and added to `-ui-allowed-principals`):

1. Open the Cloud Run `https://<service>-<project-number>.<region>.run.app` URL in an incognito browser window and confirm IAP redirects to Google Sign-In (`accounts.google.com`) and admits you back to the grain UI with no access-code prompt.
2. Open `/api/auth/user` (or inspect the signed-in badge in the UI header) and confirm `"authenticated": true`, `"authMethod": "iap"`, and `"user": "<your-email>"`.
3. Create a test task in the UI and confirm its author and audit trail show `<your-email>` (`user`), not `operator`, then delete the test Cloud Run service when finished.
