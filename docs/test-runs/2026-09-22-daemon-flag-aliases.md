# Walkthrough: Daemon legacy flag aliases and migration (docs/test-plans/2026-09-22.md §5)

## Result

Pass. The end-to-end CLI walkthrough and automated suite validated all legacy `-kontur-*` flag aliases (`-kontur-sandboxes`, `-kontur-workspace`, `-kontur-base-port`, `-kontur-ssh-user`, `-kontur-net`, `-kontur-git-proxy-host`, `-kontur-create-arg`, `-kontur-state-dir`, `-kontur-exec-key`). Startup deprecation notices were logged as expected, configuration mapping bound seamlessly to internal granule sandbox runners and storage paths without panic or unintended fallback to host sandboxes, and canonical `-granule-*` flag precedence was verified across all configurations.

The walkthrough tested commit `32695c9c6` (`Merge pull request #1828 from bwsalmon/grain/task-315`) on branch `grain/task-322`.

## Environment and method

- Debian GNU/Linux 12 (bookworm), Linux 6.1.0-50-amd64 x86_64.
- Go 1.26.6.
- Grain binary compiled from source: `go build -o /tmp/grain ./cmd/grain`.
- Isolated temporary execution directories for data stores, state files, and secrets keys.
- Real flag parsing and runtime daemon startup loops tested using isolated subprocesses and unit/integration test suites in `cmd/grain/`.

## CLI execution and validation scenarios

| Scenario | CLI Execution / Flags | Observed Behavior | Outcome |
| --- | --- | --- | --- |
| 1. Full Legacy Suite | `grain daemon -kontur-sandboxes -kontur-ssh-user debian -kontur-workspace /home/debian/work -kontur-net nat -kontur-git-proxy-host 172.17.0.1 -kontur-create-arg -disk -kontur-create-arg /tmp/disk.img -kontur-base-port 30080 -kontur-state-dir /tmp/custom-granule -kontur-exec-key /tmp/key` | Explicit deprecation notices emitted on stderr for all 9 legacy flags. Runtime initialized Granule sandbox backend without fallback to host runners. | Pass |
| 2. Canonical Precedence & Merging | `grain daemon -kontur-sandboxes=false -granule-sandboxes=true -kontur-ssh-user legacy-user -granule-ssh-user canonical-user -kontur-workspace /legacy/work -granule-workspace /canonical/work -kontur-net nat -granule-net flat -kontur-git-proxy-host 10.0.0.1 -granule-git-proxy-host 172.17.0.1 -kontur-create-arg -legacy -granule-create-arg -canonical -kontur-base-port 1111 -granule-base-port 2222 -kontur-state-dir /legacy/state -granule-state-dir /canonical/state -kontur-exec-key /legacy/key -granule-exec-key /canonical/key` | Canonical `-granule-*` flags override legacy `-kontur-*` settings for single-value flags; repeatable `-kontur-create-arg` entries prepended to `-granule-create-arg`. | Pass |
| 3. Flag Discovery & Introspection | `grain daemon -flag-names` | All 9 legacy `-kontur-*` flags (`kontur-sandboxes`, `kontur-ssh-user`, `kontur-workspace`, `kontur-net`, `kontur-git-proxy-host`, `kontur-create-arg`, `kontur-base-port`, `kontur-state-dir`, `kontur-exec-key`) discoverable with types. | Pass |
| 4. Validation Error Handling | `grain daemon -kontur-sandboxes` | Incomplete granule settings properly fail startup via `checkConfig` with actionable messages referencing `-granule-*` requirements without panics. | Pass |
| 5. Runtime VM Instantiation | `grain daemon` with fake `granulectl` and seeded dispatch | Reconciler triggers VM creation using legacy flags, constructing arguments with mapped `-state-dir`, `-kontur-image`, and create args. | Pass |

## Detailed execution traces and logs

### 1. Full Legacy Suite Invocation

Executing `grain daemon` with all legacy `-kontur-*` flags:

```bash
/tmp/grain daemon \
  -data-dir /tmp/kontur-test-data \
  -kontur-sandboxes \
  -kontur-ssh-user debian \
  -kontur-workspace /home/debian/work \
  -kontur-net nat \
  -kontur-git-proxy-host 172.17.0.1 \
  -kontur-create-arg -disk \
  -kontur-create-arg /tmp/disk.img \
  -kontur-base-port 30080 \
  -kontur-state-dir /tmp/custom-granule \
  -kontur-exec-key /tmp/key
```

Logged standard error startup output:
```text
2026/09/22 19:11:12 grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=true
2026/09/22 19:11:12 grain daemon: -kontur-state-dir is legacy alias for -granule-state-dir; using -granule-state-dir=/tmp/custom-granule
2026/09/22 19:11:12 grain daemon: -kontur-ssh-user is legacy alias for -granule-ssh-user; using -granule-ssh-user=debian
2026/09/22 19:11:12 grain daemon: -kontur-exec-key is legacy alias for -granule-exec-key; using -granule-exec-key=/tmp/key
2026/09/22 19:11:12 grain daemon: -kontur-workspace is legacy alias for -granule-workspace; using -granule-workspace=/home/debian/work
2026/09/22 19:11:12 grain daemon: -kontur-create-arg is legacy alias for -granule-create-arg; forwarded 2 argument(s)
2026/09/22 19:11:12 grain daemon: -kontur-net is legacy alias for -granule-net; using -granule-net=nat
2026/09/22 19:11:12 grain daemon: -kontur-base-port is legacy alias for -granule-base-port; using -granule-base-port=30080
2026/09/22 19:11:12 grain daemon: -kontur-git-proxy-host is legacy alias for -granule-git-proxy-host; using -granule-git-proxy-host=172.17.0.1
2026/09/22 19:11:13 grain: generated a new secrets key at /tmp/kontur-test-data/secrets/secrets.key -- back this file up: without it, /tmp/kontur-test-data/secrets/secrets.enc cannot be decrypted by anyone, grain included
2026/09/22 19:11:13 grain daemon: sandbox image ghcr.io/bwsalmon/grain/guest:latest (stamped into this build; nothing pinned one)
...
2026/09/22 19:11:13 grain daemon: grain CI is on, 1 job(s) at a time alongside up to 2 ordinary agent run(s) plus 1 Director-only bypass -- 4 sandbox(es) when all are full -- for repos carrying the "grain-ci" capability
```

