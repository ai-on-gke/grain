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

import { test, expect } from "./fixtures.js";

// Settings -> Models on a daemon that takes a grain hostd dialing in to
// it, with none connected (`grain demo -host-executor=hostd-remote-down`).
// The card says so at once: the daemon knows no session exists, so it
// does not wait out the reconnect a run would (hostd.ForDisplay). It took
// 5 s (hostd's offers and status timeouts) before that.

test.use({ demoArgs: ["-host-executor=hostd-remote-down"] });

test("with no hostd dialed in, Settings -> Models says so at once", async ({
  page,
}) => {
  const start = Date.now();
  await page.goto("/settings/models#cli-run");
  const status = page.getByTestId("cli-run-hostd-status");
  await expect(status).toContainText("none is connected", { timeout: 3000 });
  expect(Date.now() - start).toBeLessThan(3000);

  const took = await page.evaluate(async () => {
    const start = performance.now();
    const r = await fetch("/api/settings/cli-run");
    await r.json();
    return performance.now() - start;
  });
  expect(took).toBeLessThan(1500);
});
