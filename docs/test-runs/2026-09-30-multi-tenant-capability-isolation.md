# Multi-Tenant Capability Security, Subprocess Isolation & Symlink Containment Walkthrough Report

**Date**: 2026-09-30  
**Track**: Security, Multi-Tenant Capability Isolation & Sandbox Containment  
**Test Plan Reference**: `docs/test-plans/2026-09-30.md §4`  
**Test Suite References**:  
- `tests/e2e/multitenant_capability_security_walkthrough_test.go`  
- `pkg/mcp/sandbox_tools_test.go`  
- `pkg/mcp/sandbox_env_test.go`  
- `pkg/mcp/task_tools_test.go`  
- `cmd/grain/mcpserver_selfdebug_test.go`  
- `pkg/ui/console_test.go`  
- `pkg/capability/selfrepair/selfrepair_test.go`  
- `pkg/capability/packagepublish/packagepublish_test.go`  
- `pkg/capability/gcpkey/gcpkey_test.go`  
- `pkg/capability/console/console_test.go`  
- `pkg/capability/githubtoken/githubtoken_test.go`  
- `pkg/capability/quarantine/quarantine_test.go`  
**Runner / Environment**: Antigravity Sandbox (`Debian bookworm`, `Go 1.26.6`, Linux 6.6)

---

## 1. Executive Summary & Verification Matrix

This document records the end-to-end verification and security audit of **Multi-Tenant Capability Security, Subprocess Environment Isolation & Symlink Escape Containment** as specified in `docs/test-plans/2026-09-30.md §4`.

All six core verification steps passed with complete enforcement across the orchestrator, capability resolution engine, sandbox MCP server, task debugging inspection API, and interactive console subsystem.

### Verification Matrix

| Step | Verification Target | Expected Behavior | Actual Behavior | Result |
| :--- | :--- | :--- | :--- | :--- |
| **Step 1** | **Multi-Team Capability Allowlists** | Privileged capability providers (`package-publish`, `self-repair`, `self-upgrade`, `gcp-key`, `console`, `github-token`, `test-quarantine`) configure `AuthorizedTeams` allowlists. | Providers expose `AuthorizedTeams: ["team-alpha"]` in `model.CapabilitySpec` and enforce `SupportsTeam()`. | **PASS** |
| **Step 2** | **Unauthorized Team Capability Refusal** | Dispatched task from unauthorized team (`team-beta`) requesting restricted capability fails closed with `TeamUnauthorizedRefusal` and records an audit explanation. | Orchestrator/`model.ResolveGrants` refuses capability, logs audit trail, records comment in task conversation, and parks task. | **PASS** |
| **Step 3** | **Symlink Path Containment** | Symlinks pointing outside workspace root (`symlink_out -> /outside`) are blocked on `read_file`, `write_file`, and `edit_file` via `filepath.EvalSymlinks`. | All symlink escape attempts (directory symlinks, direct file symlinks, broken symlinks, nested paths) are rejected with path escape errors. | **PASS** |
| **Step 4** | **Subprocess Environment Sanitization** | `run_command` strips sensitive host variables (`GRAIN_TOKEN`, `GRAIN_DB_URL`, `GCP_SERVICE_ACCOUNT_KEY`, SSH keys); preserves safe env vars and pins `HOME`. | Subprocess `env` output contains no daemon credentials; `HOME` is pinned to sandbox workspace; only allowlisted variables pass through. | **PASS** |
| **Step 5** | **Self-Debug Multi-Tenant Scoping** | Self-debug task tools (`list_grain_tasks`, `read_grain_task`, `read_grain_task_prompt`, `read_grain_task_transcript`) enforce team partition. | Team Beta caller cannot list or inspect Team Alpha tasks/prompts/transcripts; explicit refusal string returned on cross-team query. | **PASS** |
| **Step 6** | **Interactive Console Stream RBAC** | Unauthorized interactive console streaming (`GET/POST /api/console/{task_id}/*`) returns `403 Forbidden`. | Anonymous and cross-team users are rejected with `403 Forbidden`; authorized team members and system admins connect successfully. | **PASS** |

---

## 2. Architectural Security Controls & Data Flow

### 2.1 Capability Authorization & Resolution Pipeline

```
                     Task Dispatched with Requested Capabilities
                                          │
                                          ▼
                         Capability Provider Lookups in Registry
                                          │
                                          ▼
                   For each Grant g in Task.Grants:
                   Is Provider.Spec().SupportsTeam(Task.Team)?
                                    │           │
                               (Yes)│           │(No)
                                    ▼           ▼
                           Grant Honoured   Grant Refused:
                           (Proceed to       TeamUnauthorizedRefusal
                            Materialize)    (Fail Closed & Park Task)
                                                        │
                                                        ▼
                                            Log Audit Record & Post
                                            Task Explanation Comment
```

