import { expect, test } from "./fixtures.js";

// Settings → Sandboxes → Custom sandbox image: the OCI reference box,
// the FROM line for the active backend's minimal base image, and the
// sandbox contract a custom image is checked against
// (pkg/sandboximage). The Dockerfile snippet is the one wide thing on
// the card -- a long base image reference -- and it scrolls inside its
// own box rather than pushing the page sideways on a phone.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`the custom sandbox image card fits a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/sandboxes#custom-sandbox-image");

    const input = page.getByLabel("Custom sandbox image (OCI reference)", {
      exact: true,
    });
    await expect(input).toBeVisible();
    await expect(input).toHaveAttribute("placeholder", /.+/);

    const dockerfile = page.getByTestId("sandbox-image-dockerfile");
    await expect(dockerfile).toContainText(
      /FROM \S+(guest-base|k8s-sandbox-base)/,
    );
    await expect(page.getByTestId("sandbox-contract-versions")).toContainText(
      "dev.grain.sandbox.contract = ",
    );
    // The base image already carries the contract: the card says so
    // rather than listing it as a checklist for the user to set up.
    await expect(page.getByTestId("sandbox-contract-checklist")).toHaveCount(0);
    await expect(page.getByTestId("sandbox-image-inherits")).toContainText(
      /(guest-base|k8s-sandbox-base) already satisfies the entire sandbox contract/,
    );
    await expect(page.getByTestId("sandbox-image-todo")).toContainText(
      "start FROM it",
    );
    await expect(page.getByTestId("sandbox-contract-reference")).toContainText(
      "docs/sandbox-image-contract.md",
    );

    // The card's own contents stay inside the card, and the card inside
    // the viewport.
    const card = page.locator("#custom-sandbox-image");
    const cardBox = await card.boundingBox();
    expect(cardBox).not.toBeNull();
    expect(cardBox.x).toBeGreaterThanOrEqual(0);
    expect(cardBox.x + cardBox.width).toBeLessThanOrEqual(viewport.width + 1);
    for (const inner of [input, dockerfile]) {
      const box = await inner.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(cardBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
  });
}

test("saves, reloads, refuses invalid references and clears the custom sandbox image", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await page.goto("/settings/sandboxes#custom-sandbox-image");

  const input = page.getByLabel("Custom sandbox image (OCI reference)", {
    exact: true,
  });

  await input.fill("ghcr.io/acme/custom-sandbox:v1");
  const savedResp = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "PUT",
  );
  await input.press("Enter");
  expect((await savedResp).status()).toBe(200);

  await page.reload();
  await expect(input).toHaveValue("ghcr.io/acme/custom-sandbox:v1");

  for (const invalid of [
    "a b",
    "-flag",
    "https://ghcr.io/acme/sandbox:v1",
    "http://bad",
    "img;rm",
  ]) {
    await input.fill(invalid);
    const rejectedResp = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "PUT",
    );
    await input.press("Enter");
    const resp = await rejectedResp;
    expect(resp.status(), `expected 400 for ${invalid}`).toBe(400);
    const body = await resp.json();
    expect(body.error).toMatch(/sandboxImage must be one OCI image reference/);
    await expect(
      page.getByText("Couldn't save Custom sandbox image (OCI reference)"),
    ).toBeVisible();
  }

  await input.fill("");
  const clearedResp = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "PUT",
  );
  await input.press("Enter");
  expect((await clearedResp).status()).toBe(200);

  await page.reload();
  await expect(input).toHaveValue("");
});
