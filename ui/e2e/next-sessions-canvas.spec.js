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

test("M17 Sessions workspace: inline model picker and coplanar Artifact/Memory Canvas", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1440, height: 900 });

  await page.goto("/next/sessions/director");
  await page.waitForSelector(".main-stage > *:not(router-outlet)");

  // Workspace Canvas toggles open from chat header and switches between Docs, HTML Mock, Workstream, and Memory.
  const toggle = page.getByTestId("session-canvas-toggle");
  await expect(toggle).toBeVisible();
  await toggle.click();

  const canvas = page.getByTestId("session-canvas");
  await expect(canvas).toBeVisible();

  await page.getByTestId("canvas-tab-html").click();
  await page.getByTestId("canvas-tab-plan").click();
  await page.getByTestId("canvas-tab-docs").click();
  const memTab = page.getByTestId("canvas-tab-memory");
  if ((await memTab.count()) > 0) {
    await memTab.click();
  }

  // Inline header model picker renders when models exist on deployment.
  const headModel = page.getByTestId("chat-head-model");
  if ((await headModel.count()) > 0) {
    await expect(headModel).toBeVisible();
  }

  await page.getByTestId("canvas-close").click();
  await expect(canvas).toHaveCount(0);
});
