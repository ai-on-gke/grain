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

// The classic-only Settings → Hosts and Git controls the 2026-11-13
// connections and infrastructure walkthrough listed
// (docs/test-runs/2026-11-13-connections-infrastructure-settings.md),
// driven on /next/ against a real `grain demo`. Where the demo cannot
// show a state -- a host that is connected now, a rekey request, a
// deployment with no git connection -- one field of the real answer is
// changed in flight; every write goes to the real server unless it says
// otherwise.

const hostWrite = (method, path) => (r) =>
  r.request().method() === method && new URL(r.url()).pathname === path;

test("Hosts: Edit and Rename a host, clearing a blanked capacity and the error it fixed", async ({
  page,
  request,
}) => {
  const before = (await (await request.get("/api/hostds")).json()).find(
    (h) => h.name === "cloudtop-alice",
  );
  expect(before).toBeTruthy();
  // A host that dialled in says from where, as whom and on what build.
  await page.route("**/api/hostds", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const rows = await res.json();
    for (const h of rows) {
      if (h.name === "cloudtop-alice") {
        h.state = "connected";
        h.connection = {
          connected: true,
          host: "10.20.30.40",
          principal: "alice@example.com",
          build: "v9.9.9",
        };
      }
    }
    await route.fulfill({ response: res, json: rows });
  });
  try {
    await page.goto("/next/settings/hostds");
    const row = page.getByTestId("hostd-row-cloudtop-alice");
    await expect(
      page.getByTestId("hostd-connection-cloudtop-alice"),
    ).toHaveText("from 10.20.30.40 · as alice@example.com · v9.9.9");

    await page.getByTestId("edit-hostd-cloudtop-alice").click();
    await page
      .getByTestId("edit-hostd-owner-cloudtop-alice")
      .fill("alice@example.com");
    await page
      .getByTestId("edit-hostd-labels-cloudtop-alice")
      .fill("os=linux,walk=yes");
    const cap = page.getByTestId("edit-hostd-capacity-cloudtop-alice");
    await cap.fill("abc");
    await page.getByTestId("save-edit-hostd-cloudtop-alice").click();
    const banner = page.getByText(
      'Capacity "abc" must be a non-negative whole number',
    );
    await expect(banner).toBeVisible();

    await cap.fill("3");
    let saved = page.waitForResponse(
      hostWrite("PATCH", "/api/hostds/cloudtop-alice"),
    );
    await page.getByTestId("save-edit-hostd-cloudtop-alice").click();
    let res = await saved;
    expect(res.status()).toBe(200);
    expect(JSON.parse(res.request().postData())).toEqual({
      owner: "alice@example.com",
      labels: { os: "linux", walk: "yes" },
      capacity: 3,
    });
    // The save that fixed it takes the error down.
    await expect(banner).toHaveCount(0);
    await expect(page.getByTestId("hostd-capacity-cloudtop-alice")).toHaveText(
      "capacity 3",
    );
    await expect(row).toContainText("walk=yes");

    // A blanked capacity is sent as 0, which clears it.
    await page.getByTestId("edit-hostd-cloudtop-alice").click();
    await page.getByTestId("edit-hostd-capacity-cloudtop-alice").fill("");
    saved = page.waitForResponse(
      hostWrite("PATCH", "/api/hostds/cloudtop-alice"),
    );
    await page.getByTestId("save-edit-hostd-cloudtop-alice").click();
    res = await saved;
    expect(JSON.parse(res.request().postData()).capacity).toBe(0);
    await expect(page.getByTestId("hostd-capacity-cloudtop-alice")).toHaveCount(
      0,
    );

    // Rename.
    await page.getByTestId("rename-hostd-cloudtop-alice").click();
    await page
      .getByTestId("rename-hostd-input-cloudtop-alice")
      .fill("cloudtop-alice-2");
    const renamed = page.waitForResponse(
      hostWrite("POST", "/api/hostds/cloudtop-alice/rename"),
    );
    await page.getByTestId("save-rename-hostd-cloudtop-alice").click();
    res = await renamed;
    expect(res.status()).toBe(200);
    expect(JSON.parse(res.request().postData())).toEqual({
      name: "cloudtop-alice-2",
    });
    await expect(page.getByTestId("hostd-row-cloudtop-alice-2")).toBeVisible();
    await expect(page.getByTestId("hostd-row-cloudtop-alice")).toHaveCount(0);
  } finally {
    const now = await (await request.get("/api/hostds")).json();
    if (now.some((h) => h.name === "cloudtop-alice-2")) {
      await request.post("/api/hostds/cloudtop-alice-2/rename", {
        data: { name: "cloudtop-alice" },
      });
    }
    await request.patch("/api/hostds/cloudtop-alice", {
      data: {
        owner: before.owner || "",
        labels: before.labels || {},
        capacity: before.capacity || 0,
      },
    });
  }
});

