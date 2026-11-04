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

// chooseOption is locator.selectOption for a SearchSelect box -- the
// searchable dropdown RepoField is drawn with (src/components/
// SearchSelect.jsx), which is a combobox <input> over a list rather than
// a <select>. It opens the list the box controls and clicks the row whose
// value or label is wanted: "acme/widgets", "none", "Other…", or
// { label: "Other…" } as selectOption takes it.
export async function chooseOption(box, wanted) {
  const want = typeof wanted === "string" ? wanted : wanted.label;
  if ((await box.getAttribute("aria-expanded")) !== "true") {
    await box.click();
  }
  await expect(box).toHaveAttribute("aria-expanded", "true");
  const id = await box.getAttribute("aria-controls");
  const list = `[id="${id}"] [role="option"]`;
  const row = box
    .page()
    .locator(
      `${list}[data-value=${JSON.stringify(want)}], ${list}[data-label=${JSON.stringify(want)}]`,
    );
  await row.first().click();
}

// optionValues is the values of the rows a SearchSelect box offers once
// opened -- what a <select>'s option[value] list was read for.
export async function optionValues(box) {
  if ((await box.getAttribute("aria-expanded")) !== "true") {
    await box.click();
  }
  await expect(box).toHaveAttribute("aria-expanded", "true");
  const id = await box.getAttribute("aria-controls");
  const values = await box
    .page()
    .locator(`[id="${id}"] [role="option"]`)
    .evaluateAll((rows) => rows.map((r) => r.dataset.value));
  await box.press("Escape");
  return values;
}
