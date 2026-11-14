# Classic UI e2e suite (frozen)

This directory holds the Playwright suite for the classic React UI at
`/`. The Angular UI at `/next/` has its own suite in `ui-ng/e2e/`. The classic UI only takes fixes now (`PROMPT.md`, "Two
UIs"). Its tests are being moved to Go and to ui-ng
(`docs/test-plans/classic-ui-e2e-migration/`). Until then the classic
suite is **frozen**:

1. **No new classic specs.** New coverage goes against `/next/`
   (`ui-ng/e2e/`) or into Go under `tests/uiapi/`. `TestNoNewClassicE2ESpecs`
   (`tests/hygiene/classic_e2e_freeze_test.go`) fails on any spec in
   this directory that is not in `FROZEN_SPECS`. `FROZEN_SPECS` only gets shorter: delete a line when you
   delete its spec, and never add one.
2. **Flakes are quarantined, not fixed.** A classic test that flakes
   goes in `QUARANTINE`, with the evidence (`grain ci test-history`, the
   failing runs) in a comment. It then leaves the gating `ui e2e` job.
   Fix it only if the classic page is broken for users.
3. **Specs stop gating once they're ported.** When a spec's server half
   is in Go and its ui-ng half is in the ui-ng e2e job, it goes in
   `NONGATING`. The plan's "When a classic spec stops gating" has the
   rule.
4. **Nothing is deleted** until its classic page retires.

## QUARANTINE and NONGATING

Each entry is one line. It is either a spec file or a spec file plus the
line its `test(` call starts on, relative to this directory. `#` starts
a comment:

```
director.spec.js                 # the whole file
inbox.spec.js:72                 # one test; failed 3 of 40 runs 11-07..11-11
```

- **Gating `ui e2e`** (`make test-e2e-suite`, and `npm run test:e2e`
  locally): `playwright.config.js` turns every entry into a
  `grepInvert` pattern (`frozen.js`), so those tests don't run at all.
- **`ui e2e (quarantined)`** (`make test-e2e-quarantined`, or
  `node e2e/run-frozen.mjs` from `ui/`) runs exactly the listed entries,
  with `continue-on-error`. A regression in them shows up red on the
  pull request but doesn't block it. With both lists empty it says so
  and passes.
- To run everything, quarantined tests included, set
  `GRAIN_E2E_LANE=all`. Any value other than `gating` turns the
  `grepInvert` off.

`grepInvert` matches a test's title, not its line. So a `file:line`
entry is resolved to the title written on that line, and a test whose
title is not a plain string literal (a loop's template string) has to be
quarantined as a whole file. Within a file the tests run in order and
may depend on each other. If skipping one test breaks the ones after
it, quarantine the file. `TestClassicE2EFreezeLists` checks every entry
in `go unit`: the spec exists, the line starts a `test(`, the title is
literal, and nothing is listed twice. When a quarantined test moves to
another line, its entry has to move with it.
