import { expect, test } from "./fixtures.js";

// The gchat capability end to end (docs/gchat.md), against grain demo's
// gchat server: grain's own built-in, under its canonical capability_id,
// configured by Settings' schema form like any other server. Its only
// stub is the external boundary, Google Chat itself: the demo's fakeChat
// (pkg/demo/demo_capability_server.go) answers every webhook post with
// the message it created, and nothing leaves the machine. The demo runs
// with -secrets-store so the webhook URL, a write-only field, really
// reaches the encrypted store.
//
// The flow configures one webhook space, tests the server -- its report
// names the space -- sends a test message from the space's row and sees
// it counted, then grants gchat#post on a new task with {space} picked
// from the grant form's enum of configured spaces.

test.use({ demoArgs: ["-secrets-store"] });

const GCHAT = "https://grain.dev/capabilities/gchat";
const POST = `${GCHAT}#post`;
const WEBHOOK =
  "https://chat.googleapis.com/v1/spaces/DEMO/messages?key=demo-key&token=demo-token";

function field(scope, path) {
  return scope.locator(`[data-arg="${path}"]`);
}

async function pick(page, scope, path, option) {
  await scope
    .locator(".MuiFormControl-root", {
      has: page.locator(`[data-arg="${path}"]`),
    })
    .last()
    .getByRole("combobox")
    .click();
  await page.getByRole("option", { name: option, exact: true }).click();
}

test("configures a webhook space, sends a test message, and grants gchat#post for it", async ({
  page,
  request,
}) => {
  await page.goto("/settings/capabilities#capability-servers");
  await page.locator(`[data-capability-server-row="${GCHAT}"]`).click();
  const card = page.locator(`[data-builtin="gchat"]`);
  await expect(card).toBeVisible();
  await expect(card.getByTestId("gchat-space-list")).toContainText(
    "No spaces yet",
  );

  await card.getByRole("button", { name: "Edit server settings" }).click();
  const config = card.getByTestId("capability-server-config");
  await field(config, "grain_url").fill("https://grain.demo.example");
  await config
    .locator(`[data-arg-list="spaces"]`)
    .getByRole("button", { name: "Add spaces" })
    .click();
  await field(config, "spaces[0].name").fill("team-room");
  await pick(page, config, "spaces[0].transport", "webhook");
  await config
    .getByLabel(/^Webhook URLs/)
    .fill(JSON.stringify({ "team-room": WEBHOOK }));
  await config.getByRole("button", { name: "Save server settings" }).click();
  await expect(config).toHaveCount(0);

  // The webhook URL is kept as a secret reference, never in the config.
  const listed = await (await request.get("/api/capability-servers")).json();
  const saved = listed.find((s) => s.id === GCHAT);
  expect(saved.config.config.webhook_urls).toMatch(/^\$secret:/);
  expect(JSON.stringify(saved)).not.toContain("demo-token");

  // Test: the server settings' report names the space and its transport.
  await card.getByRole("button", { name: "Test", exact: true }).click();
  await expect(card.getByTestId("capability-test-result")).toContainText(
    "space team-room: webhook",
  );

  // The space's row, once the server has attached the new configuration.
  const row = card.locator(`[data-gchat-space="team-room"]`);
  await expect(row).toBeVisible();
  await expect(row.getByTestId("gchat-space-usage")).toContainText("0 posted");
  await row.getByRole("button", { name: "Send test message" }).click();
  await expect(row.getByTestId("gchat-test-result")).toContainText(
    "Posted spaces/DEMO/messages/demo-",
  );
  await expect(row.getByTestId("gchat-space-usage")).toContainText("1 posted");
  await expect(row.getByTestId("gchat-space-usage")).toContainText(
    "1 / 60 this hour",
  );
  // The button sits on the row's right, beside the space's name.
  const nameBox = await row.locator("h5").boundingBox();
  const buttonBox = await row
    .getByRole("button", { name: "Send test message" })
    .boundingBox();
  const rowBox = await row.boundingBox();
  expect(buttonBox.x).toBeGreaterThan(nameBox.x + nameBox.width);
  expect(buttonBox.x + buttonBox.width).toBeLessThanOrEqual(
    rowBox.x + rowBox.width,
  );

  // gchat#post is granted with {space} from the configured spaces.
  await page.goto("/");
  const title = `E2E gchat post ${Date.now()}`;
  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("none");
  const grants = page.getByTestId("new-task-grants");
  await grants.getByRole("button", { name: "Add grant…" }).click();
  await page
    .getByRole("menuitem", { name: /grain\.dev\/capabilities\/gchat/ })
    .click();
  // The method is a section inline with its {space} select; picking
  // the space saves the grant (there is no dialog to confirm).
  const section = grants.locator(`[data-method="${POST}"] .grant-section`);
  await expect(section).toHaveCount(1);
  await pick(page, section, "space", "team-room");
  await expect(grants.locator(`[data-grant^="${POST}="]`)).toHaveAttribute(
    "data-grant",
    /"space":"team-room"/,
  );

  await page.getByRole("button", { name: "Create task" }).click();
  await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(0);
  const list = await (await request.get("/api/tasks")).json();
  const tasks = Array.isArray(list) ? list : list.tasks;
  const filed = tasks.find((t) => t.title === title);
  expect(filed).toBeTruthy();
  const held = (filed.capabilities || []).filter((c) => c.startsWith(POST));
  expect(held).toEqual([`${POST}={"space":"team-room"}`]);
});
