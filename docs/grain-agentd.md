# grain-agentd: agent CLIs on a machine that is not the daemon's

`grain-agentd` is a small, rootless grain hostd that runs **agent CLIs and
nothing else** (agy, claude, codex) for a grain daemon somewhere else: a
workstation whose agy login a model connection should use, say, while the
daemon runs on a server or in Kubernetes. It runs as the user who starts it,
dials out to the daemon, and starts the CLIs the daemon asks for, as that
user, under its allowlist.

It is `grain hostd -server` cut down to that one job (`cmd/grain-agentd`,
`pkg/agentd`):

| | `grain hostd` | `grain-agentd` |
| --- | --- | --- |
| Runs as | root, or the daemon's user | the user who starts it; refuses root |
| Capability servers | yes (`capabilities:`, built-ins) | none: an allowlist naming any is refused |
| Allowlist owner | root | you or root (not group- or world-writable) |
| Paths | `/etc/grain`, `/run/grain-hostd` | under your home and `$XDG_RUNTIME_DIR` |
| Agent hooks | `grain agy-tool-hook`, `grain claude-stop-hook` | the same subcommands, on its own binary |
| Size | all of grain | `pkg/hostd` and three leaf packages |

A user-owned allowlist is weaker than a root-owned one: anything running as
you can edit it. That is acceptable only because this allowlist can name
agent CLIs and nothing else, and an agent CLI already runs as you with your
shell.

## Build it

```sh
make grain-agentd        # bin/grain-agentd, static
```

`make build` does not build it, and no release ships it yet.

## Register it

On the daemon, register a hostd for the machine and keep its token: **Settings
→ Hostds**, or

```sh
grain hostds register ws-1                       # a daemon reached directly
grain hostds register -owner you@example.com ws-1   # a daemon behind IAP
```

A hostd that dials in through IAP or another identity-verifying proxy must be
registered with its owner, the identity it dials in as; see
[access-code.md](access-code.md#a-grain-hostd-dialing-in).

Then point a model connection at it (Settings → Models → the connection →
**Run on** `ws-1`); see [hostd-placement.md](hostd-placement.md).

## Install it

As yourself, on the machine the CLIs should run on:

```sh
GRAIN_HOSTD_TOKEN=<token> ./bin/grain-agentd install -server https://grain.example.com -name ws-1
```

With no `$GRAIN_HOSTD_TOKEN` it asks for the token on the terminal without
echoing it (and puts the terminal back if you interrupt it). `install`:

1. copies the binary to `~/.local/bin/grain-agentd`;
2. writes the token to `~/.config/grain/agentd-token` (mode 0600, in a 0700
   directory), or keeps the one already there;
3. writes `~/.config/grain/agentd.yaml` with an `agy` entry -- agy found on
   `$PATH`, else `~/.local/bin/agy` -- confined to the staging root a remote
   run uses (`~/.grain/hostd-stage`), unless an allowlist is already there, and
   checks it is agents-only;
4. writes `~/.config/systemd/user/grain-agentd.service`;
5. enables and starts it with `systemctl --user`, and enables lingering
   (`loginctl enable-linger`) so it keeps running after you log out, where
   that is allowed.

`-no-start` stops after step 4. Watch it connect with

```sh
journalctl --user -u grain-agentd -f     # look for "connected to the controller"
```

Running `install` again replaces the binary and the unit and keeps the token
(unless `$GRAIN_HOSTD_TOKEN` is set) and the allowlist.

### A daemon reached directly

`-server` is the daemon's URL as this machine reaches it --
`https://grain.example.com`, or `http://127.0.0.1:4242` through a
`kubectl port-forward` or an SSH tunnel. The hostd token alone names the
hostd.

### A daemon behind IAP

Add the IAP OAuth client ID:

```sh
grain-agentd install -server https://grain.example.com -name ws-1 \
  -iap-audience 1234567890-abc.apps.googleusercontent.com
```

grain-agentd then sends an ID token for that audience from your Application
Default Credentials (`gcloud auth application-default login`) or the
machine's service account, with the hostd token beside it, exactly as
`grain hostd -server -iap-audience` does. The identity must be the hostd's
registered owner.

## Run it by hand

Without systemd, run what the unit runs, e.g. in tmux:

```sh
grain-agentd -server https://grain.example.com -name ws-1 \
  [-iap-audience <client id>] [-token-file ~/.config/grain/agentd-token] \
  [-allowlist ~/.config/grain/agentd.yaml] [-state-dir ~/.local/state/grain-agentd]
```

- `-check` checks the allowlist and prints the agent CLIs it offers.
- `-build` prints the binary's build.
- `-socket` is the local socket it also listens on
  (`$XDG_RUNTIME_DIR/grain-agentd/agentd.sock`, mode 0600).

## The allowlist

The same format as `grain hostd`'s, with `clis:` only:

```yaml
clis:
  agy:
    path: /home/you/.local/bin/agy
    paths_under: [/home/you/.grain/hostd-stage]
    cwd_under: [/home/you/.grain/hostd-stage]
    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]
    pass_env: [HTTP_PROXY, HTTPS_PROXY, NO_PROXY, http_proxy, https_proxy, no_proxy]
capabilities: {}
```

grain-agentd refuses to start on an allowlist that names a capability, has no
CLI, is missing, or is writable by anyone but its owner. hostd re-reads it on
every request.

## Hooks

A run's agent CLI forks hooks -- agy's pre-tool hook (`agy-tool-hook`) and
Claude Code's Stop hook (`claude-stop-hook`) -- and a run on a remote hostd
names that hostd's own binary for them. grain-agentd answers both, from the
leaf packages `pkg/agent/antigravity/hook` and `pkg/agent/claude/stophook`
that `cmd/grain` uses too, so the hook decides the same way wherever the CLI
runs.

## For a build that extends it

`pkg/agentd` links nothing of the daemon's: no `pkg/ui`, `pkg/daemon`,
`pkg/model`, `pkg/mcp` or store package (`TestDependencyFence`). A build adds
to it from `init()` in a main of its own that calls `agentd.Main(os.Args)`
(the Makefile's `MAIN_grain-agentd`):

- **A transport** -- another way to reach a daemon, such as a corporate
  access proxy core grain knows nothing about:

  ```go
  agentd.RegisterTransport(agentd.Transport{
      Name:  "my-proxy",
      Match: func(u *url.URL) bool { return strings.HasSuffix(u.Hostname(), ".proxy.example") },
      Flags: func(fs *flag.FlagSet) { fs.StringVar(&helper, "my-proxy-helper", "", "...") },
      Dial: func(ctx context.Context, cfg agentd.DialConfig) (func(context.Context) (net.Conn, error), func(), error) {
          // start what the proxy needs; return how to open one session stream
      },
      Prepare: func(ctx context.Context, cfg agentd.InstallConfig) ([]string, error) {
          // fetch or build the helper at install time; return flags for the unit
      },
  })
  ```

  The first transport whose `Match` takes `-server` is used for it; any other
  URL is dialed directly or through IAP as above.
- **Host-tool grants** for the agy hook: `hook.RegisterHostToolGrant` (the
  registry `antigravity.RegisterHostToolGrant` writes to). The binary that
  answers the hook must link the same grants as the daemon that wrote the
  run's hook arguments.
- **Where agy lives**, when not on `$PATH`: `agentd.RegisterAgyPath`.
