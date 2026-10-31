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

// A task an external agent files over /mcp is created with the
// deployment's person as its creator, and its page used to say only
// "Creator: debian" -- as though that person had filed it
// (docs/test-runs/2026-10-29-external-agent-mcp.md, follow-up 1). The
// page now names the agent ahead of the creator.

import { test, expect } from "./fixtures.js";

const VIEWPORTS = [
  { label: "desktop", width: 1440, height: 900 },
  { label: "phone", width: 390, height: 844 },
];

test("a task an external agent filed names that agent on its page", async ({
  page,
  request,
}, testInfo) => {
  const before = await (await request.get("/api/settings")).json();
  let tokenID = "";
  try {
    const put = await request.put("/api/settings", {
      data: { externalAgentApi: true },
    });
    expect(put.ok()).toBe(true);
    const issue = await request.post("/api/external-agents/tokens", {
      data: { name: "widgets-writer", scopes: ["tasks:write@acme/widgets"] },
    });
    expect(issue.ok()).toBe(true);
    const issued = await issue.json();
    tokenID = issued.id;

    const call = await request.post("/mcp", {
      headers: { Authorization: `Bearer ${issued.token}` },
      data: {
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: {
          name: "api_create_task",
          arguments: {
            body: {
              title: "Widget work from w",
              body: "x",
              repo: "acme/widgets",
            },
          },
        },
      },
    });
    expect(call.ok()).toBe(true);
    const rpc = await call.json();
    expect(rpc.error).toBeUndefined();
    expect(rpc.result.isError).toBeFalsy();
    const created = JSON.parse(rpc.result.content[0].text);
    expect(created.author).toBe("external:widgets-writer");

    for (const viewport of VIEWPORTS) {
      await page.setViewportSize(viewport);
      await page.goto(`/tasks/${created.id}`);
      const filedBy = page.locator(".declared-row", { hasText: "Filed by" });
      await expect(filedBy).toContainText(
        "external agent widgets-writer (external:widgets-writer)",
      );
      // The creator is still named, after the agent.
      const creator = page.locator(".declared-row", { hasText: "Creator" });
      await expect(creator).toBeVisible();
      const a = await filedBy.boundingBox();
      const c = await creator.boundingBox();
      expect(a.y).toBeLessThan(c.y);
      // The value stays inside its column, even at phone width.
      const block = await page.locator(".declared").first().boundingBox();
      const value = await filedBy.locator(".declared-value").boundingBox();
      expect(value.x + value.width).toBeLessThanOrEqual(
        block.x + block.width + 0.5,
      );
      expect(block.x + block.width).toBeLessThanOrEqual(viewport.width);
      await page.screenshot({
        path: testInfo.outputPath(`external-filer-${viewport.label}.png`),
      });
    }

    // The list's Origin attribute no longer counts it as filed by hand.
    await page.setViewportSize(VIEWPORTS[0]);
    const title = page.getByText("Widget work from w", { exact: true });
    await page.goto("/?f=origin:any:external");
    await expect(title.first()).toBeVisible();
    await page.goto("/?f=origin:any:hand");
    await expect(page.locator(".declared-row, a, li").first()).toBeVisible();
    await expect(title).toHaveCount(0);
  } finally {
    if (tokenID) {
      await request.delete(`/api/external-agents/tokens/${tokenID}`);
    }
    await request.put("/api/settings", {
      data: { externalAgentApi: Boolean(before.externalAgentApi) },
    });
  }
});
