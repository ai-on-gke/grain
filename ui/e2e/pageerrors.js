import { expect } from "@playwright/test";

// See the console listener below.
const RENDER_FAILED = "grain: render failed";

// WebKit's report of a fetch the page itself abandoned by navigating:
// a poll or a held /api/restart-watch still in flight when a test calls
// page.goto or reloads. WebKit hands it to Playwright as a page error
// even though the app caught the rejection, and words it as an access
// control failure although every request here is same-origin. It failed
// specs at random across the suite -- whichever one navigated while a
// request was out (candidate-keyboard-walkthrough.spec.js most often) --
// and names no bug, so it is the one page error not counted.
const WEBKIT_ABANDONED_FETCH =
  /^Fetch API cannot load \S+ due to access control checks\.$/;

// An uncaught exception in the page is a test failure, said as one.
//
// React unmounts the whole tree when a render throws, so a component that
// blows up on a value it did not expect leaves a blank pane behind it and
// nothing else. Every assertion after that reads "element(s) not found",
// which is the same sentence a selector typo produces and the same
// sentence a slow server produces -- and none of the three name the
// TypeError sitting in a browser console nobody is reading.
//
// grain/task-182 is what this costs when it is missing. Two `ui e2e` runs
// went red on two different specs -- config.spec.js waiting for a Submit
// button, director.spec.js waiting for a task's state badge -- each one
// "element(s) not found, 5000ms", each on a branch that had touched
// neither the frontend nor anything either spec drives. Both were
// DetailOverlay reading `config.agentFramework` before /api/config had
// answered, which is a race the page can only lose on a busy machine; the
// suite reported it as two unrelated flaky waits, and the reading that
// invites is "raise the timeout" or "the CI box is too small". The error
// had been in the trace both times.
//
// So: collect what the page throws, and assert at teardown that it threw
// nothing. That does not make the failing assertion any faster -- it
// still spends its own timeout first -- but the report underneath it now
// names the exception and its stack, which is the difference between
// reading this in a minute and guessing at it for an afternoon.
//
// Spread into `base.extend()` (fixtures.js, director-fixtures.js) or into
// `test.extend()` for a spec that drives Playwright's own `test` directly.
export const pageErrors = {
  page: async ({ page }, use, testInfo) => {
    const thrown = [];
    page.on("pageerror", (err) => {
      // Matched on the first line of the message or of the stack: the
      // anchored test of err.message alone let it through on 10-15
      // (candidate-keyboard-walkthrough.spec.js, twice running), with
      // the stack reading exactly this sentence and its frames.
      const abandoned = [err.message, err.stack].some((text) =>
        WEBKIT_ABANDONED_FETCH.test(
          String(text || "")
            .split("\n")[0]
            .trim(),
        ),
      );
      if (abandoned) return;
      thrown.push(err);
    });
    // A render throw an ErrorBoundary caught (grain/task-1435) is no
    // longer uncaught -- the page keeps working around it, which is the
    // point -- but it is still the bug this guard is here to name. The
    // boundary writes one console line for it, starting with
    // RENDER_FAILED (src/components/ErrorBoundary.jsx); the literal is
    // repeated here because this file runs in Node, outside the bundle.
    //
    // A spec that breaks a page on purpose, to watch a boundary catch it
    // (error-boundaries.spec.js), says so with an "allows-render-error"
    // annotation; an uncaught exception still fails it.
    const allowsCaught = testInfo.annotations.some(
      (a) => a.type === "allows-render-error",
    );
    page.on("console", (msg) => {
      if (
        !allowsCaught &&
        msg.type() === "error" &&
        msg.text().startsWith(RENDER_FAILED)
      ) {
        thrown.push(new Error(msg.text()));
      }
    });
    await use(page);
    // Formatted rather than passed as Error objects: what a reader needs
    // here is the message and the stack, and a bare `toEqual([])` over
    // Errors prints neither usefully.
    expect(
      thrown.map((err) => err.stack || String(err)),
      "the page threw an uncaught exception -- React unmounts the tree " +
        "when a render throws, so any 'element(s) not found' above this " +
        "is most likely its consequence rather than its own failure",
    ).toEqual([]);
  },
};
