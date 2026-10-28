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

import http from "node:http";
import { test, expect } from "./fixtures.js";

// The UI/API perimeter walkthrough of 2026-10-01
// (docs/test-runs/2026-10-01-ui-api-perimeter-hardening.md), kept as a
// spec: the CSP, Host guard, CSRF, attachment-serving and body-limit
// hardening checked from a real browser against a real `grain demo`.
// Nothing of grain's is mocked. The one known console error -- the demo
// has no GitHub, so GET .../github-branches answers 404 -- is the only
// one tolerated, and only by its URL (with example.invalid's images,
// which are meant not to load).

const PNG_1X1 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC";
const b64 = (s) => Buffer.from(s).toString("base64");

const HARDENING_HEADERS = {
  "x-frame-options": "DENY",
  "x-content-type-options": "nosniff",
  "referrer-policy": "same-origin",
};

// rawGet issues a GET with a Host header of our choosing, which neither
// fetch() nor Playwright's request context will forge, showing the
// demo's access code as the CLI does.
function rawGet(baseURL, path, host, accessCode) {
  const u = new URL(baseURL);
  return new Promise((resolve, reject) => {
    const req = http.request(
      {
        host: u.hostname,
        port: u.port,
        path,
        headers: { Host: host, Authorization: `Bearer ${accessCode}` },
      },
      (res) => {
        res.resume();
        res.on("end", () => resolve(res.statusCode));
      },
    );
    req.on("error", reject);
    req.end();
  });
}

