# 2026-10-22: agent CLI run locations (task 5012)

What was run to verify per-CLI run locations
([design/design/cli-run-location.design.md](../../../design/design/cli-run-location.design.md)) in
the harness. A real Workstation is the owner's to test after the merge.

## Container launcher, end to end against real docker

`go test ./pkg/agent/claude -run Container` (container_docker_test.go)
runs this on the sandbox's own docker engine (29.9.0):

- A throwaway `FROM scratch` image holds
  `pkg/agent/claude/testdata/fakeclaude` as `/usr/local/bin/claude`. It
  is the one stub: it stands in for the claude CLI, which needs a model
  provider.
- Everything else is real: `agent.ContainerHost`'s `docker run --rm
  --init`, `claude.WithHostMCP`, and a freshly built `grain mcpserver`
  behind `mcp.StartCommandHTTP` on the host.
- The run calls `read_file` on a host sandbox over the HTTP endpoint and
  gets the file back.
- It runs as the test's uid, with HOME set to the per-CLI home and the
  OAuth token arriving by name (`--env NAME`, value only in the docker
  client's environment).
- Its stream-json lands in the live transcript file.
- A cancelled run's container is killed and removed (label
  `dev.grain.cli.deployment`).

Both tests pass. They skip where the engine does not share the test's
filesystem.

## Settings → Models, against `grain demo`

`grain demo` resolves run locations as a workstation's daemon is started:
`-claude-run=container`, `-agy-run=host` with the Jetski `-agy-path`, and
codex on `auto`. The `auto` probe really runs on this machine, which has
no `codex`. The demo's `-cli-image` is
`ghcr.io/bwsalmon/grain/google-grain:latest`; nothing is pulled.

`ui/e2e/cli-run-locations.spec.js` (Playwright, Chromium) drives it:

- It switches claude to Host, saves, and sees the resolved line become
  `host (...)` with the reason "host set in Settings -> Models".
- After a reload, `GET /api/settings/cli-run` shows the stored row is
  `{"claude":{"location":"host"}}`.
- It switches back and sees `container (...)` again.
- Bounding boxes: the four rows stack inside the card, the selects line
  up at 1440 px, and the page has no horizontal overflow at 1440 px or
  390 px.

## Screenshots

`node e2e/screenshot.mjs --pages "/settings/models,/settings/models#cli-run"`,
desktop (1440×900) and phone (390×844), light and dark. The card shots
are here, with `contact-sheet.png` holding all four.

The PNGs were not looked at by eye. The run's file reader shows a PNG as
bytes, and the Gemini key `LOOK=1` uses was refused (`API_KEY_INVALID`).
Each shot was read back with tesseract OCR instead:

- every row's label, select, image field, resolved line and reason is
  present in both themes and at both widths;
- the long image/version line wraps inside the card on the phone;
- the custom-commands row read `host (not found on PATH)`, which was
  fixed to `host (each connection's own command)`.

The chip beside each resolved line repeats its first word (`host host
(...)` in OCR). That is a deliberate colour cue, not duplicated text.
