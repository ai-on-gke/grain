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

// ```mermaid fences in a task's conversation render as diagrams through
// [gMarkdown] (shared/mermaid.ts): a sanitized SVG drawn by the lazily
// loaded mermaid chunk, a Diagram / Source toggle, an explained fallback
// for a source that does not parse, and Before / After fences paired
// side by side.

const COMMENT = [
  "Here is the request path:",
  "",
  "```mermaid Request flow",
  "flowchart LR",
  "  Browser --> Proxy --> Daemon",
  "```",
  "",
  "```mermaid before",
  "flowchart TD",
  "  A[Monolith] --> DB[(Store)]",
  "```",
  "",
  "```mermaid after: split",
  "flowchart TD",
  "  A[API] --> Q[Queue] --> W[Worker] --> DB[(Store)]",
  "```",
  "",
  "```mermaid Broken",
  "flowchart TD",
  "  A --> --> ((",
  "```",
].join("\n");

async function api(
  baseURL: string,
  code: string,
  method: string,
  path: string,
  body?: unknown,
) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

test("a task comment's Mermaid fences render as diagrams @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}, testInfo) => {
  test.setTimeout(90_000);
  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid diagrams ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      { body: COMMENT },
    );
    await page.goto(`task/${task.id}`);

    const flow = page.getByRole("region", { name: "Request flow" });
    await expect(flow.locator("svg")).toBeVisible({ timeout: 20_000 });
    await expect(flow.locator("svg")).toContainText("Daemon");
    await expect(flow.getByLabel("Request flow source")).toBeHidden();

    // Source shows the fence text in place of the drawing.
    await flow.getByRole("button", { name: "Source" }).click();
    await expect(flow.getByRole("button", { name: "Source" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(flow.getByLabel("Request flow source")).toContainText(
      "Browser --> Proxy --> Daemon",
    );
    await expect(flow.locator("svg")).toBeHidden();
    await flow.getByRole("button", { name: "Diagram" }).click();
    await expect(flow.locator("svg")).toBeVisible();

    // Before / After pair: side by side on a desktop, Before alone on a
    // phone, and each layout button shows what it says.
    const pair = page.getByRole("region", {
      name: "Before and after diagrams",
    });
    const before = pair
      .getByRole("group", { name: "Before", exact: true })
      .locator("svg");
    const after = pair
      .getByRole("group", { name: "After: split", exact: true })
      .locator("svg");
    await expect(before).toBeVisible({ timeout: 20_000 });
    if (testInfo.project.name === "phone") {
      await expect(after).toBeHidden();
      await pair.getByRole("button", { name: "After" }).click();
      await expect(after).toBeVisible();
      await expect(before).toBeHidden();
    } else {
      await expect(after).toBeVisible();
      const b = (await before.boundingBox())!;
      const a = (await after.boundingBox())!;
      expect(a.x).toBeGreaterThan(b.x + b.width - 1);
      expect(Math.abs(a.y - b.y)).toBeLessThan(40);
      await pair.getByRole("button", { name: "Stacked" }).click();
      const b2 = (await before.boundingBox())!;
      const a2 = (await after.boundingBox())!;
      expect(a2.y).toBeGreaterThan(b2.y + b2.height - 1);
      await pair.getByRole("button", { name: "Side by side" }).click();
    }

    // A source that does not parse says so and shows the source.
    const broken = page.getByRole("region", { name: "Broken" });
    await expect(broken.getByRole("status")).toContainText(
      "Diagram could not be rendered",
      { timeout: 20_000 },
    );
    await expect(broken.getByLabel("Broken source")).toContainText("A -->");

    // Nothing the diagrams put in the page can run script or load
    // anything from elsewhere.
    expect(
      await page
        .locator(
          ".mermaid-canvas script, .mermaid-canvas foreignObject, .mermaid-canvas image",
        )
        .count(),
    ).toBe(0);

    // Nothing overflows sideways.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);

    expect(await axe()).toEqual([]);
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});

test("a theme switch redraws a diagram in the other palette in place", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  await page.addInitScript(() =>
    localStorage.setItem("grain.themeMode", "light"),
  );
  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid theme ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      { body: "```mermaid Themed\nflowchart LR\n  A --> B\n```" },
    );
    await page.goto(`task/${task.id}`);
    const svg = page.getByRole("region", { name: "Themed" }).locator("svg");
    await expect(svg).toBeVisible({ timeout: 20_000 });
    const nodeFill = () =>
      svg
        .locator(".node rect, .node path")
        .first()
        .evaluate((el) => getComputedStyle(el).fill);
    const light = await nodeFill();
    // "t" switches the theme in place; the diagram is redrawn, not
    // re-rendered from scratch, so its Source / Diagram choice survives.
    await page.locator("body").press("t");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await expect.poll(nodeFill).not.toBe(light);
    await expect(svg).toBeVisible();
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});

