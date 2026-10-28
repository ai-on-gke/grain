import { expect, test } from "./fixtures.js";

// A dependency with a long title in the task pane's "Depends on" list
// (src/components/detail/Dependencies.jsx). The chip is one row: the
// depended-on task's id and title, then the × that detaches it. A title
// longer than the pane used to push the row wider than the pane, which
// is a <fieldset> -- and a fieldset's min-width is its content's
// min-content width unless something says otherwise -- so the × ended up
// off the right edge of the screen and the dependency could not be
// removed. The title is the part that gives way now: it ellipsizes, and
// the id and × keep their room.
//
// Two titles, because they break differently: an ordinary long sentence,
// and one carrying a long unbroken token (a path, a URL) with nowhere to
// wrap at all.
//
// Like narrow.spec.js this is flexbox arithmetic against a real viewport,
// which jsdom cannot do: the component test next door only proves the ×
// is in the DOM.

const SIZES = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

const LONG_TITLE =
  "Substrate bug hunt (CTL): control plane — ateapi, atecontroller, api/, " +
  "pkg/client [report only] — and then a good deal more of it, enough to " +
  "outrun any pane this could be shown in";
const UNBROKEN_TITLE =
  "Fix https://github.com/agent-substrate/substrate/blob/main/pkg/" +
  "atecontroller/reconcile/sandbox_lifecycle_reconciler_with_a_long_name.go";

test.describe("Depends on with long titles", () => {
  let child;
  const deps = [];

  test.beforeAll(async ({ request }) => {
    for (const title of [LONG_TITLE, UNBROKEN_TITLE]) {
      const res = await request.post("/api/tasks", {
        data: { title, description: "A prerequisite.", repo: "acme/widgets" },
      });
      expect(res.ok()).toBeTruthy();
      deps.push(await res.json());
    }
    const res = await request.post("/api/tasks", {
      data: {
        title: "Depends on two long-titled tasks",
        description: "Waits on both.",
        repo: "acme/widgets",
        dependsOn: deps.map((d) => d.id),
      },
    });
    expect(res.ok()).toBeTruthy();
    child = await res.json();
    expect(child.dependsOn).toEqual(deps.map((d) => d.id));
  });

  for (const [name, viewport] of Object.entries(SIZES)) {
    test(`keeps each Remove control on screen at ${name} width`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto(`/tasks/${child.id}`);
      const dialog = page.locator(".MuiDialog-paper");
      const fieldset = dialog
        .locator("fieldset")
        .filter({ has: page.locator("legend", { hasText: "Depends on" }) });
      await expect(fieldset.locator(".MuiChip-root")).toHaveCount(2);

      const pane = await dialog.boundingBox();
      for (const dep of deps) {
        const remove = fieldset.getByTitle(`Remove dependency on ${dep.id}`);
        await expect(remove).toBeVisible();
        const box = await remove.boundingBox();
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
        expect(box.x + box.width).toBeLessThanOrEqual(pane.x + pane.width);

        // The title gave way rather than the row: the chip ends inside
        // the fieldset, and its label is cut short, not laid out whole.
        const chip = fieldset
          .locator(".MuiChip-root")
          .filter({ has: page.getByTitle(`Remove dependency on ${dep.id}`) });
        const [chipBox, fieldBox] = [
          await chip.boundingBox(),
          await fieldset.boundingBox(),
        ];
        expect(chipBox.x + chipBox.width).toBeLessThanOrEqual(
          fieldBox.x + fieldBox.width + 0.5,
        );
        const title = chip.locator(".dep-title");
        expect(
          await title.evaluate((el) => el.scrollWidth > el.clientWidth),
        ).toBe(true);
        const id = await chip.locator(".dep-id").boundingBox();
        expect(id.x).toBeGreaterThanOrEqual(chipBox.x);
      }

      // Nothing in the pane scrolls sideways to reach it either.
      expect(
        await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
      ).toBe(true);

      // And it is the real control: clicking it detaches that dependency
      // and nothing else.
      const first = deps[0];
      await fieldset.getByTitle(`Remove dependency on ${first.id}`).click();
      await expect(fieldset.locator(".MuiChip-root")).toHaveCount(1);

      // The picker below lists the same long titles -- the one just
      // removed is offered again -- and its rows end inside the pane too.
      await fieldset.getByPlaceholder("Add a dependency…").fill("Substrate");
      const option = page
        .locator(".MuiMenuItem-root")
        .filter({ hasText: "Substrate bug hunt" });
      await expect(option).toBeVisible();
      const optionBox = await option.boundingBox();
      expect(optionBox.x + optionBox.width).toBeLessThanOrEqual(
        Math.min(viewport.width, pane.x + pane.width) + 0.5,
      );
      await page.keyboard.press("Escape");

      const res = await page.request.post(`/api/tasks/${child.id}/depends-on`, {
        data: { id: first.id, attach: true },
      });
      expect(res.ok()).toBeTruthy();
    });
  }
});