// serveAttacker serves one HTML page from a second local port, under the
// name "localhost" -- a different site from the daemon's 127.0.0.1.
async function serveAttacker(html) {
  const server = http.createServer((req, res) => {
    res.writeHead(200, { "Content-Type": "text/html" });
    res.end(html);
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return {
    url: `http://localhost:${server.address().port}/`,
    close: () => new Promise((r) => server.close(r)),
  };
}

function watchPage(page) {
  const problems = [];
  page.on("console", (msg) => {
    if (msg.type() !== "error") return;
    const at = msg.location()?.url || "";
    if (at.endsWith("/github-branches")) return;
    // The Markdown images point at a name that never resolves; the
    // browser logging that is not grain's doing.
    if (at.includes("example.invalid")) return;
    problems.push(`console: ${msg.text()}`);
  });
  page.on("dialog", (d) => {
    problems.push(`dialog: ${d.message()}`);
    d.dismiss();
  });
  return problems;
}

test.describe("UI/API perimeter walkthrough", () => {
  let task;

  test.beforeAll(async ({ request }) => {
    const res = await request.post("/api/tasks", {
      data: {
        title: "Perimeter walkthrough",
        repo: "acme/widgets",
        description: [
          "![over https](https://example.invalid/a.png)",
          "![over http](http://example.invalid/b.png)",
          "[external](https://example.invalid) [internal](/ci) [js](javascript:alert(1))",
          "<img src=x onerror=alert(1)>",
        ].join("\n\n"),
        attachments: [
          {
            filename: "evil.html",
            contentType: "text/html",
            content: b64("<script>alert('html')</script>"),
          },
          {
            filename: "evil.svg",
            contentType: "image/svg+xml",
            content: b64(
              '<svg xmlns="http://www.w3.org/2000/svg"><script>alert("svg")</script></svg>',
            ),
          },
          { filename: "dot.png", contentType: "image/png", content: PNG_1X1 },
        ],
      },
    });
    expect(res.status()).toBe(201);
    task = await res.json();
    const detail = await (await request.get(`/api/tasks/${task.id}`)).json();
    task.attachments = Object.fromEntries(
      detail.attachments.map((a) => [a.filename, a.id]),
    );
  });

  test("every response class carries the security headers", async ({
    request,
  }) => {
    for (const path of [
      "/",
      "/api/config",
      "/api/no-such-route",
      `/api/tasks/${task.id}/attachments/${task.attachments["dot.png"]}`,
    ]) {
      const res = await request.get(path);
      const h = res.headers();
      const csp = h["content-security-policy"];
      if (path.includes("/attachments/")) {
        // An attachment is a document of its own, held to a stricter
        // policy than the UI's: nothing loads, nothing runs.
        expect(csp, path).toContain("default-src 'none'");
        expect(csp, path).toContain("sandbox");
      } else {
        expect(csp, path).toContain("script-src 'self'");
        expect(csp, path).toContain("frame-ancestors 'none'");
      }
      for (const [name, want] of Object.entries(HARDENING_HEADERS)) {
        expect(h[name], `${path} ${name}`).toBe(want);
      }
    }
  });

  test("the main views raise no CSP violation or console error", async ({
    page,
  }) => {
    const problems = watchPage(page);
    await page.addInitScript(() => {
      document.addEventListener("securitypolicyviolation", (e) =>
        console.error(`CSP ${e.violatedDirective} ${e.blockedURI}`),
      );
    });
    for (const path of [
      "/",
      "/boards",
      `/tasks/${task.id}`,
      "/ci",
      "/repos",
      "/repos/acme/widgets",
      "/teams/default",
      "/settings",
      "/system",
      "/merge-queue",
    ]) {
      await page.goto(path);
      await page.waitForLoadState("networkidle");
    }
    expect(problems).toEqual([]);
  });

  test("a Host the daemon does not answer to is refused", async ({
    baseURL,
    accessCode,
  }) => {
    const get = (host) => rawGet(baseURL, "/api/config", host, accessCode);
    expect(await get("evil.example")).toBe(421);
    expect(await get("localhost")).toBe(200);
    expect(await get("localhost:8420")).toBe(200);
  });

  test("a cross-site page cannot drive the daemon, the UI and CLI still can", async ({
    page,
    request,
    baseURL,
  }) => {
    const victim = await (
      await request.post("/api/tasks", {
        data: { title: "CSRF victim", repo: "acme/widgets" },
      })
    ).json();
    const attacker = await serveAttacker(`<!doctype html>
      <form id="f" method="POST" enctype="text/plain" target="sink"
            action="${baseURL}/api/tasks/${victim.id}/close">
        <input name='{"x":"' value='"}'></form>
      <iframe name="sink"></iframe>
      <script>
        const post = (path, body) => fetch("${baseURL}" + path, {
          method: "POST", mode: "no-cors",
          headers: { "Content-Type": "text/plain" }, body });
        window.done = Promise.allSettled([
          post("/api/tasks", JSON.stringify({ title: "csrf", repo: "acme/widgets" })),
          post("/api/tasks/${victim.id}/close", "{}"),
        ]);
        document.getElementById("f").submit();
      </script>`);
    const answers = [];
    page.on("response", (r) => {
      if (r.url().startsWith(baseURL)) answers.push(r.status());
    });
    try {
      await page.goto(attacker.url);
      await page.evaluate(() => window.done);
      await expect.poll(() => answers.length).toBe(3);
    } finally {
      await attacker.close();
    }
    expect(answers).toEqual([403, 403, 403]);
    const tasks = await (await request.get("/api/tasks")).json();
    expect(tasks.map((t) => t.title)).not.toContain("csrf");
    expect(
      (await (await request.get(`/api/tasks/${victim.id}`)).json()).state,
    ).not.toBe("closed");

    // The real UI, same-origin, still closes it.
    await page.goto(`/tasks/${victim.id}`);
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Reopen", exact: true }),
    ).toBeVisible();

    // And a client with no Origin -- the CLI, curl -- still writes.
    const created = await request.post("/api/tasks", {
      data: { title: "Filed with no Origin", repo: "acme/widgets" },
    });
    expect(created.status()).toBe(201);
  });

  test("HTML and SVG attachments download; a PNG previews", async ({
    page,
    context,
  }) => {
    const problems = watchPage(page);
    for (const name of ["evil.html", "evil.svg"]) {
      const tab = await context.newPage();
      const tabProblems = watchPage(tab);
      const download = tab.waitForEvent("download");
      await tab
        .goto(`/api/tasks/${task.id}/attachments/${task.attachments[name]}`)
        .catch(() => {}); // "Download is starting" -- the point.
      expect((await download).suggestedFilename()).toBe(name);
      expect(tabProblems).toEqual([]);
      await tab.close();
    }

    await page.goto(`/tasks/${task.id}`);
    await page.getByText("dot.png", { exact: true }).click();
    const img = page.getByRole("dialog").locator('img[alt="dot.png"]');
    await expect
      .poll(() => img.evaluate((e) => e.complete && e.naturalWidth))
      .toBe(1);
    await page.getByRole("button", { name: "Close image" }).click();

    // SVG is a link to its download, not the viewer (which would draw
    // the octet-stream it is served as as a broken image).
    await expect(page.getByRole("link", { name: "evil.svg" })).toHaveAttribute(
      "href",
      `/api/tasks/${task.id}/attachments/${task.attachments["evil.svg"]}`,
    );
    expect(problems).toEqual([]);
  });

  test("oversized bodies are 413; the largest attachment is accepted", async ({
    request,
  }) => {
    const nine = "x".repeat(9 << 20);
    const put = await request.put("/api/settings", {
      data: { description: nine },
    });
    expect(put.status()).toBe(413);
    const create = await request.post("/api/tasks", {
      data: { title: "t", repo: "acme/widgets", description: nine },
    });
    expect(create.status()).toBe(413);

    const max = Buffer.alloc(10 << 20, 7).toString("base64");
    const ok = await request.post("/api/tasks", {
      data: {
        title: "max attachment",
        repo: "acme/widgets",
        attachments: [
          {
            filename: "max.bin",
            contentType: "application/octet-stream",
            content: max,
          },
        ],
      },
    });
    expect(ok.status()).toBe(201);
  });
});
