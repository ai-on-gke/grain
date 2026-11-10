# Nightly Prompt: Guest Image Lifecycle, Packing & Rebase

## Objective
Walk through and test the guest image lifecycle components (`pkg/guestimage`, `pkg/guestpack`, `pkg/guestrebase`, `pkg/guestverify`, `pkg/guestboot`, `deploy/guest-image/`), verifying guest rootfs overlays, boot contracts, image packing, rebase operations, and image publishing dry runs.

## Context & Key Workflows
Granule images package hypervisor binaries, Linux guest kernels, initramfs, in-guest agents (`granule-agent`), and guest rootfs layers into standard OCI images. Key nightly verification areas include:
1. **Guest Rootfs Overlays**: Debian (`overlay-debian`) and Alpine (`overlay-alpine`) initialization scripts, ACPI power-button handlers (`etc/acpi/powerbtn.sh`), network configuration, and serial console setup.
2. **Boot Contract Verification**: Asserting that rootfs contains valid init systems (systemd or OpenRC), kernel modules, agent sockets, and contract sidecars.
3. **Guest Packing (`granule guest pack`)**: Converting raw rootfs directories or container images into bootable granule VM images.
4. **Guest Rebase (`granule guest rebase`)**: Upgrading the hypervisor/supervisor runtime layer under an existing guest image without modifying or corrupting guest rootfs layers.
5. **Guest Verification (`granule guest verify`)**: Testing dry-run and validation logic for guest images.
6. **Publishing Workflow Rehearsal**: Verifying that `deploy/publish-images.sh` publishes each variant to its own repository under `ghcr.io/gke-labs/granule/`: the packed guest image at `ghcr.io/gke-labs/granule/<variant>:latest` and the guest rootfs it was packed from at `ghcr.io/gke-labs/granule/<variant>.rootfs:latest`.

## Execution Steps

### 1. Run Guest Subsystem Package Tests
Run tests for all guest-related packages:
```bash
go test -v ./pkg/guestimage/... ./pkg/guestpack/... ./pkg/guestrebase/... ./pkg/guestverify/... ./pkg/guestboot/... ./pkg/guestcp/...
```
- Verify rootfs layout parsing, boot contract checks, and volume mapping logic.
- Verify rebase safety checks (ensuring rebase refuses images without kernels or with incompatible contracts).

### 2. Validate Guest Packaging Scripts & Overlays
Inspect and validate the guest packaging shell scripts and overlay files:
```bash
# Verify syntax of all guest image deployment scripts
find deploy/guest-image -type f -name "*.sh" -exec bash -n {} +

# Verify presence of essential overlay files
test -f deploy/guest-image/overlay-common/etc/granule/guest-contract
test -f deploy/guest-image/overlay-debian/etc/acpi/powerbtn.sh
test -f deploy/guest-image/overlay-alpine/etc/acpi/powerbtn.sh
```

### 3. Rehearse Image Publishing Dry Run
Run the publish script dry-run to verify tag generation and registry destination paths:
```bash
deploy/publish-images.sh --dry-run --no-login all
```
- Verify output pushes the guest and runner images `ghcr.io/gke-labs/granule/debian:latest`, `ghcr.io/gke-labs/granule/alpine:latest`, `ghcr.io/gke-labs/granule/debian12:latest` and `ghcr.io/gke-labs/granule/github-ubuntu-slim:latest`.
- Verify output pushes the guest rootfs images `ghcr.io/gke-labs/granule/debian.rootfs:latest`, `ghcr.io/gke-labs/granule/alpine.rootfs:latest` and `ghcr.io/gke-labs/granule/debian12.rootfs:latest`.

### 4. Test Guest CLI Commands
Build and test guest CLI operations:
```bash
go build -o /tmp/granule ./cmd/granule

# Verify help and flag structures
/tmp/granule guest build -h
/tmp/granule guest pack -h
/tmp/granule guest rebase -h
/tmp/granule guest verify -h
```

## Evaluation & Reporting Criteria
1. **Packaging Integrity**: Confirm all guest overlays maintain valid permissions and shell syntax.
2. **Contract Preservation**: Verify that rebase operations preserve guest contract sidecars and do not introduce stale metadata.
3. **Publishing Validation**: Confirm all publish tags match current repository conventions.
4. **Failure Analysis**: Report any broken overlay symlinks, missing contract metadata, or packaging errors.
