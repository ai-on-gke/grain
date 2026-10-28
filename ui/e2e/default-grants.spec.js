import { expect, test } from "./fixtures.js";

// A capability server method with arguments set as a deployment default
// capability, from Settings → Tasks' own GrantsPicker, against grain
// demo's capability server (pkg/demo/demo_capability_server.go): opening
// + New task afterwards starts the task with that grant, drawn as an
// inline section whose arguments can be edited there and then.

const CAP = "https://capabilities.demo.grain.example/cloud";
const KEY = `${CAP}#cloud-key`;

test("a default method grant is an editable section on a new task", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/settings")).json();
  const original = before.defaultCapabilities || [];
  try {
    await page.goto("/settings/tasks");
    const defaults = page.getByTestId("settings-default-grants");
    await defaults.getByRole("button", { name: "Add grant…" }).click();
    await page.getByRole("menuitem", { name: /cloud-key/ }).click();
    const draft = defaults.locator(`[data-method="${KEY}"] [data-grant-draft]`);
    await expect(draft).toBeVisible();
    await draft.locator("[id*='-args-'][id$='-account']").first().click();
    await page.getByRole("option", { name: "staging", exact: true }).click();
    await draft.getByLabel(/^project/).fill("acme-staging");
    // Valid: the section saves itself into the deployment's defaults.
    await expect(defaults.locator(`[data-grant^="${KEY}="]`)).toHaveCount(1);
    await expect
      .poll(async () => {
        const s = await (await request.get("/api/settings")).json();
        return (s.defaultCapabilities || []).filter((c) => c.startsWith(KEY));
      })
      .toEqual([
        `${KEY}={"account":"staging","project":"acme-staging","read_only":true,"scopes":["cloud-platform"],"ttl_minutes":60}`,
      ]);
    // The legacy picker beside it does not list the grant.
    await expect(
      page.locator('[data-setting-id="defaultCapabilities"]'),
    ).not.toContainText("cloud-key");

    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await page.getByLabel(/^Repos/).selectOption("none");
    const grants = page.getByTestId("new-task-grants");
    const section = grants.locator(`[data-grant^="${KEY}="]`);
    await expect(section).toHaveCount(1);
    await expect(section).toHaveAttribute(
      "data-grant",
      /"account":"staging","project":"acme-staging"/,
    );
    const project = section.getByLabel(/^project/);
    await expect(project).toHaveValue("acme-staging");
    await project.fill("acme-other");
    await expect(grants.locator(`[data-grant^="${KEY}="]`)).toHaveAttribute(
      "data-grant",
      /"project":"acme-other"/,
    );
  } finally {
    await request.put("/api/settings", {
      data: { defaultCapabilities: original },
    });
  }
});
