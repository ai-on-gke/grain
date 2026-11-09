# Bootstrap: test repos (GitHub App)

Goal: register and install the GitHub App the `github-sandbox` capability
(`pkg/capability/githubsandbox`) mints per-task installation tokens
from, and confirm it can actually create and reach the test repos this
deployment targets. Unlike the two GCP playbooks, there is no master
credential to ask for here at all -- and that's deliberate, not a gap
this playbook works around. Read `cmd/grain/controller.go`'s own doc
comment if you want the full reasoning; the short version: GitHub has no
API to mint a personal access token for an account, and scripting the
login page to drive one is unreliable against any account with
two-factor authentication -- which a dedicated bot account should have.
So the one manual step in this whole flow is a human, logged into the
bot account in their own real browser, clicking the one button GitHub
itself renders. Nothing you do here should try to shortcut that.

## 1. Ask whether an App already exists

If github-connections.md already registered a GitHub App for this
deployment, reuse it -- don't register a second one. Ask before
proceeding.

## 2. Run `grain controller bootstrap-github-app`

This is the one command in this whole flow that needs a human at an
actual browser, not just a chat reply -- `run_host_command`'s
confirm-then-run-to-completion shape doesn't fit an interactive,
multi-step browser flow with its own redirect and timeout. Tell the
human to run it themselves, from a shell on this host (or anywhere with
network access to it and to github.com), logged into the bot account in
their browser:

```
grain controller bootstrap-github-app -data-dir $GRAIN_DATA_DIR
```

It opens a browser tab pre-filled with a GitHub App manifest (name,
homepage, the permission set `githubsandbox` and default git credentials need --
see `controller.go`'s own `manifestPermissions`), waits up to five
minutes for GitHub's redirect back to a local, loopback-only callback
server, and then writes the resulting App ID and private key straight
into `$GRAIN_DATA_DIR/secrets` -- the same store `grain daemon` and
`grain secrets list` read. Nothing about the bot account's password ever
reaches grain or this chat.

If they're not at a real browser on this host (SSH session with no X11,
a serial console, ...), the command still prints the manifest and the
submission URL -- they can open that URL in whatever browser they do
have, POST the printed manifest to it by hand, or run the command with
port forwarding set up so the callback can still reach the loopback
server it starts. Whichever way, wait for them to report success (or an
error) back in this chat before continuing -- don't try to poll or
retry this one yourself.

## 3. Install the App

The command's own final output names the App's settings URL and says as
much, but repeat it clearly: creating the App and installing it are two
separate GitHub actions, and the manifest flow above only does the
first. The human needs to visit that URL, choose "Install App", and
install it on the same bot account -- "All repositories" is the right
choice, since `githubsandbox` creates scratch repos on demand rather
than needing a fixed list selected ahead of time.

## 4. Confirm it mints tokens and can reach test repos

Once installed, verify with a read-only check rather than taking the
human's word for it -- ask them to open (or start) a task with the
`github-sandbox` capability and confirm it dispatches successfully; a
failure to mint a token there means the install didn't take, or is
scoped to the wrong account. (This step named `scratch-repo` until
bwsalmon/agents#612 -- v1's name for it, which v2 ships no provider
under; `github-sandbox` is the provider that actually mints the App's
tokens, and the row the capability picker now offers.) If this
deployment also has a fixed list of real target repos (`test_repos` in
`terraform/gcp/variables.tf`, or
`pkg/model.Config.TargetRepos`), confirm those repos already exist
under the account the App is installed on -- `githubsandbox` only ever
creates its own ephemeral scratch repos, never the deployment's real
target repos, so those need to exist beforehand regardless of anything
in this playbook.

## 5. Nothing to drop

Unlike the GCP playbooks, there's no ephemeral master credential to
revoke here -- the App ID and private key `bootstrap-github-app` wrote
in step 2 are this deployment's own standing credential, meant to
persist in `$GRAIN_DATA_DIR/secrets` the same way github-connections.md's
PAT or App key does. This playbook is finished once step 4's dispatch
succeeds.