1. **Strict Allowlisting**: Each privileged provider implements `AuthorizedTeams []string` in its configuration.
2. **Deterministic Refusal**: If a task's team is not present in `AuthorizedTeams`, `model.ResolveGrants` generates a human-readable `TeamUnauthorizedRefusal`:
   ```go
   func TeamUnauthorizedRefusal(capability, team string, authorized []string) string {
       return fmt.Sprintf("Capability %q is restricted to authorized teams (%s); team %q is not authorized.",
           capability, strings.Join(authorized, ", "), team)
   }
   ```
3. **Fail-Closed Execution**: `orchestrator.RunDispatch` halts before any sandbox creation or credential minting, invoking `noteRefusedGrant` to append an audit explanation and park the task until authorized.

---

### 2.2 Symlink Path Containment Architecture

Inside `pkg/mcp/sandbox_tools.go`, all filesystem operations (`read_file`, `write_file`, `edit_file`) evaluate paths against the sandbox root before performing any I/O:

```go
func checkSymlinks(rootClean, realRoot, full string) error {
    resolved, err := filepath.EvalSymlinks(full)
    if err == nil {
        if !isInside(resolved, realRoot) && !isInside(resolved, rootClean) {
            return fmt.Errorf("symlink escapes root: %s -> %s", full, resolved)
        }
        return nil
    }
    // Handle non-existent or broken target symlinks by checking ancestor components
    return evalExistingOrBroken(rootClean, realRoot, full)
}
```

- **Existing Files & Targets**: Evaluates target canonical path using `filepath.EvalSymlinks(full)`.
- **Broken / Non-Existent Targets**: Evaluates the longest existing ancestor component to prevent directory-symlink escapes before file creation.
- **Error Formatting**: Path escapes outside the workspace root emit user-friendly errors directing the agent to use workspace-relative paths.

---

### 2.3 Subprocess Environment Sanitization

Subprocesses invoked via `run_command` in `pkg/mcp/sandbox_tools.go` use `sandboxEnv(root)` and `sandboxPassthrough`:

```go
var sandboxPassthrough = map[string]bool{
    "PATH":             true,
    "USER":             true,
    "LOGNAME":          true,
    "SHELL":            true,
    "TERM":             true,
    "LANG":             true,
    "LC_ALL":           true,
    "LC_CTYPE":         true,
    "TMPDIR":           true,
    "GOMODCACHE":       true,
    "GOCACHE":          true,
    "npm_config_cache": true,
}
```

- **Credential Stripping**: Host variables like `GRAIN_TOKEN`, `GRAIN_DB_URL`, `GRAIN_AUTH_TOKEN`, `GCP_SERVICE_ACCOUNT_KEY`, `AWS_SECRET_ACCESS_KEY`, and `SSH_AUTH_SOCK` are omitted from subprocess execution.
- **Pinned Workspace**: `HOME` is pinned to `root` (the sandbox workspace).

---

### 2.4 Self-Debug Tool Scoping & Console RBAC

- **Task Tools Scoping (`pkg/mcp/task_tools.go`)**:
  - `TaskScope` evaluates `permitsTeam(task.Team)`:
    - If `scope.CallerTeam != "" && task.Team != scope.CallerTeam`, `list_grain_tasks` filters the task out.
    - `read_grain_task`, `read_grain_task_prompt`, and `read_grain_task_transcript` return explicit cross-team refusals:
      `"Task <id> belongs to team <team>, and this run belongs to team <caller_team> -- so this is a refusal, not a task that does not exist."`
- **Interactive Console RBAC (`pkg/ui/console.go`)**:
  - Console streaming endpoints (`GET /api/console/{task_id}/output`, `POST /api/console/{task_id}/input`, etc.) extract the acting user from authentication headers (`X-User-LDAP`).
  - Access is verified via `store.IsAdmin(ctx, user)` or `store.UserInTeam(ctx, user, task.Team)`.
  - Non-members are rejected immediately with HTTP `403 Forbidden`.

---

## 3. Detailed Walkthrough Execution & Results

The complete walkthrough was executed in `tests/e2e/multitenant_capability_security_walkthrough_test.go` under `TestMultiTenantCapabilitySecurityWalkthrough`.

### Step 1: Multi-Team Initialization with AuthorizedTeams

- Configured teams:
  - `team-alpha`: Authorized Platform & Core team (`alice@example.com`).
  - `team-beta`: Restricted Application Dev team (`bob@example.com`).
  - System Admin: `admin-user@example.com`.
- Capability Providers Registered with `AuthorizedTeams: ["team-alpha"]`:
  - `self-repair`, `package-publish`, `self-upgrade`, `gcp-key`, `console`, `github-credential:github-token`, `test-quarantine`.
- Verified `SupportsTeam("team-alpha") == true` and `SupportsTeam("team-beta") == false`.

### Step 2: Unauthorized Capability Request & Fail-Closed Dispatch

- Dispatched task `task-beta-unauth-001` (`team-beta`) requesting `package-publish`.
- `model.ResolveGrants` evaluated capability and returned `Resolution{Refused: true}`.
- Refusal reason verified:
  `"Capability \"package-publish\" is restricted to authorized teams (team-alpha); team \"team-beta\" is not authorized."`
