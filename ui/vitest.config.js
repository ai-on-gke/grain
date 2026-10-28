// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { defineConfig, mergeConfig, configDefaults } from "vitest/config";
import viteConfig from "./vite.config.js";

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      setupFiles: ["./src/setupTests.js"],
      // e2e/ holds Playwright specs, which the ui-e2e CI job runs through
      // `make test-e2e`. Vitest's default include glob matches *.spec.js
      // too, and Playwright's own test() throws outright when called
      // under any runner but its own -- so without this the two suites
      // collide and `npm test` fails on a file it was never meant to run.
      exclude: [...configDefaults.exclude, "e2e/**"],
      // Vitest's default is 5s per test, which this suite outgrew a while
      // ago. The tests that render a whole App or a whole NewTaskOverlay
      // -- the rail with every section in it, the toolbar, the list, then
      // three or four userEvent clicks, each one a re-render of all of it
      // -- take 7-13s on an idle three-core box, and the *same* test
      // varies by nearly 2x between two runs of the same suite on the
      // same machine. Around thirty of the ~1300 tests cross 5s on an
      // ordinary run. So 5s was not a bound on anything: it sat inside
      // the spread of a healthy run, and which tests it caught was
      // decided by how loaded the runner was that morning -- the worst
      // kind of red, one that says "this branch broke something" about a
      // branch that touched no JavaScript at all.
      //
      // The alternative was to trim the whole-App renders instead, and
      // that is the wrong trade here: what those tests assert (which pane
      // a URL opens, what narrowing survives a trip to another view and
      // back) only exists once the whole shell is mounted, and the time
      // goes on the render itself rather than on anything wasteful.
      //
      // 20s is a bound on a *stuck* test rather than a target: a passing
      // suite takes exactly as long as it did, and a test that really is
      // hanging still goes red, just four times slower. It is about 3x
      // the slowest honest test on a CI runner, and no run at one worker
      // per core has come near it -- the only way to fail a test at 20s
      // was to deliberately oversubscribe the box. Raise it again if the
      // shell grows, but keep it a bound with a cost in mind: at 20s a
      // change that hangs every App test still tells CI so within a few
      // minutes, where at a minute a test it would take a quarter hour.
      //
      // Two rules go with it, so this does not go back to being a
      // treadmill of one-test-at-a-time caps:
      //
      //   - Nothing gets an `it(..., ms)` cap below this one. A per-test
      //     cap only makes the slowest tests -- exactly the ones that
      //     needed the room -- fail first, and it goes stale here rather
      //     than where it is written.
      //   - This does not cover `waitFor`, which bounds itself. A
      //     `waitFor` waiting on a real timer (the 3s poll interval, the
      //     5s error-banner dismissal) needs its own generous timeout:
      //     the interval plus ten seconds, by convention, and under this
      //     bound so the failure names the assertion rather than the
      //     test. It costs a green run nothing, since waitFor returns the
      //     moment its condition holds.
      testTimeout: 20000,
    },
  }),
);
