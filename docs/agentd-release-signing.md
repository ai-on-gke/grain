# Signing `grain-agentd` Release Binaries

Follow-up 5 to `design/design/agentd-stable-contract.design.md` ("The safety valve"), prerequisite for owner-run `grain-agentd update`.

## Why keyed Ed25519

`grain-agentd` runs on operator workstations and remote Linux hosts that may not reach the public internet and carry no external signature-verification tooling. A controller compromise must not let the controller push arbitrary binaries onto enrolled hosts: every binary accepted by `grain-agentd` must be signed by a **release signing key held outside the controller**, verified against the public key **pinned on the host at install time** (`~/.config/grain/agentd-release.pub` and embedded in `grain-agentd`).

Releases sign per-platform manifests with **Ed25519** (RFC 8032 / RFC 8410) rather than keyless Sigstore/Fulcio:

- **Offline verification with zero external dependencies.** `grain-agentd` verifies Ed25519 signatures directly via Go's standard library (`crypto/ed25519`), with no `cosign` binary, no OIDC identity check, and no Rekor transparency log query over the internet.
- **Standard OpenSSL 3 interoperability.** Private keys are standard unencrypted PKCS#8 PEM (`-----BEGIN PRIVATE KEY-----`) and public keys are standard SubjectPublicKeyInfo PEM (`-----BEGIN PUBLIC KEY-----`, or `ed25519:<base64>`). The `contents: write` release-asset jobs sign manifests using preinstalled `openssl pkeyutl -sign -rawin` without installing a Go toolchain or running build artifacts.
- **Explicit forward-only key rotation ("the old key signs the new").** Routine key rotation needs no controller trust and no manual walk across enrolled hosts.

## Per-platform assets and manifest format

Every `vX.Y.Z` and `vX.Y.Z-rc.N` release publishes static (`CGO_ENABLED=0`) Linux binaries alongside their signed manifests (`scripts/build-agentd.sh`, `pkg/agentd/release`):

- `grain-agentd-linux-amd64` and `grain-agentd-linux-amd64.manifest.json`
- `grain-agentd-linux-arm64` and `grain-agentd-linux-arm64.manifest.json`

Each `.manifest.json` (`schema: 1`) carries:

```json
{
  "schema": 1,
  "version": "v3.5.0",
  "platform": "linux/amd64",
  "asset": "grain-agentd-linux-amd64",
  "sha256": "<64 lowercase hex digits>",
  "size": 11489280,
  "min_contract_features": ["start"],
  "features": ["start", "detach", "drain", "socket_owner", "stage", "listen", "renew", "logins", "agents_only", "features"],
  "key_id": "<16-hex SHA-256 prefix of raw 32-byte Ed25519 public key>",
  "signature": "ed25519:<base64 64-byte signature>",
  "rotations": []
}
```

To avoid JSON canonicalization ambiguity or duplicate-key smuggling, `ParseManifest` rejects unknown fields and verifies `signature` over the deterministic domain-separated text:

```text
grain-agentd-release-manifest/v1
schema: 1
version: v3.5.0
platform: linux/amd64
asset: grain-agentd-linux-amd64
sha256: <sha256>
size: <size>
min_contract_features: start
features: start,detach,...
key_id: <key_id>
```

## Key custody

The release signing private key must never be stored on a Grain controller (neither in its data directory nor in its secrets store). Hold it in an offline keystore or in the release repository's CI secret store (`secrets.AGENTD_RELEASE_SIGNING_KEY`), exposed only to the verified release-publishing jobs (`publish-release.yml`, `promote-release.yml`, `promote.yml`, and `release-train-assets.yml`).

### Generating the initial keypair

```sh
go run ./pkg/agentd/release/cmd/grain-agentd-sign keygen \
  -private-key agentd-release.key \
  -public-key pkg/agentd/release/agentd-release.pub \
  -force
```

(Equivalently, `openssl genpkey -algorithm Ed25519 -out agentd-release.key` and `openssl pkey -in agentd-release.key -pubout -out pkg/agentd/release/agentd-release.pub`.)

1. Store `agentd-release.key` outside the controller as the repository secret `AGENTD_RELEASE_SIGNING_KEY` (and keep an offline backup for future key rotation), then delete the working copy.
2. Commit `pkg/agentd/release/agentd-release.pub` (or set the repository variable `AGENTD_RELEASE_PUBLIC_KEY` to stamp `-X github.com/ai-on-gke/grain/pkg/agentd/release.ReleasePublicKey=...` at build time).
3. Optionally set the repository variable `REQUIRE_AGENTD_SIGNING=true` so release publishing fails closed if `AGENTD_RELEASE_SIGNING_KEY` is absent.