test("Hosts: enrollment code options, editable pending requests and rekeys", async ({
  page,
}) => {
  const pending = page.waitForResponse(
    (r) => new URL(r.url()).pathname === "/api/hostds/pending",
  );
  // The demo seeds no rekey request: the first pending one is shown as
  // one, rekeying cloudtop-alice.
  let rekeyID = "";
  await page.route("**/api/hostds/pending", async (route) => {
    const res = await route.fetch();
    const rows = await res.json();
    if (rows.length > 1) {
      rows[1].rekey_hostd = "cloudtop-alice";
      rekeyID = rows[1].id;
    }
    await route.fulfill({ response: res, json: rows });
  });
  await page.goto("/next/settings/hostds");
  const rows = await (await pending).json();
  expect(rows.length).toBeGreaterThan(0);
  const p = rows[0];

  // A new code takes every option classic offers.
  const name = `walk-${Date.now().toString(36)}`;
  await page.getByTestId("input-enrollment-code-name").fill(name);
  await page.getByTestId("input-enrollment-code-expires").fill("7d");
  await page.getByTestId("input-enrollment-code-max-uses").fill("5");
  await page.getByTestId("input-enrollment-code-capacity").fill("2");
  await page.getByTestId("input-enrollment-code-labels").fill("team=walk");
  await page
    .getByTestId("input-enrollment-code-principals")
    .fill("user:a@example.com, org:acme");
  const created = page.waitForResponse(
    hostWrite("POST", "/api/hostds/enrollment-codes"),
  );
  await page.getByTestId("create-enrollment-code-btn").click();
  const res = await created;
  expect(res.ok()).toBe(true);
  expect(JSON.parse(res.request().postData())).toEqual({
    name,
    expires_in: "7d",
    max_uses: 5,
    default_capacity: 2,
    default_labels: { team: "walk" },
    allowed_principals: ["user:a@example.com", "org:acme"],
  });
  const defaults = page.getByTestId("enrollment-code-defaults-" + name);
  await expect(defaults).toContainText("capacity 2");
  await expect(defaults).toContainText("team=walk");
  await expect(defaults).toContainText("allow org:acme");
  await expect(page.getByTestId("enrollment-code-row-" + name)).toContainText(
    "0 / 5",
  );

  // A pending request's name, labels and capacity are the owner's to
  // set before Accept. The accept itself is answered here so the demo's
  // seeded request stays for the other specs.
  await page.getByTestId("pending-name-" + p.id).fill("walk-devbox");
  await page.getByTestId("pending-labels-" + p.id).fill("os=linux");
  await page.getByTestId("pending-capacity-" + p.id).fill("");
  let acceptBody = null;
  await page.route(`**/api/hostds/pending/${p.id}/accept`, async (route) => {
    acceptBody = JSON.parse(route.request().postData());
    await route.fulfill({ json: { name: "walk-devbox" } });
  });
  await page.getByTestId("accept-pending-" + p.id).click();
  await expect
    .poll(() => acceptBody)
    .toEqual({
      name: "walk-devbox",
      labels: { os: "linux" },
      capacity: 0,
    });

  if (rekeyID) {
    await expect(page.getByTestId("pending-rekey-" + rekeyID)).toHaveText(
      "rekey: cloudtop-alice",
    );
    const nameBox = page.getByTestId("pending-name-" + rekeyID);
    await expect(nameBox).toBeDisabled();
    await expect(nameBox).toHaveValue("cloudtop-alice");
  }

  // The legacy per-host token form keeps classic's guidance and owner.
  await expect(page.getByTestId("hostd-legacy-copy")).toContainText(
    "Use a per-host token only for non-interactive setups",
  );
  await expect(page.getByTestId("input-new-hostd-owner")).toBeVisible();
});

