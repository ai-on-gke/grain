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

import { expect } from "@playwright/test";

// The page header rail's alerts bell and the panel it opens
// (src/components/AlertsButton.jsx, grain/task-4008): every standing
// warning -- a pause, a stopped reconcile loop, a failing settings sync,
// loopback trust, a team's setup steps left -- where each used to be a
// banner across the top of every page.

// alertsBell is the bell on screen: in the page header rail, or on a phone in the
// bar PhoneNav keeps above the page.
export function alertsBell(page) {
  return page.getByTestId("alerts-button").filter({ visible: true });
}

// openAlerts opens the panel, if it is not open already, and returns it.
export async function openAlerts(page) {
  const panel = page.getByTestId("alerts-panel");
  if (!(await panel.isVisible())) await alertsBell(page).click();
  await expect(panel).toBeVisible();
  return panel;
}

// closeAlerts closes the panel again, as Escape does.
export async function closeAlerts(page) {
  const panel = page.getByTestId("alerts-panel");
  if (await panel.isVisible()) await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
}

// alertRow is the panel's row saying text (a string or a pattern).
export function alertRow(page, text) {
  return page
    .getByTestId("alerts-panel")
    .getByTestId("alert-row")
    .filter({ hasText: text });
}
