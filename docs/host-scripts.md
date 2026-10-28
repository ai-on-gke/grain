# Host scripts: a how-to for operators

The `host-script` capability server runs bash scripts that you, the operator, write. They run on the host as you, or in the daemon's container. A task granted one calls it in its sandbox with the `grain` CLI, `grain host-script <name> [args...]`, which runs the script on grain's side with the arguments it was called with. The reference is [`docs/capabilities.md`](capabilities.md#host-scripts-grain-capability-host-script). This page walks through adding one.

## 1. Add the server

In **Settings → Capabilities → Capability servers**, add a stdio server:

```json
"https://github.com/bwsalmon/grain/capabilities/host-script": {
  "command": "grain",
  "args": ["capability", "host-script"],
  "env": { "DEPLOY_TOKEN": "$secret:deploy-token" }
}
```

- **Run on the host as you** with `"run": "host"` and `"command": "host-script"`, which is the name of an entry in grain hostd's allowlist, `/etc/grain/hostd.yaml`, and no `args`:

  ```yaml
  capabilities:
    host-script:
      command: /usr/local/bin/grain        # wherever grain is on the host
      args: [capability, host-script]
      env: [DEPLOY_TOKEN]                  # the names the entry's env may set
      pass_env: [KUBECONFIG, SSH_AUTH_SOCK, CLOUDSDK_CONFIG]
  ```

  Scripts then run with your ambient identity: your uid, plus the `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG` and `TZ` hostd passes to every entry. Anything else of yours a script needs, such as `KUBECONFIG` or `SSH_AUTH_SOCK`, must be listed in `pass_env`.

  Scripts also run at `oom_score_adj` +500. `grain-hostd.service` itself runs at `OOMScoreAdjust=-900`, as the daemon does, so that under memory pressure the kernel kills the work and not the controller ([design/auto-overcommit.md](design/auto-overcommit.md)).
- **Run in the daemon's container** by leaving `run` unset. Scripts then run with the container's identity.

The server listens on `127.0.0.1:9733` (`-listen` to change it). It keeps each grant's private script file, its scratch directory and `audit.jsonl` under `-data-dir`, which defaults to `~/.cache/grain/host-script`.

## 2. Write a script

Open the server's **Configure** form and add an entry to **scripts**: each is a card of its own, with `usage` and `script` as multi-line text (the script in a monospace font) and `env` as name/value rows, where a value can be `$env:NAME`. A script that deploys one service to one environment, and parses its own flags:

| Field | Value |
|---|---|
| `name` | `deploy` |
| `description` | `Deploy one service to staging or prod.` |
| `usage` | `deploy --env staging\|prod --service NAME [--dry-run]` |
| `timeout` | `600` |
| `env` | `{"DEPLOY_TOKEN": "$env:DEPLOY_TOKEN", "REGISTRY": "europe-docker.pkg.dev/acme/images"}` |
| `max_calls` | `20` |

`script`:

```bash
set -euo pipefail

usage() { echo "usage: deploy --env staging|prod --service NAME [--dry-run]" >&2; exit 2; }

env="" service="" dry_run=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --env)      [ "$#" -ge 2 ] || usage; env=$2; shift 2 ;;
    --service)  [ "$#" -ge 2 ] || usage; service=$2; shift 2 ;;
    --dry-run)  dry_run=1; shift ;;
    -h|--help)  usage ;;
    *)          echo "deploy: unknown argument: $1" >&2; usage ;;
  esac
done

# Arguments come from the sandbox and are untrusted: validate every one
# against an allowlist before using it.
case "$env" in staging|prod) ;; *) echo "deploy: --env must be staging or prod" >&2; exit 2 ;; esac
[[ "$service" =~ ^[a-z][a-z0-9-]{0,40}$ ]] || { echo "deploy: bad --service" >&2; exit 2; }

echo "deploying $service to $env (task $GRAIN_TASK_ID)"
if [ "$dry_run" = 1 ]; then
  echo "dry run: would deploy $REGISTRY/$service:latest"
  exit 0
fi
curl -fsS -H "Authorization: Bearer $DEPLOY_TOKEN" \
  "https://deploy.example.com/v1/$env/$service" -d '{"image":"'"$REGISTRY/$service"':latest"}'
```

Things to know when writing one:

- **Arguments are passed straight through.** The command's argv reaches the script as `"$@"`, exactly as given: spaces, quotes, `$(…)`, newlines and a leading `--` are all literal, and grain never runs them through a shell. Your script parses and validates them. Any process in a granted sandbox can call the command with any arguments, so treat every argument as hostile. Use `"$1"`, never `eval`, and never paste an argument into another shell string.
- **Fixed values belong in the script or its `env`, not in arguments.** A grant can't add to or change the script text.
- **Secrets go in through the server's environment.** Bind them in the server entry's `env` as `$secret:<name>`, then name them in a script's `env` as `$env:<NAME>`. A script sees only its own `env`, `GRAIN_TASK_ID`, `GRAIN_ATTEMPT_ID` and the ambient identity variables (`HOME`, `USER`, `PATH`, `KUBECONFIG`, …). It never sees the rest of the server's environment.
- **Arguments are logged.** Every call's argv is in the audit log, so never ask callers to pass a secret as an argument.
- **Limits.** `timeout` (seconds) kills the script's whole process group, and the call exits 124. `max_output` caps stdout and stderr each, with a truncation marker. `max_calls` caps calls per grant. `max_args` and `max_arg_len` cap the argv, and an argument holding a NUL byte is refused.
- **Working directory.** `cwd` defaults to a scratch directory private to the grant, which is removed at release.

## 3. Try it

Use the server's **Test** button to check the configuration attaches. Once it is saved, the server's card lists each script with its description, usage and limits, and each script has a **Test** button of its own: add arguments, one field per argument, and **Run** runs the saved script once and shows its stdout, stderr, exit code and duration. Each field is passed as exactly one argument — `two words` or `$(id)` arrives as typed, never through a shell — so this is the same argv handling a sandbox gets.

A test runs in a throwaway grant the server mints and releases around the call, under the script's own limits and environment, with its `timeout` capped at a minute (the control plane answers one call at a time, so a test holds up the server's grants while it runs). Its audit record says `caller: settings-test` and an `attempt_id` of `settings-test-<hex>`, where a sandbox's says `caller: sandbox`. Behind the button is `POST /api/capability-servers/test-script` (`{id, script, argv}`), which calls the server's control-plane tool `test_script`; it tests what the server has configured now, so save a changed script before testing it.

## 4. Grant it

Grant `https://github.com/bwsalmon/grain/capabilities/host-script#host-script` with `with: {script: deploy}`. The grant form offers the configured names as a choice. A task may hold several such grants, one per script. In the sandbox:

```console
$ grain host-script --list
deploy	Deploy one service to staging or prod.
$ grain host-script deploy --grain-help
Usage: grain host-script deploy [arguments...]

Deploy one service to staging or prod.

deploy --env staging|prod --service NAME [--dry-run]
...
$ grain host-script deploy --env staging --service web --dry-run
deploying web to staging (task 5087)
dry run: would deploy europe-docker.pkg.dev/acme/images/web:latest
$ echo $?
0
```

The agent's prompt has a section for each granted script with its description and usage, and says how to call it. Repository scripts and tests can call `grain host-script deploy` too: `grain` is on the `PATH` of `run_command` and of a grain CI step, either the sandbox image's own `/usr/local/bin/grain` or, in an image built without one, the stand-in the grant's setup installs at `/home/debian/.grain/bin/grain`. Arguments are passed literally, never through a shell, and every call is logged with them, so never pass a secret as one. Output streams back as it is produced, and the exit status is the script's. Stdin is not forwarded. When the attempt ends, the grant is released and the command exits 125, saying so.