test("a diagram's %%{init}%% cannot pin its own palette or run its click handlers", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  await page.addInitScript(() => {
    localStorage.setItem("grain.themeMode", "light");
    (window as unknown as { __pwned: number }).__pwned = 0;
    window.alert = () => {
      (window as unknown as { __pwned: number }).__pwned++;
    };
  });
  const dialogs: string[] = [];
  page.on("dialog", (d) => {
    dialogs.push(d.message());
    void d.dismiss();
  });
  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid init ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      {
        body: [
          "```mermaid Plain",
          "flowchart LR",
          "  A --> B",
          "```",
          "",
          "```mermaid Pinned",
          "%%{init: {'theme': 'dark', 'securityLevel': 'loose', 'themeVariables': {'primaryColor': '#ff0000', 'mainBkg': '#ff0000'}, 'themeCSS': 'rect { fill: #ff0000 !important; }'}}%%",
          "flowchart LR",
          '  A["<img src=x onerror=alert(1)>"] --> B',
          "  click B call alert(2)",
          '  click A "javascript:alert(3)"',
          "```",
        ].join("\n"),
      },
    );
    await page.goto(`task/${task.id}`);
    const fill = (name: string) =>
      page
        .getByRole("region", { name })
        .locator("svg .node rect")
        .first()
        .evaluate((el) => getComputedStyle(el).fill);
    const pinned = page.getByRole("region", { name: "Pinned" }).locator("svg");
    await expect(pinned).toBeVisible({ timeout: 20_000 });
    await expect(
      page.getByRole("region", { name: "Plain" }).locator("svg"),
    ).toBeVisible();
    // The directive's colours and CSS are dropped: both diagrams wear the
    // palette ThemeService picked, and follow it through a switch.
    expect(await fill("Pinned")).not.toBe("rgb(255, 0, 0)");
    expect(await fill("Pinned")).toBe(await fill("Plain"));
    const light = await fill("Pinned");
    await page.locator("body").press("t");
    await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
    await expect.poll(() => fill("Pinned")).not.toBe(light);
    expect(await fill("Pinned")).toBe(await fill("Plain"));

    // Clicking the nodes runs nothing, and nothing unsafe is in the SVG.
    for (const node of await pinned.locator(".node").all()) {
      await node.click({ force: true });
    }
    expect(dialogs).toEqual([]);
    expect(
      await page.evaluate(
        () => (window as unknown as { __pwned: number }).__pwned,
      ),
    ).toBe(0);
    expect(
      await pinned
        .locator("script, foreignObject, image, [onclick], [onerror]")
        .count(),
    ).toBe(0);
    expect(
      await pinned
        .locator('a[href^="javascript"], a[*|href^="javascript"]')
        .count(),
    ).toBe(0);
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});

