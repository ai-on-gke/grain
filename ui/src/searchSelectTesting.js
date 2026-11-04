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

import { waitFor } from "@testing-library/react";

// chooseOption is user.selectOptions for a SearchSelect box (RepoField,
// SearchSelect.jsx): it opens the list the box controls and clicks the
// row whose value, label or text is wanted -- "acme/widgets", "none" or
// "Other…" all name a row, the way selectOptions takes either an
// option's value or its text.
export async function chooseOption(user, input, wanted) {
  if (input.getAttribute("aria-expanded") !== "true") {
    await user.click(input);
  }
  const listbox = await waitFor(() => {
    const id = input.getAttribute("aria-controls");
    const el = id && document.getElementById(id);
    if (!el) throw new Error("the option list did not open");
    return el;
  });
  const rows = [...listbox.querySelectorAll('[role="option"]')];
  const row = rows.find(
    (r) =>
      r.dataset.value === wanted ||
      r.dataset.label === wanted ||
      r.textContent === wanted,
  );
  if (!row) {
    throw new Error(
      `no option ${JSON.stringify(wanted)} among ${rows
        .map((r) => JSON.stringify(r.dataset.value))
        .join(", ")}`,
    );
  }
  await user.click(row);
}

// optionValues is the values of the rows a SearchSelect box's open list
// is showing right now.
export function optionValues(input) {
  const id = input.getAttribute("aria-controls");
  const el = id && document.getElementById(id);
  if (!el) return [];
  return [...el.querySelectorAll('[role="option"]')].map(
    (r) => r.dataset.value,
  );
}
