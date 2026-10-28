# Test Run: Auth Method and IAP Password Controls

Plan: `docs/test-plans/2026-10-07-evening.md` §2.
Task 2538 (filed by task 2516, Origin: task 2516).
Commits under test: `f45bfe28f` on `candidate`, ported from `main` `ee4900cf7`.

## Environment

- The grain sandbox VM: Debian 12, 4 vCPU, 7.8 GiB RAM, with systemd as PID 1.
- Node.js v20 / npm 10. Production UI bundle built in `pkg/ui/static` via `npm ci && npm run build` inside `ui/`.
- Grain binary `/tmp/grain` built with `go build -o /tmp/grain ./cmd/grain`.
- Playwright Chromium installed from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` (`chromium-1234` and `chromium_headless_shell-1234`).
- Daemons executed with `-disable-native-sandboxes -ci=false -ui-allowed-hosts "*"`.

## Tested Commit

- Merge base commit: `9b0b38fc1` on `origin/candidate`.
- Commit under test: `f45bfe28f` (`feat(ui): authMethod/hasPassword on /api/auth/user and /api/config, hide password controls from IAP users`), verified present in `origin/candidate` history via `git branch -r --contains f45bfe28f`.

## Stubs

| Boundary | Stub | Used for |
|---|---|---|
| Google Cloud IAP | Local ECDSA signing reverse proxy `/tmp/iap-proxy` (compiled from `/tmp/auth-walkthrough/iap_proxy_server.go`). Emits valid ES256 JWT assertions signed by an ephemeral ECDSA P-256 key matching the public key endpoint at `/keys`, or forged assertions signed by an untrusted ECDSA key, or unauthenticated requests. Bound to `-ui-iap-audience /projects/12345/global/backendServices/67890` and `-ui-iap-keys-url http://127.0.0.1:18881/keys`. | Standing in for Google's Identity-Aware Proxy to test assertion verification, refusal logging, and header parsing. |

Grain's own APIs, authentication handling, database models, and React UI components were not mocked.

---

## 1. Browser behind signing proxy as `iap@example.com`

**Configuration**:
The daemon was started with:
```bash
/tmp/grain daemon \
  -disable-native-sandboxes \
  -data-dir /tmp/grain-browser-run-.../data \
  -ui-addr 127.0.0.1:18420 \
  -ci=false \
  -ui-iap-audience /projects/12345/global/backendServices/67890 \
  -ui-iap-keys-url http://127.0.0.1:18881/keys \
  -ui-allowed-hosts "*"
```
The reverse proxy intercepted traffic on port `18882`, signing an `X-Goog-IAP-JWT-Assertion` with valid ES256 signature containing:
- `sub`: `accounts.google.com:10001`
- `email`: `iap@example.com`
- `aud`: `/projects/12345/global/backendServices/67890`
- `iss`: `https://cloud.google.com/iap`

**API Verification**:
- `GET /api/auth/user`:
  ```json
  {
    "authenticated": true,
    "user": "iap@example.com",
    "passwordAuthEnabled": true,
    "googleOAuthEnabled": false,
    "isSystemAdmin": false,
    "adminMode": false,
    "authMethod": "iap",
    "hasPassword": false
  }
  ```
- `GET /api/config`:
  ```json
  {
    "actor": "iap@example.com",
    "authMethod": "iap",
    "hasPassword": false,
    "authenticated": true
  }
  ```

**UI Verification (Playwright Browser Test)**:
- Browser navigated to `http://127.0.0.1:18882/`.
- Opened the User Settings dialog via `getByTestId("user-settings-button")`.
- Heading displays: **Settings**.
- Identity indicator displays: **"Signed in through IAP as iap@example.com"**.
- The **"Log out"** button is **NOT present** (`toHaveCount(0)`).
- The **"Change password"** form and heading are **NOT present** (`toHaveCount(0)`).

---

## 2. Forged assertion

### 2a. Forged assertion together with valid password session
- A password user `testuser` was created via `POST /api/auth/register`, and logged in via `POST /api/auth/login`, obtaining a valid `grain_session` cookie.
- A request was dispatched carrying both the valid `grain_session` cookie AND an untrusted/forged `X-Goog-IAP-JWT-Assertion` (signed with an ECDSA private key whose public key is not published in `/keys`).
- **Response**:
  ```json
  {
    "authenticated": true,
    "user": "testuser",
    "passwordAuthEnabled": true,
    "googleOAuthEnabled": false,
    "isSystemAdmin": false,
    "adminMode": false,
    "authMethod": "password",
    "hasPassword": true
  }
  ```
