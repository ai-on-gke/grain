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

// Every capability picker's policy verdict links to
// /settings/capabilities#capability-policy. The policy card draws at once,
// and the server list above it a moment later: the link used to scroll to
// the card and then be pushed some 1600px past it by that list.
for (const [label, viewport] of [
  ["desktop", { width: 1440, height: 1000 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`#capability-policy stays in view once the server list has drawn (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/capabilities#capability-policy");
    await expect(
      page.locator("[data-capability-server-row]").first(),
    ).toBeVisible();
    const card = page.locator("#capability-policy");
    await expect(card).toBeInViewport();
    await expect(
      card.getByRole("button", { name: "Add rule" }),
    ).toBeInViewport();
  });
}