test("Git: per-connection Test, proxy, Reuse authentication from, and the refusal banner", async ({
  page,
  request,
}) => {
  const host = "proxy.walk.example.test";
  const cleanup = async () => {
    const conns = await (
      await request.get("/api/deployment/git-connections")
    ).json();
    for (const cred of new Set(
      conns.filter((c) => c.host === host).map((c) => c.credential),
    )) {
      await request.delete(
        "/api/deployment/git-connections/" + encodeURIComponent(cred),
      );
    }
  };
  await page.goto("/next/settings/git");
  const rows = page.locator('table[aria-label="Git connections"] tbody tr');
  try {
    // A proxy with a login in it is refused, and said beside the form.
    await page.getByTestId("input-git-conn-host").fill(host);
    await page.getByTestId("select-git-conn-kind").click();
    await page.getByTestId("select-git-conn-kind-opt-git").click();
    await page.getByTestId("input-git-conn-secret").fill("walk-token-not-real");
    const proxy = page.getByTestId("input-git-conn-proxy");
    await proxy.fill("http://user:pw@proxy.example.test:3128");
    let saved = page.waitForResponse(
      hostWrite("POST", "/api/deployment/git-connections"),
    );
    await page.getByTestId("save-git-conn-btn").click();
    let res = await saved;
    expect(res.status()).toBe(400);
    const refusal = page.getByTestId("git-connections-error");
    await expect(refusal).toContainText("carries a login");

    // The fixed save goes through and takes the refusal down.
    await proxy.fill("http://proxy.example.test:3128");
    if ((await page.getByTestId("input-git-conn-secret").inputValue()) === "") {
      await page
        .getByTestId("input-git-conn-secret")
        .fill("walk-token-not-real");
    }
    saved = page.waitForResponse(
      hostWrite("POST", "/api/deployment/git-connections"),
    );
    await page.getByTestId("save-git-conn-btn").click();
    res = await saved;
    expect(res.status()).toBe(200);
    expect(JSON.parse(res.request().postData())).toMatchObject({
      host,
      kind: "git",
      proxy: "http://proxy.example.test:3128",
    });
    await expect(refusal).toHaveCount(0);
    const row = rows.filter({ hasText: host });
    await expect(row).toHaveCount(1);
    await expect(row).toContainText("via proxy http://proxy.example.test:3128");

    // Test checks that one connection: its host, credential and scope.
    const cred = (
      await (await request.get("/api/deployment/git-connections")).json()
    ).find((c) => c.host === host).credential;
    const checked = page.waitForRequest(
      (r) =>
        r.method() === "POST" &&
        new URL(r.url()).pathname === "/api/deployment/git-connections/check",
    );
    await page.getByTestId("test-git-conn-" + cred).click();
    const body = JSON.parse((await checked).postData());
    expect(body).toMatchObject({ host, credential: cred });
    const result = page.getByTestId("git-conn-test-result");
    await expect(result).toBeVisible();
    await expect(result).toContainText("Test " + cred);

    // Reuse authentication from names each stored credential with the
    // hosts it serves, and picks its kind with it.
    await page.getByTestId("select-git-conn-mode").click();
    await page.getByTestId("select-git-conn-mode-opt-existing").click();
    await page.getByTestId("select-git-conn-reuse").click();
    const opt = page.getByTestId("select-git-conn-reuse-opt-" + cred);
    await expect(opt).toContainText(`${host} · git host · all repos (pat)`);
    await expect(opt).toContainText(cred);
    await opt.click();
    await expect(page.getByTestId("input-git-conn-name")).toHaveValue(cred);

    // The pointer to where tokens live.
    const note = page.getByTestId("git-secret-note").getByRole("link");
    await expect(note).toHaveAttribute(
      "href",
      "/next/settings/capabilities#credentials",
    );
  } finally {
    await cleanup();
  }
});

