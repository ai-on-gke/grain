import path from "node:path";
import { test, expect } from "./fixtures.js";

// Stage (c) of docs/design/settings-panel.md (§6), walked through on a
// real `grain demo` in a real browser. Nothing is stubbed: the board is
// made through POST /api/boards, the Director is the session the demo
// seeds, and the task and comment are filed through the UI's own forms.
//
//   - a browser from before the stage -- grain.username = "alice", her
//     pinned board under grain.pinnedNav.alice.default and a second
//     name's pinned session under grain.pinnedNav.bob.default -- keeps
//     both pins, hers first, once storageMigration.js has run;
//   - every old key is gone and grain.storageVersion is "1";
//   - the rail has no user tile, and its foot is the build stamp;
//   - a task and a comment filed from the UI carry no creator, author or
//     X-Grain-User, and the server records its own actor on both.
test.setTimeout(120_000);

async function shot(page, name) {
  const dir = process.env.USER_TILE_SCREENSHOTS;
  if (!dir) return;
  await page.screenshot({ path: path.join(dir, `${name}.png`) });
}

test("a browser's old per-user pins and name become deployment-wide", async ({
  page,
}) => {
  const stamp = Date.now();
  const res = await page.request.post("/api/boards", {
    data: { name: `Alice's board ${stamp}`, kind: "list", query: "" },
  });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const board = await res.json();

  try {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    // This browser as it stood before the stage: the migration has not
    // run on it, and its lists are under the old per-user keys.
    await page.evaluate(
      ({ boardId }) => {
        localStorage.removeItem("grain.storageVersion");
        localStorage.removeItem("grain.pinned");
        localStorage.setItem("grain.username", "alice");
        localStorage.setItem(
          "grain.pinnedNav.alice.default",
          JSON.stringify([{ kind: "board", id: boardId }]),
        );
        localStorage.setItem(
          "grain.pinnedNav.bob.default",
          JSON.stringify([{ kind: "session", id: "director" }]),
        );
        localStorage.setItem("grain.userTimeZone", "Europe/London");
      },
      { boardId: board.id },
    );
    await page.reload();
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Both pins, in that order, at the top of the rail.
    const aside = page.locator("aside");
    const pinnedBoard = aside
      .getByRole("button", { name: `Alice's board ${stamp}` })
      .first();
    const pinnedDirector = aside
      .getByRole("button", { name: "Director" })
      .first();
    await expect(pinnedBoard).toBeVisible();
    await expect(pinnedDirector).toBeVisible();
    expect((await pinnedBoard.boundingBox()).y).toBeLessThan(
      (await pinnedDirector.boundingBox()).y,
    );

    const stored = await page.evaluate(() => ({ ...localStorage }));
    expect(JSON.parse(stored["grain.pinned"])).toEqual([
      { kind: "board", id: board.id },
      { kind: "session", id: "director" },
    ]);
    expect(stored["grain.storageVersion"]).toBe("1");
    expect(stored["grain.timeZone"]).toBe("Europe/London");
    for (const key of Object.keys(stored)) {
      expect(key).not.toMatch(/^grain\.(pinnedNav|username$|userTimeZone$)/);
    }

    // No user tile; the build stamp is the foot.
    await expect(aside.getByTestId("user-tile")).toHaveCount(0);
    await expect(aside.getByText("alice", { exact: true })).toHaveCount(0);
    const buildStamp = aside.getByTestId("build-stamp");
    await expect(buildStamp).toBeVisible();
    await buildStamp.scrollIntoViewIfNeeded();
    await shot(page, "sidebar-footer-desktop");

    // On a phone the drawer's foot is System, then the build stamp (§4.3).
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByRole("button", { name: "Open navigation" }).click();
    const drawer = page.locator(".MuiDrawer-paper");
    const system = drawer.getByRole("button", { name: "System", exact: true });
    const phoneStamp = drawer.getByTestId("build-stamp");
    await expect(system).toBeVisible();
    await expect(phoneStamp).toBeVisible();
    expect((await phoneStamp.boundingBox()).y).toBeGreaterThan(
      (await system.boundingBox()).y,
    );
    await expect(drawer.getByTestId("user-tile")).toHaveCount(0);
    await shot(page, "sidebar-footer-phone");
  } finally {
    await page.request.delete(`/api/boards/${board.id}`).catch(() => {});
  }
});

test("a task and a comment filed from the UI are the server's actor's", async ({
  page,
}) => {
  const config = await (await page.request.get("/api/config")).json();
  expect(config.actor).toBeTruthy();

  const sent = [];
  page.on("request", (req) => {
    if (req.url().includes("/api/") && req.method() === "POST") {
      sent.push({
        url: req.url(),
        body: req.postData() || "",
        headers: req.headers(),
      });
    }
  });

  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  // A name an older browser still remembers changes nothing.
  await page.evaluate(() => localStorage.setItem("grain.username", "alice"));

  await page.getByRole("button", { name: "+ New task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
  const title = `No username ${Date.now()}`;
  await page.getByLabel("Title", { exact: true }).fill(title);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  const created = page.waitForResponse(
    (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Create task" }).click();
  const filed = await created;
  expect(filed.ok(), await filed.text()).toBe(true);
  const task = await filed.json();

  await page.goto(`/tasks/${task.id}`);
  await page.getByPlaceholder("Reply...").fill("filed with no name");
  const commented = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${task.id}/comments`) &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Comment", exact: true }).click();
  expect((await commented).ok()).toBe(true);

  for (const req of sent) {
    expect(req.headers["x-grain-user"], req.url).toBeUndefined();
    if (req.body.startsWith("{")) {
      const body = JSON.parse(req.body);
      expect(body, req.url).not.toHaveProperty("creator");
      expect(body, req.url).not.toHaveProperty("author");
    }
  }

  const stored = await (await page.request.get(`/api/tasks/${task.id}`)).json();
  expect(stored.creator).toBe(config.actor);
  const comment = (stored.comments || []).find(
    (c) => c.body === "filed with no name",
  );
  expect(comment, JSON.stringify(stored.comments)).toBeTruthy();
  expect(comment.author).toBe(config.actor);
  expect(stored.creator).not.toBe("alice");
});