At install time, `grain-agentd install` writes the binary's embedded public key to `~/.config/grain/agentd-release.pub` (mode `0644` inside `0700` `~/.config/grain`), keeping any valid file already present on re-runs. `agentd.PinnedReleasePublicKey` loads `~/.config/grain/agentd-release.pub` (refusing symlinks and group- or world-writable files) and falls back to the embedded key when absent.

## Key rotation: the old key signs the new

### Routine rotation (outgoing key is intact)

When rotating from outgoing key `A` (`old.key`) to replacement key `B` (`new.key` / `new.pub`):

1. Generate the replacement keypair:
   ```sh
   go run ./pkg/agentd/release/cmd/grain-agentd-sign keygen \
     -private-key new.key \
     -public-key new.pub
   ```
2. Sign a delegation record with the outgoing private key, appending to (or creating) `agentd-rotations.json`:
   ```sh
   go run ./pkg/agentd/release/cmd/grain-agentd-sign rotate \
     -old-private-key old.key \
     -new-public-key new.pub \
     -chain agentd-rotations.json
   ```
   Each hop signs:
   ```text
   grain-agentd-key-rotation/v1
   old_public_key: ed25519:<base64>
   new_public_key: ed25519:<base64>
   issued_at: <RFC3339 UTC>
   ```
3. Commit `new.pub` to `pkg/agentd/release/agentd-release.pub` (so newly installed hosts pin `B` immediately), update `secrets.AGENTD_RELEASE_SIGNING_KEY` to `new.key`, and set the repository variable `AGENTD_RELEASE_ROTATIONS` to the JSON contents of `agentd-rotations.json`.
4. Retire `old.key` once `agentd-rotations.json` is recorded.

Verification walks `rotations` **strictly forward** from the host's own pinned key:

- A host still pinned to `A` verifies `A -> B` using `A`, then verifies the manifest's signature using `B`, and advances `~/.config/grain/agentd-release.pub` to `B` once the update completes.
- A host already pinned to `B` verifies `B` directly and never walks backward, so retired key `A` can never sign a manifest accepted by a host pinned to `B`.
- Once all active hosts have updated past `A`, `AGENTD_RELEASE_ROTATIONS` can be cleared.

### Emergency rotation (compromised key)

If a signing key is compromised rather than retired cleanly, do **not** sign a rotation from the compromised key (an attacker holding it could sign competing rotations). Instead:

1. Generate a new keypair, commit the new `pkg/agentd/release/agentd-release.pub`, replace `AGENTD_RELEASE_SIGNING_KEY`, and clear `AGENTD_RELEASE_ROTATIONS`.
2. Replace `~/.config/grain/agentd-release.pub` on enrolled hosts out-of-band (or remove `~/.config/grain/agentd-release.pub` and re-run `grain-agentd install` with a binary built from the new release).

## Serving binaries and manifests from the controller

Hosts that cannot reach `github.com` fetch both the manifest and binary from the controller's open install endpoints (`pkg/ui/hostd_install.go`):

- `GET /api/hostd/agentd/linux/amd64` and `GET /api/hostd/agentd/linux/amd64/manifest.json`
- `GET /api/hostd/agentd/linux/arm64` and `GET /api/hostd/agentd/linux/arm64/manifest.json`

`curl -fsSL https://<controller>/hostd/install | sh -s -- --agentd --join <code>` downloads the manifest and binary from these routes automatically when `grain-agentd` is not already installed on the host, verifying the binary's SHA-256 against the manifest before running `grain-agentd install`.

The controller resolves each platform's asset in order:

1. A local distribution directory (`$GRAIN_AGENTD_DIST_DIR`, defaulting to `/usr/local/share/grain/agentd`) carrying a **signed** manifest and binary verified against the pinned release public key -- for air-gapped controllers where an operator mirrors the signed release assets onto disk.
2. Lazy fetch and on-disk cache from `$GRAIN_AGENTD_RELEASE_URL` (defaulting to `$GRAIN_CLI_RELEASE_URL`, else GitHub Releases; set `GRAIN_AGENTD_RELEASE_URL=off` to disable) for the controller's own release version, verified against the pinned key before serving (an unsigned upstream release is refused), and cached in a 0700 directory of the controller's own.
3. An unsigned manifest and binary in the same local directories, checked only against the manifest's own SHA-256 and size: the static binaries baked into `/usr/local/share/grain/agentd` inside the controller image (`Dockerfile`), or whatever an operator placed in `$GRAIN_AGENTD_DIST_DIR`, so bootstrap installs still work when both the host and the controller are offline. A local manifest that is signed but fails verification against the pinned key is never served.
