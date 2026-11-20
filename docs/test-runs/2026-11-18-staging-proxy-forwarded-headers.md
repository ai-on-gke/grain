# Staging proxy: the edge's scheme and client chain (2026-11-18)

This is the follow-up to [2026-11-18-staging-proxy-stale-chunks.md](2026-11-18-staging-proxy-stale-chunks.md), task 7117. `handleStagingProxy` now forwards the edge's `X-Forwarded-Proto` and keeps its `X-Forwarded-For` chain. It does so only for a request that carries an assertion a registered identity verifier accepted.

## Setup

I used the same harness, `source docs/test-runs/2026-11-18-staging-proxy-stale-chunks/env.sh && up_walk sqlite`:

- the `grain-slowverifier` daemon, built from this branch, on `127.0.0.1:4291` with `-ui-require-verifier=slow-proxy`;
- the TLS identity-aware proxy on `4292`, which sends `X-Forwarded-Proto: https` and `X-Forwarded-For: 198.51.100.42`;
- the hostile echo app `preview` at `/staging/preview/`.

Then I ran `OUT=/tmp/probe7117 bash probe_staging.sh`. Every case passed (`FAILS=0`).

## Headers the app echoed (through the edge)

| Case | `X-Forwarded-Proto` | `X-Forwarded-For` | `X-Forwarded-Host` | `X-Forwarded-Prefix` |
|---|---|---|---|---|
| h1-canonical (HTTP/1.1) | `https` | `198.51.100.42, 127.0.0.1` | `127.0.0.1:4292` | `/staging/preview` |
| h2-canonical (HTTP/2) | `https` | `198.51.100.42, 127.0.0.1` | `127.0.0.1:4292` | `/staging/preview` |
| websocket (101) | `https` | `198.51.100.42, 127.0.0.1` | `127.0.0.1:4292` | `/staging/preview` |

Before this change (task 7058's walk), the same cases echoed `X-Forwarded-Proto: http` and `X-Forwarded-For: 127.0.0.1`.

## Straight to the daemon, with forged headers

Each request sent `X-Forwarded-Proto: https` and `X-Forwarded-For: 198.51.100.42` to `http://127.0.0.1:4291/staging/preview/echo`:

| Credential | Result |
|---|---|
| access code (`Authorization: Bearer …`) | 401, the "sign in through slow-proxy" page (`-ui-require-verifier`) |
| none | 401 |
| forged `X-Slow-Proxy-Assertion` | 401 |

Under `-ui-require-verifier`, a direct caller never reaches the app. On a daemon without it, the access code is admitted, and the forged headers are ignored. `TestStagingProxyForwardsVerifiedEdgeSchemeAndChain` (`pkg/ui`) and `TestStagingProxyTrustsForwardingHeadersOnlyFromTheVerifiedEdge` (`tests/security`) check that: the app is sent `http` and `127.0.0.1`.