test("No git connection: New task and Repos say so and link to Settings → Git", async ({
  page,
}) => {
  const strip = (d) => {
    if (!d || typeof d !== "object") return d;
    delete d.gitConnections;
    delete d.gitCredential;
    d.hasGitCredential = false;
    return d;
  };
  // A request still in flight when the page navigates away (Repos to the
  // Inbox below) has its response disposed under the handler. That one no
  // longer matters to anything on screen, so it is dropped rather than
  // failing the test; every other error still does.
  const gone = (err) =>
    /disposed|closed|Target page, context or browser has been closed/.test(
      String(err?.message ?? err),
    );
  const rewrite = (edit) => async (route) => {
    try {
      const res = await route.fetch();
      await route.fulfill({ response: res, json: edit(await res.json()) });
    } catch (err) {
      if (!gone(err)) throw err;
    }
  };
  await page.route("**/api/deployment", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return rewrite(strip)(route);
  });
  await page.route(
    "**/api/config",
    rewrite((cfg) => {
      strip(cfg.deployment);
      return cfg;
    }),
  );

  await page.goto("/next/settings/repositories");
  const notice = page.getByTestId("no-git-connection");
  await expect(notice).toContainText("no git connection yet");
  await expect(notice).toContainText("SSH key");
  await expect(notice).toContainText("token");
  await expect(notice.getByRole("link")).toHaveAttribute(
    "href",
    "/next/settings/git",
  );

  await page.goto("/next/inbox");
  await page.getByRole("button", { name: /^New/ }).first().click();
  const sheetNotice = page.getByTestId("no-git-connection");
  await expect(sheetNotice).toBeVisible();
  await sheetNotice.getByRole("link").click();
  await expect(page).toHaveURL(/\/next\/settings\/git$/);
});

// The new fields and banners stay inside their cards at every width: the
// tables scroll in their own wrap, and nothing else pushes the page sideways.
test("Hosts and Git controls fit their cards @phone @dark", async ({
  page,
}) => {
  const fits = async (cards) => {
    for (const id of cards) {
      const card = page.getByTestId(id);
      await expect(card).toBeVisible();
      const outside = await card.evaluate((el) => {
        const box = el.getBoundingClientRect();
        const bad = [];
        for (const n of el.querySelectorAll(
          ".s-field, .s-banner, .s-hint, .s-subhead, form, button",
        )) {
          if (n.closest(".s-table-wrap")) continue;
          const r = n.getBoundingClientRect();
          if (r.width === 0) continue;
          if (r.left < box.left - 1 || r.right > box.right + 1) {
            bad.push(`${n.className || n.tagName}: ${r.left}-${r.right}`);
          }
        }
        return bad;
      });
      expect(outside, id).toEqual([]);
    }
    const sideways = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth + 1,
    );
    expect(sideways).toBe(false);
  };
  await page.goto("/next/settings/hostds");
  await fits(["card-hostds-list", "card-hostds-enrollment"]);
  await page.goto("/next/settings/git");
  await fits(["card-git-connections"]);
});
