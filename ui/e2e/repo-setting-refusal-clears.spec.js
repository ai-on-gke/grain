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

// A refused save on the repo page goes to the shell's error banner, which
// stays up for five seconds whatever happens next. Before the fix, a box
// fixed and saved a second later still read as refused under the value
// that had just been stored (the 2026-11-04 repository walkthrough). The
// good save takes its own box's refusal down at once, and only that one.
test("a repo setting's refusal goes once that setting saves, and only that one", async ({
  page,
}) => {
  await page.goto("/repos/acme/widgets/settings/agents");
  const upstream = page.getByLabel("Upstream repository for acme/widgets");
  const hostd = page.getByLabel("Default hostd for acme/widgets");
  const refusal = page.getByRole("alert").filter({ hasText: "upstreamRepo:" });
  try {
    await upstream.fill("not a repo");
    await upstream.blur();
    await expect(refusal).toBeVisible();
    await expect(upstream).toHaveValue("");

    await upstream.fill("acme-upstream/widgets");
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/repos/acme/widgets/settings") &&
        r.request().method() === "PUT" &&
        r.ok(),
    );
    await upstream.blur();
    await saved;
    // Well inside the banner's own five seconds.
    await expect(refusal).toHaveCount(0, { timeout: 1500 });
    await expect(page.getByTestId("repo-upstream-repo-summary")).toContainText(
      "acme-upstream/widgets",
    );

    // Another box's refusal is not this box's to take down.
    await hostd.fill("nowhere-e2e");
    await hostd.blur();
    const hostdRefusal = page
      .getByRole("alert")
      .filter({ hasText: "nowhere-e2e" });
    await expect(hostdRefusal).toBeVisible();
    await upstream.fill("");
    const cleared = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/repos/acme/widgets/settings") &&
        r.request().method() === "PUT" &&
        r.ok(),
    );
    await upstream.blur();
    await cleared;
    await expect(hostdRefusal).toBeVisible();
  } finally {
    // The demo server is shared by the worker's other specs.
    await page.request.put("/api/repos/acme/widgets/settings", {
      data: { upstreamRepo: "" },
    });
  }
});