- Authorized task `task-alpha-auth-001` (`team-alpha`) requesting `self-repair` was resolved and honoured successfully.

### Step 3: Symlink Path Traversal Containment

- Set up sandbox workspace with outside secret directory `/outside/host_secret.txt`.
- Created traversal symlinks:
  - Directory symlink: `workspace/symlink_out -> /outside`
  - File symlink: `workspace/secret_link.txt -> /outside/host_secret.txt`
  - Broken ancestor symlink: `workspace/broken_link -> /outside/nonexistent`
- Tested MCP tool actions:
  - `read_file(symlink_out/host_secret.txt)`: **BLOCKED** with path escape refusal.
  - `read_file(secret_link.txt)`: **BLOCKED** with path escape refusal.
  - `write_file(symlink_out/evil_write.txt)`: **BLOCKED**, no file created outside root.
  - `edit_file(secret_link.txt)`: **BLOCKED**.
  - `write_file(broken_link/escaped.txt)`: **BLOCKED**.
  - `write_file(valid.txt)` inside workspace: **SUCCESS**.

### Step 4: Subprocess Environment Sanitization

- Test host process populated with fake sensitive tokens (`GRAIN_TOKEN`, `GRAIN_DB_URL`, `GCP_SERVICE_ACCOUNT_KEY`, `SSH_AUTH_SOCK`, `AWS_SECRET_ACCESS_KEY`).
- Executed `run_command(command="env")`.
- Output Inspection:
  - Zero sensitive host credentials exposed in subprocess output.
  - `HOME` verified pinned to sandbox workspace directory.
  - Standard variables (`PATH`, `USER`, `LOGNAME`, `TMPDIR`, compiler caches) preserved.

### Step 5: Multi-Tenant Scoping on Self-Debug Tools

- Created confidential task `task-alpha-debug` in `team-alpha` and open task `task-beta-debug` in `team-beta`.
- Scoped MCP TaskTools to `team-beta`:
  - `list_grain_tasks`: Returned `task-beta-debug`; completely omitted `task-alpha-debug`.
  - `read_grain_task(task-alpha-debug)`: **REFUSED** with cross-team refusal error.
  - `read_grain_task_prompt(task-alpha-debug)`: **REFUSED**.
  - `read_grain_task_transcript(task-alpha-debug)`: **REFUSED**.
  - `read_grain_task(task-beta-debug)`: **SUCCESS**.

### Step 6: Interactive Console Stream RBAC & 403 Forbidden Rejection

- Created interactive task `task-alpha-interactive` in `team-alpha` with active console stream.
- Test endpoints against UI server:
  - Anonymous GET `/api/console/task-alpha-interactive/output`: **403 Forbidden** (`authentication required`).
  - Cross-team user (`X-User-LDAP: bob@example.com` from `team-beta`) GET `/output`: **403 Forbidden** (`permission denied`).
  - Cross-team user POST `/input`: **403 Forbidden**.
  - Authorized team member (`X-User-LDAP: alice@example.com` from `team-alpha`) GET `/output`: **200 OK**.
  - System Administrator (`X-User-LDAP: admin-user@example.com`) GET `/output`: **200 OK**.

---

## 4. Test Execution Output

```
=== RUN   TestMultiTenantCapabilitySecurityWalkthrough
    multitenant_capability_security_walkthrough_test.go:50: === Step 1: Multi-Team Environment Setup with AuthorizedTeams Allowlists ===
    multitenant_capability_security_walkthrough_test.go:117: === Step 2: Verify Unauthorized Team Capability Refusal & Fail-Closed Dispatch ===
    multitenant_capability_security_walkthrough_test.go:180: === Step 3: Workspace Symlink Escape Containment via filepath.EvalSymlinks ===
    multitenant_capability_security_walkthrough_test.go:288: === Step 4: Subprocess Environment Sanitization & Host Credential Stripping ===
    multitenant_capability_security_walkthrough_test.go:334: === Step 5: Multi-Tenant Scoping on Self-Debug Task Inspection MCP Tools ===
    multitenant_capability_security_walkthrough_test.go:432: === Step 6: Interactive Console Stream RBAC & 403 Forbidden on Unauthorized Access ===
    multitenant_capability_security_walkthrough_test.go:512: === ALL MULTI-TENANT CAPABILITY SECURITY & ISOLATION STEPS PASSED SUCCESSFULLY ===
--- PASS: TestMultiTenantCapabilitySecurityWalkthrough (0.07s)
PASS
ok  	github.com/ai-on-gke/grain/tests/e2e	0.081s
```

---

## 5. Conclusion & Verification Sign-Off

The multi-tenant capability security, subprocess isolation, and symlink containment controls are fully verified, robust, and free of regressions. All automated tests across `tests/e2e/`, `pkg/...`, and `cmd/...` pass consistently.
