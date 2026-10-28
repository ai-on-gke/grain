# Run locations: container or host (task 5118)

Settings -> Models' "Where each agent CLI runs" card after the per-run
docker launcher and the `auto` run location were removed
(`docs/design/cli-run-location.md`, "Run locations").

## How it was run

Against the real `grain demo` server built from this branch, nothing of
grain's own mocked and no external boundary stubbed:

```
cd ui && npm ci && npm run build && go build -o /tmp/grain ../cmd/grain
node e2e/screenshot.mjs --bin /tmp/grain --pages "/settings/models#cli-run" --out screenshots/after
node e2e/screenshot.mjs --bin /tmp/grain --pages "/settings/models#cli-run" --out screenshots/after-hostd \
  --demo-args "-host-executor=hostd"
npx playwright test e2e/cli-run-locations.spec.js e2e/cli-run-locations-kubernetes.spec.js \
  e2e/cli-run-locations-hostd.spec.js
```

Desktop (1440) and phone (390), light and dark. The card sits below the
fold on a phone, so the phone shots show its top; the layout of every row
at both widths is asserted by `cli-run-locations.spec.js`'s
bounding-box test (rows stacked inside the card, selects in one column at
desktop width, no horizontal overflow).

## What it shows

- **Bare machine** (`bare-machine-*.png`; the demo's default, a
  development daemon): the card says every CLI runs on this host and that
  container is offered only in the release container. All four rows
  resolve to `host`. claude's `-claude-run=container`, a flag an older
  unit could pass, is shown with its warning: "set to container, but a
  daemon on the bare machine has no container of its own to run it in.
  It runs on the host until this is set to host or default". The select
  offers Default and Host only, and `PUT` of `container` is a 400 (e2e).
- **Behind grain hostd** (`hostd-contact-sheet.png`): claude, codex and
  custom commands resolve to "container (the daemon's own)" with the
  default's reason "container the default: a subprocess in the daemon's
  own container"; agy to "host (through grain hostd)". No "auto" and no
  image field anywhere.

## Result

All six run-location e2e tests passed. Nothing in the card overflows or
overlaps. Two things noted that predate this change: the chip repeats the
resolved location word, and a select left at Default shows only its
"Runs on" label.
