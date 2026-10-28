import { defineConfig } from "@playwright/test";

// e2e is the one suite in this tree that is not jsdom (vite.config.js's
// own `test` block, which vitest reads): a real browser, driven by
// Playwright, against a real pkg/ui.Server over a real embedded SQLite
// store -- see e2e/fixtures.js and e2e/tasks.spec.js's own doc comment
// for why (bwsalmon/agents#415).
export default defineConfig({
  testDir: "./e2e",
  // Serial within a file, and the files still run several at a time --
  // as many as `workers` comes to, which is half the machine's cores
  // unless something says otherwise. Both halves of that matter, and
  // they are kept apart by different things:
  //
  //   - Within a file, the tests are written to run in order, over one
  //     store, sometimes leaning on what the test above them left (a
  //     task it filed, a layout it saved). That is what this line buys,
  //     and it is why it stays.
  //   - Across files, what keeps two specs off each other is that each
  //     worker now drives a `grain demo` of its own -- e2e/fixtures.js,
  //     grain/task-292 -- rather than this line, which never applied
  //     between files. It was one shared store until then, with several
  //     files reordering and editing the same seeded rows in it.
  //
  // So this is not the suite's isolation, and turning it on
  // (`fullyParallel: true`) would still be a change to what the specs
  // may assume about each other, not a free one.
  fullyParallel: false,
  retries: 0,
  // "list" everywhere, and on CI the github reporter beside it: it
  // writes each failure out as a workflow annotation, which is the only
  // part of a job an agent -- or anyone without admin on this repository
  // -- can read back afterwards. Without it a red `ui e2e` says
  // "Process completed with exit code 2" and nothing else, and the trace
  // this config keeps on failure sits in an artifact needing the same
  // rights the log does.
  reporter: process.env.CI ? [["github"], ["list"]] : "list",
  // Builds the `grain` binary each worker's own demo server runs, and
  // cleans up after it. There is no `webServer` here any more: a server
  // configured here is one server for the whole run, and the point of
  // e2e/fixtures.js is that there is one per worker, on a port the
  // kernel picks, which only a fixture can arrange.
  globalSetup: "./e2e/global-setup.js",
  globalTeardown: "./e2e/global-teardown.js",
  use: {
    // No baseURL here: e2e/fixtures.js overrides that option per worker
    // with its own server's URL. The specs still only ever write paths.
    trace: "retain-on-failure",
  },
  // Two browsers, and only one of them runs everything (grain/task-74).
  //
  // # Why a second engine at all
  //
  // The touch gestures in src/components/ReorderPickup.jsx exist because
  // of a report from an iPad -- from Safari -- and until now Chromium was
  // the only engine that had ever executed them. Nearly everything a
  // touch gesture rests on is the browser's own: which pointer events a
  // finger produces and with what `pointerType`, whether
  // `touch-action: none` suppresses the pan, whether `preventDefault()`
  // on `pointerdown` suppresses the compatibility click, whether
  // style.css's `(pointer: coarse)` block applies at all. Checking all of
  // that against Chromium and shipping it for Safari is an assertion
  // about an engine nothing here runs.
  //
  // Playwright's WebKit is not iOS Safari and cannot be -- it is a Linux
  // build of the same engine, driven headless, with no iPadOS text
  // selection, callout or long-press drag in it. It is the nearest thing
  // a Linux runner can hold, and its value is that it is a *second
  // implementation* rather than a second copy of the first. A quick check
  // on a real iPad is still worth more than this whole project.
  //
  // It is already earning that: running phone.spec.js here found that
  // `preventDefault()` on `pointerdown` suppresses the compatibility
  // mousedown/mouseup in both engines and, in WebKit, the compatibility
  // *click* as well -- which Chromium fires anyway, with pointerType
  // "touch". So ReorderPickup's CLICK_GRACE claim is answering Chromium's
  // behaviour, and is simply never exercised on the engine the gesture
  // was written for.
  //
  // # Why phone.spec.js and nothing else
  //
  // It is the file whose subject is the browser rather than the app:
  // touch input, layout at a phone's and a tablet's width, and the
  // coarse-pointer rules. The other specs drive the same React through
  // clicks and typing, where a second engine would mostly re-verify
  // Playwright -- for about the wall clock of the whole suite again.
  //
  // keyboard-open-rows.spec.js is the other: whether Tab stops on a link
  // at all is the engine's own call, and Safari's is not Chromium's.
  // alerts-panel-layout.spec.js is the phone layout test for the alerts
  // panel (it began as the banner-strip one that used to sit in
  // responsive-navigation-walkthrough.spec.js). It has a file of its own
  // because it needs a demo of its own, and it keeps the second engine
  // it had there.
  //
  // # What WebKit cannot run
  //
  // One test: the finger drag, which needs a touch point that moves.
  // Playwright's public touchscreen is a single atomic `tap()`, so that
  // test reaches past it into Chromium's own input pipeline over CDP --
  // and CDP is Chromium-only. WebKit's inspector protocol does have the
  // command that would do it (`Input.dispatchTouchEvent`, with
  // touchStart/touchMove/touchEnd, in this build's own protocol.json),
  // but Playwright wires nothing to it and exposes no raw WebKit session
  // to reach it through. There is no honest substitute: a `page.mouse`
  // drag arrives with pointerType "mouse", which the gesture deliberately
  // ignores in favour of the HTML5 drag events; a TouchEvent dispatched
  // from the page generates no pointer events at all, and WebKit will not
  // even construct one (`new Touch()` throws "Illegal constructor"). So
  // that test skips here, out loud, rather than passing against something
  // that is not a touch -- see its own comment in phone.spec.js.
  projects: [
    { name: "chromium", use: { browserName: "chromium" } },
    {
      name: "webkit",
      testMatch:
        /(?:phone|responsive-navigation-walkthrough|alerts-panel-layout|keyboard-open-rows|candidate-keyboard-walkthrough)\.spec\.js/,
      use: { browserName: "webkit" },
    },
  ],
});