### 2. Mixed Canonical and Legacy Flags (Precedence Verification)

Executing `grain daemon` with conflicting canonical and legacy flags:

```bash
/tmp/grain daemon \
  -data-dir /tmp/kontur-mixed-data \
  -kontur-sandboxes=false \
  -granule-sandboxes=true \
  -kontur-ssh-user legacy-user \
  -granule-ssh-user canonical-user \
  -kontur-workspace /legacy/work \
  -granule-workspace /canonical/work \
  -kontur-net nat \
  -granule-net flat \
  -kontur-git-proxy-host 10.0.0.1 \
  -granule-git-proxy-host 172.17.0.1 \
  -kontur-create-arg -legacy \
  -granule-create-arg -canonical \
  -kontur-base-port 1111 \
  -granule-base-port 2222 \
  -kontur-state-dir /legacy/state \
  -granule-state-dir /canonical/state \
  -kontur-exec-key /legacy/key \
  -granule-exec-key /canonical/key
```

Logged standard error startup output:
```text
2026/09/22 19:11:24 grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=true
2026/09/22 19:11:24 grain daemon: -kontur-state-dir is legacy alias for -granule-state-dir; using -granule-state-dir=/canonical/state
2026/09/22 19:11:24 grain daemon: -kontur-ssh-user is legacy alias for -granule-ssh-user; using -granule-ssh-user=canonical-user
2026/09/22 19:11:24 grain daemon: -kontur-exec-key is legacy alias for -granule-exec-key; using -granule-exec-key=/canonical/key
2026/09/22 19:11:24 grain daemon: -kontur-workspace is legacy alias for -granule-workspace; using -granule-workspace=/canonical/work
2026/09/22 19:11:24 grain daemon: -kontur-create-arg is legacy alias for -granule-create-arg; forwarded 1 argument(s)
2026/09/22 19:11:24 grain daemon: -kontur-net is legacy alias for -granule-net; using -granule-net=flat
2026/09/22 19:11:24 grain daemon: -kontur-base-port is legacy alias for -granule-base-port; using -granule-base-port=2222
2026/09/22 19:11:24 grain daemon: -kontur-git-proxy-host is legacy alias for -granule-git-proxy-host; using -granule-git-proxy-host=172.17.0.1
```

### 3. Flag Discovery via `-flag-names`

Executing `grain daemon -flag-names`:

```text
kontur-base-port value
kontur-create-arg value
kontur-exec-key value
kontur-git-proxy-host value
kontur-net value
kontur-sandboxes bool
kontur-ssh-user value
kontur-state-dir value
kontur-workspace value
```

### 4. Incremental Validation Failures

Executing `grain daemon` with missing required granule parameters:

```bash
/tmp/grain daemon -data-dir /tmp/walkthrough-kontur-incomplete -kontur-sandboxes
# Output:
# 2026/09/22 19:15:00 grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=true
# grain daemon: -granule-ssh-user is required with -granule-sandboxes

/tmp/grain daemon -data-dir /tmp/walkthrough-kontur-incomplete -kontur-sandboxes -kontur-ssh-user debian
# Output:
# 2026/09/22 19:15:00 grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=true
# 2026/09/22 19:15:00 grain daemon: -kontur-ssh-user is legacy alias for -granule-ssh-user; using -granule-ssh-user=debian
# grain daemon: -granule-workspace is required with -granule-sandboxes

/tmp/grain daemon -data-dir /tmp/walkthrough-kontur-incomplete -kontur-sandboxes -kontur-ssh-user debian -kontur-workspace /home/debian/work
# Output:
# 2026/09/22 19:15:00 grain daemon: -kontur-sandboxes is legacy alias for -granule-sandboxes; using -granule-sandboxes=true
# 2026/09/22 19:15:00 grain daemon: -kontur-ssh-user is legacy alias for -granule-ssh-user; using -granule-ssh-user=debian
# 2026/09/22 19:15:00 grain daemon: -kontur-workspace is legacy alias for -granule-workspace; using -granule-workspace=/home/debian/work
# grain daemon: -granule-git-proxy-host is required with -granule-sandboxes
```

## Verification

1. **Automated Unit & Integration Suites**: All tests in `cmd/grain/daemon_kontur_flag_alias_test.go` pass (100% pass rate):
   - `TestKonturFlagAliasesBindToGranuleConfig`
   - `TestCanonicalGranuleFlagsPrecedeKonturAliases`
   - `TestKonturFlagsIncludedInFlagNames`
   - `TestKonturFlagAliasesDeprecationLogging`
   - `TestKonturSandboxesSelectsGranuleBackendWithoutHostFallback`
   - `TestKonturSandboxesMissingRequiredFieldsRefused`
2. **Package Test Suite**: Full `go test ./cmd/grain/...` suite runs and passes cleanly.
3. **No Unintended Fallbacks or Panics**: All legacy configurations safely bind to the Granule sandbox backend (`model.SandboxBackendGranule`) with complete deprecation visibility.