test("hostile Mermaid image shapes, actor icons and style URLs never fetch remote URLs", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  const leaked: string[] = [];
  await page.route("**/*evil.example*", (route) => {
    leaked.push(route.request().url());
    return route.abort();
  });

  const body = [
    "```mermaid Unquoted image",
    "flowchart LR",
    '  A@{ img: "https://evil.example/unquoted.png", label: "x" }',
    "```",
    "",
    "```mermaid Quoted image",
    "flowchart LR",
    '  A@{ "img": "https://evil.example/quoted.png", "label": "x" }',
    "```",
    "",
    "```mermaid Actor icon",
    "sequenceDiagram",
    "  participant A",
    '  properties A: {"icon": "https://evil.example/actor.png"}',
    "  A->>A: ping",
    "```",
    "",
    "```mermaid Style URL",
    "flowchart LR",
    "  A[hi]:::x",
    "  classDef x fill:url(https://evil.example/style.png)",
    "```",
    "",
    "```mermaid Theme CSS",
    '%%{init: {"themeCSS": "@import url(https://evil.example/theme.css);"}}%%',
    "flowchart LR",
    "  Ok --> Safe",
    "```",
  ].join("\n");

  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid remote fetch guard ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      { body },
    );
    await page.goto(`task/${task.id}`);

    for (const name of [
      "Unquoted image",
      "Quoted image",
      "Actor icon",
      "Style URL",
    ]) {
      const region = page.getByRole("region", { name, exact: true });
      await expect(region.getByRole("status")).toContainText(
        "Diagram could not be rendered",
        { timeout: 20_000 },
      );
    }

    const safe = page.getByRole("region", { name: "Theme CSS" });
    await expect(safe.locator("svg")).toBeVisible({ timeout: 20_000 });
    await expect(safe.locator("svg")).toContainText("Safe");

    expect(leaked).toEqual([]);
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});

test("YAML-anchored and tagged Mermaid @{...} image keys never fetch remote URLs", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  const leaked: string[] = [];
  await page.route("**/*evil.example*", (route) => {
    leaked.push(route.request().url());
    return route.abort();
  });

  const body = [
    "```mermaid Anchored key",
    "flowchart TD",
    '  A@{ &a img: "https://evil.example/anchor.png", label: "x" }',
    "```",
    "",
    "```mermaid Tagged key",
    "flowchart TD",
    '  A@{ !!str img: "https://evil.example/tag.png" }',
    "```",
    "",
    "```mermaid Verbatim tagged key",
    "flowchart TD",
    '  A@{ !<tag:yaml.org,2002:str> img: "https://evil.example/verbatim-tag.png" }',
    "```",
    "",
    "```mermaid Anchored quoted and combined key",
    "flowchart TD",
    '  A@{ &a "img": "https://evil.example/anchor-quoted.png" } --> B@{ &b !!str img: "https://evil.example/combined.png" }',
    "```",
    "",
    "```mermaid Block anchored key",
    "flowchart TD",
    "  A@{",
    "    label: x",
    '    &k img: "https://evil.example/block-anchor.png"',
    "  }",
    "```",
    "",
    "```mermaid Safe anchored shape",
    "flowchart LR",
    "  A@{ &s shape: rect, label: 'ok' } --> B[Done]",
    "```",
  ].join("\n");

  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `Mermaid YAML anchor/tag guard ${Date.now()}`,
  });
  try {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${task.id}/comments`,
      { body },
    );
    await page.goto(`task/${task.id}`);

    for (const name of [
      "Anchored key",
      "Tagged key",
      "Verbatim tagged key",
      "Anchored quoted and combined key",
      "Block anchored key",
    ]) {
      const region = page.getByRole("region", { name, exact: true });
      await expect(region.getByRole("status")).toContainText(
        "Diagram could not be rendered",
        { timeout: 20_000 },
      );
    }

    const safe = page.getByRole("region", { name: "Safe anchored shape" });
    await expect(safe.locator("svg")).toBeVisible({ timeout: 20_000 });
    await expect(safe.locator("svg")).toContainText("Done");

    expect(leaked).toEqual([]);
  } finally {
    await fetch(`${demoServer}/api/tasks/${task.id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
