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

import { test, expect } from "./fixtures";

// The Context page's design doc preview renders through [gMarkdown], so a
// ```mermaid fence in a memory document's body is drawn as a diagram, and
// the preview's code-copy buttons and in-app links keep working.

const BODY = [
  "# Request path",
  "",
  "See the [inbox](/next/inbox) for open questions.",
  "",
  "```mermaid Request flow",
  "flowchart LR",
  "  Browser --> Proxy --> Daemon",
  "```",
  "",
  "```go",
  'fmt.Println("hello")',
  "```",
].join("\n");

test("a design doc preview draws its Mermaid fences as diagrams @phone @dark", async ({
  page,
  context,
  axe,
}) => {
  test.setTimeout(90_000);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto("context?tab=documents");

  const preview = page.getByTestId("doc-preview");
  await page.getByTestId("doc-new-btn").click();
  // An empty body shows the placeholder text, not a blank box.
  await expect(preview).toHaveText("Document body preview appears here.");

  await page.getByTestId("doc-text-input").fill(BODY);

  const flow = preview.getByRole("region", { name: "Request flow" });
  await expect(flow.locator("svg")).toBeVisible({ timeout: 20_000 });
  await expect(flow.locator("svg")).toContainText("Daemon");
  await flow.getByRole("button", { name: "Source" }).click();
  await expect(flow.getByLabel("Request flow source")).toContainText(
    "Browser --> Proxy --> Daemon",
  );
  await flow.getByRole("button", { name: "Diagram" }).click();
  await expect(flow.locator("svg")).toBeVisible();

  // The Go fence is a code card whose Copy button copies its code.
  const copy = preview.locator(".code-card [data-copy-code]");
  await copy.click();
  await expect(copy).toHaveText("Copied");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(
    'fmt.Println("hello")',
  );

  // The diagram and the code card sit inside the preview box, and the
  // preview's inline-code chip style does not pad the code card's block.
  const box = (await preview.boundingBox())!;
  for (const el of [flow, preview.locator(".code-card")]) {
    const b = (await el.boundingBox())!;
    expect(b.x).toBeGreaterThanOrEqual(box.x - 1);
    expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width + 1);
  }
  expect(
    await preview
      .locator(".code-card pre code")
      .evaluate((el) => getComputedStyle(el).paddingLeft),
  ).toBe("0px");

  // Nothing overflows sideways.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);

  expect(await axe()).toEqual([]);

  // A /next/ link navigates inside the app rather than reloading it.
  await page.evaluate(() => {
    (window as unknown as { __noReload: boolean }).__noReload = true;
  });
  await preview.getByRole("link", { name: "inbox" }).click();
  await expect(page).toHaveURL(/\/next\/inbox/);
  expect(
    await page.evaluate(
      () => (window as unknown as { __noReload?: boolean }).__noReload,
    ),
  ).toBe(true);
});
