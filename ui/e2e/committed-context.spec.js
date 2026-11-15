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

const SIZES = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

const SEEDED_TITLE = "Add a dark mode toggle";

async function findSeededTask(request) {
  const response = await request.get("/api/tasks");
  expect(response.ok()).toBeTruthy();
  const payload = await response.json();
  const list = Array.isArray(payload) ? payload : payload.tasks || [];
  return list.find((t) => t.title === SEEDED_TITLE);
}

async function expectFitsViewportAndCard(section, page, viewportWidth) {
  await section.scrollIntoViewIfNeeded();
  await expect(section).toBeVisible();

  const docOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(docOverflow).toBeLessThanOrEqual(0);

  const box = await section.boundingBox();
  expect(box).not.toBeNull();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(viewportWidth + 1);

  const sectionOverflow = await section.evaluate(
    (el) => el.scrollWidth - el.clientWidth,
  );
  expect(sectionOverflow).toBeLessThanOrEqual(1);

  for (const item of await section.locator(".committed-context-item").all()) {
    const itemBox = await item.boundingBox();
    expect(itemBox).not.toBeNull();
    expect(itemBox.x).toBeGreaterThanOrEqual(box.x - 1);
    expect(itemBox.x + itemBox.width).toBeLessThanOrEqual(
      box.x + box.width + 1,
    );
  }
}

for (const [name, size] of Object.entries(SIZES)) {
  test(`committed context card renders seeded target-repo spec and manifest (${name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const task = await findSeededTask(request);
    expect(task, `seeded "${SEEDED_TITLE}" task`).toBeTruthy();

    await page.goto(`/tasks/${task.id}`);
    const section = page.getByTestId("committed-context");
    await expectFitsViewportAndCard(section, page, size.width);

    await expect(section).toContainText("Committed context");
    await expect(section).toContainText("target repo");
    await expect(section).toContainText("design/ui/theme-toggle");
    await expect(section).toContainText("Theme Switcher Spec");
    await expect(section).toContainText(
      "Approved product brief, contrast tokens, and settings mock",
    );
    await expect(section).toContainText("spec.md");
    await expect(section).toContainText("settings-mock.html");

    const shaLink = section.getByRole("link", { name: "@9f4c2b81e7" });
    await expect(shaLink).toHaveAttribute(
      "href",
      /\/tree\/9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90\/design\/ui\/theme-toggle$/,
    );

    const sourceLink = section.getByRole("link", { name: "source" });
    await expect(sourceLink).toHaveAttribute(
      "href",
      "https://example.com/specs/theme-toggle",
    );
  });

  test(`committed context card renders read-only repo placement cleanly (${name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const task = await findSeededTask(request);
    expect(task, `seeded "${SEEDED_TITLE}" task`).toBeTruthy();

    await page.route(
      new RegExp(`/api/tasks/${task.id}(\\?.*)?$`),
      async (route) => {
        if (route.request().method() !== "GET") return route.fallback();
        const headers = { ...route.request().headers() };
        delete headers["if-none-match"];
        const response = await route.fetch({ headers });
        const body = await response.json();
        const nextContext = [
          ...(body.context || []),
          {
            repo: "acme/product-specs",
            path: "design/plc/042-committed-context-architecture",
            rev: "4d71a90cc3e8102b5f649a12d83c7e09f1b42a61",
            placement: "read",
          },
        ];
        await route.fulfill({
          response,
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ ...body, context: nextContext }),
        });
      },
    );

    await page.goto(`/tasks/${task.id}`);
    const section = page.getByTestId("committed-context");
    await expectFitsViewportAndCard(section, page, size.width);

    await expect(section.getByText("target repo")).toBeVisible();
    await expect(section.getByText("read-only repo")).toBeVisible();
    await expect(
      section.getByText(
        "acme/product-specs:design/plc/042-committed-context-architecture",
      ),
    ).toBeVisible();
    const readSha = section.getByRole("link", { name: "@4d71a90cc3" });
    await expect(readSha).toHaveAttribute(
      "href",
      "https://github.com/acme/product-specs/tree/4d71a90cc3e8102b5f649a12d83c7e09f1b42a61/design/plc/042-committed-context-architecture",
    );
  });
}
