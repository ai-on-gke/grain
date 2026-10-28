# Nightly Prompt: Granulefile Specification Conformance & Compiler

## Objective
Walk through and rigorously test the Granulefile compiler (`pkg/granulefile`, `granule granulefile check`, `granule granulefile build`) across all language constructs and worked examples defined in `docs/granulefile.md` (§14.1 - §14.9), verifying parser accuracy, intermediate stage translation, and error diagnostics.

## Context & Key Workflows
The Granulefile format is a strict superset of Dockerfile syntax with the added `VM` instruction. Key workflows to validate nightly include:
1. **Pure Container Stages**: Validating standard Dockerfiles execute without VM overhead.
2. **Live In-VM Execution (`VM FROM`)**: Inheriting from bootable VM images and executing `RUN` inside the VM.
3. **Offline Guest Rootfs Composition (`VM PACK` / `VM PACK FROM`)**: Modifying guest OS rootfs in container stages and packing into bootable VM disks offline.
4. **Container Wrapping (`VM WRAP` / `VM WRAP FROM`)**: Packaging standard container images into bootable microVMs.
5. **Disk Extraction (`VM UNPACK FROM`)**: Extracting raw disk layers into container stages.
6. **Multi-Stage Cross-Stage Copies (`COPY --from=<stage>`)**: Copying build artifacts between VM-to-VM, container-to-VM, and VM-to-container stages.
7. **Volume & Workload Directives**: `VOLUME` mounts (auxiliary disks), `ENTRYPOINT`, and `CMD`.
8. **Negative Diagnostics**: Rejection of invalid stage transitions, missing kernels, circular dependencies, and unknown flags.

## Execution Steps

### 1. Granulefile Compiler Unit & Use-Case Tests
Run the compiler package tests with verbose reporting:
```bash
go test -v ./pkg/granulefile/... ./pkg/guestdockerfile/...
```
- Assert that tests covering §14.1 through §14.9 pass cleanly.
- Verify AST generation, instruction lexing, parser table rules, and validation logic.

### 2. Live CLI Granulefile Checking (`granule granulefile check`)
Build the `granule` binary and test validation across positive and negative cases:
```bash
go build -o /tmp/granule ./cmd/granule

# Positive test: Pure Dockerfile
cat << 'EOF' > /tmp/Dockerfile.pure
FROM alpine:latest
RUN echo "hello container" > /test.txt
CMD ["cat", "/test.txt"]
EOF
/tmp/granule granulefile check -f /tmp/Dockerfile.pure

# Positive test: Multi-stage VM WRAP with Volume
cat << 'EOF' > /tmp/Granulefile.vmwrap
FROM alpine:latest AS builder
RUN apk add --no-cache gcc
VM WRAP builder AS runtime_vm
VOLUME ["/data"]
CMD ["/bin/sh"]
EOF
/tmp/granule granulefile check -f /tmp/Granulefile.vmwrap

# Positive test: Offline VM PACK
cat << 'EOF' > /tmp/Granulefile.vmpack
FROM granule/alpine.rootfs:latest
RUN apk add --no-cache redis
VM PACK AS redis_vm
EOF
/tmp/granule granulefile check -f /tmp/Granulefile.vmpack
```

### 3. Pipeline Generation & Transformation (`granule granulefile build -print`)
Verify that the generated multi-stage Dockerfile pipeline matches the specification:
```bash
/tmp/granule granulefile build -f /tmp/Granulefile.vmwrap -print
/tmp/granule granulefile build -f /tmp/Granulefile.vmpack -print
```
- Verify base image resolution, intermediate stage naming, and auxiliary copy instructions.

### 4. Negative Semantic Validation Checks
Test that the compiler correctly rejects malformed or unbootable specifications:
```bash
# Negative test: Malformed instruction or missing kernel
cat << 'EOF' > /tmp/Granulefile.invalid
VM FROM scratch
RUN echo "no kernel"
EOF
if /tmp/granule granulefile check -f /tmp/Granulefile.invalid 2>/dev/null; then
  echo "ERROR: Compiler accepted unbootable stage without error!"
  exit 1
else
  echo "PASS: Compiler properly rejected unbootable stage"
fi
```

## Evaluation & Reporting Criteria
1. **Conformance Report**: Confirm compliance with `docs/granulefile.md` §12 implementation status.
2. **Grammar & Lexer Stability**: Report any parser errors, line number misattributions, or unexpected panic handling.
3. **Pipeline Verification**: Confirm that `-print` outputs valid, syntactically correct multi-stage Dockerfiles.
4. **Issue Tracking**: Detail any newly introduced regressions in Granulefile parsing or code generation.
