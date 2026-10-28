import { test, expect } from "./fixtures.js";

// Leaving a repo page with an unsaved job box (found by the 2026-10-12
// repository, release and memory walkthrough): "Jobs dispatched after
// every merge" saves only on its own button, and the repo page is not an
// Overlay, so "← Repos", the rail and Back all dropped a typed value
// without a word. Now each asks the question the panes ask
// (unsaved-edits-guard.spec.js), and saying no keeps the page and the
// value. A reload or a closed tab gets the browser's own question.

const PROMPT = /unsaved changes/;
const LABEL = "Jobs dispatched after every merge";

test.describe("Leaving a repo page with an unsaved job box", () => {
  test("asks before ← Repos, the rail, Back or a reload drop the value", async ({
    page,
  }) => {
    const dialogs = [];
    page.on("dialog", (d) => dialogs.push(d.message()));

    // The job box is on the page's Releases tab, which the address
    // names; the checks below compare the page's address without it.
    await page.goto("/repos/acme/widgets#releases");
    const box = page.getByLabel(LABEL);
    await expect(box).toBeVisible();
    const saved = await box.inputValue();
    const bare = () => page.url().split("#")[0];
    const repoUrl = bare();

    // Untouched, "← Repos" leaves at once and asks nothing.
    await page.getByRole("button", { name: "← Repos" }).click();
    await expect(box).toHaveCount(0);
    expect(dialogs).toEqual([]);

    // Reopened from the list rather than by address, so the entry Back
    // returns to is this page's own and Back reaches App's popstate: a
    // goto's entry belongs to another document, and leaving that is the
    // browser's beforeunload question, tested below.
    await page
      .getByRole("link", { name: "acme/widgets", exact: true })
      .first()
      .click();
    await page.getByRole("tab", { name: "Releases" }).click();
    await box.fill("ci.yml:unsaved-job");

    // "← Repos": told no, the page and the value both stay.
    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "← Repos" }).click();
    await expect.poll(() => dialogs.length).toBe(1);
    expect(dialogs[0]).toMatch(PROMPT);
    await expect(box).toHaveValue("ci.yml:unsaved-job");
    expect(bare()).toBe(repoUrl);

    // A rail item: the same question, asked once.
    page.once("dialog", (d) => d.dismiss());
    await page
      .getByRole("navigation")
      .getByRole("button", { name: /^Processes/ })
      .click();
    await expect.poll(() => dialogs.length).toBe(2);
    await expect(box).toHaveValue("ci.yml:unsaved-job");
    expect(bare()).toBe(repoUrl);

    // Browser Back: the same question, and staying puts the address back.
    page.once("dialog", (d) => d.dismiss());
    await page.goBack();
    await expect.poll(() => dialogs.length).toBe(3);
    await expect(box).toHaveValue("ci.yml:unsaved-job");
    await expect.poll(bare).toBe(repoUrl);

    // The repo header's own Releases button: the same question.
    page.once("dialog", (d) => d.dismiss());
    await page.getByRole("button", { name: "Releases", exact: true }).click();
    await expect.poll(() => dialogs.length).toBe(4);
    expect(dialogs[3]).toMatch(PROMPT);
    await expect(box).toHaveValue("ci.yml:unsaved-job");
    expect(bare()).toBe(repoUrl);

    // The header rail's System button: the same question.
    page.once("dialog", (d) => d.dismiss());
    await page.getByTestId("top-bar-system").click();
    await expect.poll(() => dialogs.length).toBe(5);
    expect(dialogs[4]).toMatch(PROMPT);
    await expect(box).toHaveValue("ci.yml:unsaved-job");
    expect(bare()).toBe(repoUrl);

    // The same jobs as saved, spelled differently, are nothing to lose.
    await box.fill(` ${saved} `);
    await page.getByRole("button", { name: "← Repos" }).click();
    await expect(box).toHaveCount(0);
    expect(dialogs).toHaveLength(5);

    // Told yes, it leaves and the server never saw the value.
    await page.goto(`${repoUrl}#releases`);
    await box.fill("ci.yml:discarded-job");
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "← Repos" }).click();
    await expect(box).toHaveCount(0);
    expect(dialogs).toHaveLength(6);
    await page.goto(`${repoUrl}#releases`);
    await expect(page.getByLabel(LABEL)).toHaveValue(saved);
  });

  test("a reload or a closed tab gets the browser's own question", async ({
    page,
  }) => {
    await page.goto("/repos/acme/widgets#releases");
    const box = page.getByLabel(LABEL);
    await box.fill("ci.yml:unsaved-job");
    const asked = page.waitForEvent("dialog");
    await page.close({ runBeforeUnload: true });
    const dialog = await asked;
    expect(dialog.type()).toBe("beforeunload");
    await dialog.accept();
  });
});