- The forged IAP assertion was completely rejected and ignored; session cookie authentication took precedence.
- Daemon log recorded:
  ```
  ui: refused an X-Goog-IAP-JWT-Assertion (issuer "https://cloud.google.com/iap", audience "/projects/12345/global/backendServices/67890"): invalid IAP JWT assertion: token signature is invalid: crypto/ecdsa: verification error -- if the proxy that sent it is this deployment's own, set -ui-iap-audience (GRAIN_IAP_AUDIENCE) and, for a proxy other than IAP, -ui-iap-issuer and -ui-iap-keys-url to match
  ```

### 2b. Forged assertion alone (no valid session)
- A request was sent with the forged `X-Goog-IAP-JWT-Assertion` alone, without any session cookie.
- **Response**:
  ```json
  {
    "authenticated": false,
    "passwordAuthEnabled": true,
    "googleOAuthEnabled": false,
    "identityNotAccepted": "This request's identity was not accepted because its X-Goog-IAP-JWT-Assertion did not verify against this deployment's IAP audience, issuer and keys (-ui-iap-audience, -ui-iap-issuer, -ui-iap-keys-url).",
    "isSystemAdmin": false,
    "adminMode": false,
    "hasPassword": false
  }
  ```
- No principal was recognized; `identityNotAccepted` is populated with the diagnostic explanation; `authenticated` is `false`.

---

## 3. IAP user with password account

- An account for `iap@example.com` exists and was given a password hash in the store (or created as a password user).
- When `iap@example.com` accesses Grain through the valid IAP reverse proxy:
  - `GET /api/auth/user` returns:
    ```json
    {
      "authenticated": true,
      "user": "iap@example.com",
      "authMethod": "iap",
      "hasPassword": true
    }
    ```
  - `GET /api/config` returns:
    ```json
    {
      "actor": "iap@example.com",
      "authMethod": "iap",
      "hasPassword": true,
      "authenticated": true
    }
    ```
- **UI Behavior (Playwright Browser Test)**:
  - User Settings dialog displays: **"Signed in through IAP as iap@example.com"**.
  - Because `authMethod === 'iap'`, the **"Change password"** form is **deliberately hidden** despite `hasPassword === true`.
  - The **"Log out"** button is also hidden.
  - Password management controls are not exposed to users authenticated via IAP.

---

## 4. Demo and trusted sources

- **Loopback with nobody signed in**:
  - `GET /api/auth/user` directly on loopback `127.0.0.1:18420`:
    ```json
    {
      "authenticated": false,
      "authMethod": "trusted",
      "hasPassword": false
    }
    ```
  - `GET /api/config` on loopback:
    ```json
    {
      "actor": "debian",
      "authMethod": "trusted",
      "hasPassword": false,
      "authenticated": false
    }
    ```
  - In the UI Settings pane:
    - Since `authenticated` is false and `hasPassword` is false, no password form is offered.
    - Loopback warning alert is displayed when multiple accounts/teams exist.

- **Password user (`alice`)**:
  - `alice` registered with password and logged in with `grain_session` cookie:
    ```json
    {
      "authenticated": true,
      "user": "alice",
      "authMethod": "password",
      "hasPassword": true
    }
    ```
  - In the UI Settings pane:
    - User Settings dialog displays **"Change password"** form (`<h3>` / `<h6>` heading and inputs).
    - The **"Log out"** button is visible and active.
    - Password can be changed successfully.

---

## 5. Users panel & Admin mode

- **Access to `GET /api/users`**:
  - Standard non-admin user (`alice`) with session cookie:
    - Calling `GET /api/users`: Returns **`403 Forbidden`** (`{"error":"user \"alice\" does not have administrator privileges"}`).
    - Non-admin users cannot read `/api/users` or access user credential states.
  - Direct loopback operator (or administrator with Admin mode):
    - Calling `GET /api/users`: Returns **`200 OK`** with JSON array:
      ```json
      [
        {
          "id": "iap@example.com",
          "username": "iap@example.com",
          "hasPassword": true
        },
        {
          "id": "alice",
          "username": "alice",
          "hasPassword": true
        }
      ]
      ```
    - Each user entry accurately reports `hasPassword: true/false`.
    - No password hashes or secrets are exposed in the response.

---

## Conclusion & Verdict

All requirements in §2 of `docs/test-plans/2026-10-07-evening.md` have been thoroughly tested across both direct API endpoints and end-to-end headless browser interaction with the built production UI.
- `authMethod` and `hasPassword` correctly convey the authentication mechanism and credential status.
- IAP authenticated users never see password controls or log out buttons, even when a password row exists in the database.
- Forged assertions are safely rejected and logged, falling back to valid session cookies if present or unauthenticated state with diagnostic refusal message.
- Full verification passed cleanly; no flaws or regressions detected.
