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

import { expect, test } from "./fixtures.js";

// Settings → Sandboxes → Disk floor minimum (MiB) shows grain's default
// (5120) as its placeholder, so a blank box means that default. It used to
// save 0 instead, which is "no minimum" -- the 2026-11-08 connections and
// infrastructure settings walkthrough
// (docs/test-runs/2026-11-08-connections-infrastructure-settings.md).
test("blanking Disk floor minimum saves its default, not 0", async ({
  page,
}) => {
  const stored = async () =>
    (await (await page.request.get("/api/settings")).json())
      .sandboxDiskFloorMinMb;
  const original = await stored();
  await page.goto("/settings/sandboxes");
  const box = page.getByLabel("Disk floor minimum (MiB)");
  try {
    await expect(box).toHaveAttribute("placeholder", "5120");

    const save = () =>
      page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/settings") && r.request().method() === "PUT",
      );
    await box.fill("2048");
    let saved = save();
    await box.press("Tab");
    expect((await saved).status()).toBe(200);
    await expect.poll(stored).toBe(2048);

    await box.fill("");
    saved = save();
    await box.press("Tab");
    expect(JSON.parse((await saved).request().postData())).toEqual({
      sandboxDiskFloorMinMb: 5120,
    });
    await expect.poll(stored).toBe(5120);
    await page.reload();
    await expect(box).toHaveValue("5120");
  } finally {
    await page.request.put("/api/settings", {
      data: { sandboxDiskFloorMinMb: original },
    });
  }
});
