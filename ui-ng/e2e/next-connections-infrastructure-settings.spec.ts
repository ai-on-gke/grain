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

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "./fixtures";

test.use({ demoArgs: ["-github-sim", "-secrets-store"] });

function generateThrowawayKeys(): {
  sshPrivateKey: string;
  sshKnownHostsLine: string;
  rsaPrivateKey: string;
  cleanup: () => void;
} {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-e2e-keys-"));
  const sshPath = path.join(dir, "id_ed25519");
  const rsaPath = path.join(dir, "app.pem");
  execFileSync(
    "ssh-keygen",
    ["-t", "ed25519", "-N", "", "-f", sshPath, "-C", "e2e@example.test"],
    { stdio: "ignore" },
  );
  execFileSync("openssl", ["genrsa", "-out", rsaPath, "2048"], {
    stdio: "ignore",
  });
  const pubParts = fs
    .readFileSync(`${sshPath}.pub`, "utf8")
    .trim()
    .split(/\s+/);
  return {
    sshPrivateKey: fs.readFileSync(sshPath, "utf8"),
    sshKnownHostsLine: `gitssh.example.test ${pubParts[0]} ${pubParts[1]}\n`,
    rsaPrivateKey: fs.readFileSync(rsaPath, "utf8"),
    cleanup: () => fs.rmSync(dir, { recursive: true, force: true }),
  };
}

test("Settings -> Git saves plain-git SSH + GitHub App connections, clears refusal banners, and confirms Remove @phone @dark", async ({
  page,
  request,
  axe,
}) => {
  const keys = generateThrowawayKeys();
  const createdCreds: string[] = [];
  try {
    await page.goto("/next/settings/git");
    await expect(page.getByTestId("card-git-connections")).toBeVisible();
    expect(await axe()).toEqual([]);

    // Plain git host disables GitHub App with an explanation and accepts SSH + known_hosts.
    await page.getByTestId("select-git-conn-kind").click();
    await page.getByTestId("select-git-conn-kind-opt-git").click();
    await page.getByTestId("select-git-conn-type").click();
    const appOpt = page.getByTestId("select-git-conn-type-opt-app");
    await expect(appOpt).toHaveAttribute("aria-disabled", "true");
    await expect(appOpt).toContainText("Needs a GitHub host");
    await page.getByTestId("select-git-conn-type-opt-ssh").click();

    await page.getByLabel("Host", { exact: true }).fill("gitssh.example.test");
    await page
      .getByLabel("SSH private key", { exact: true })
      .fill(keys.sshPrivateKey);
    await page
      .getByLabel("Known hosts", { exact: true })
      .fill(keys.sshKnownHostsLine);
    await page.getByRole("button", { name: "Add Git connection" }).click();

    await expect
      .poll(async () => {
        const list = await (
          await request.get("/api/deployment/git-connections")
        ).json();
        return list.find(
          (c: { host?: string }) => c.host === "gitssh.example.test",
        )?.credential;
      })
      .toBeTruthy();
    const afterSsh = await (
      await request.get("/api/deployment/git-connections")
    ).json();
    const sshConn = afterSsh.find(
      (c: { host?: string }) => c.host === "gitssh.example.test",
    );
    createdCreds.push(sshConn.credential);

    // GitHub App: invalid PEM shows refusal banner; valid PEM sends string IDs and clears the banner.
    await page.getByTestId("select-git-conn-kind").click();
    await page.getByTestId("select-git-conn-kind-opt-github").click();
    await page.getByTestId("select-git-conn-type").click();
    await page.getByTestId("select-git-conn-type-opt-app").click();
    await page.getByLabel("Host", { exact: true }).fill("github.com");
    await page.getByLabel("Scope (optional)").fill("e2e-app-org");
    await page
      .getByLabel("Proxy (optional)")
      .fill("http://proxy.example.test:3128");
    await page.getByLabel("GitHub App ID").fill("12345");
    await page.getByLabel("Installation ID").fill("67890");
    await page.getByLabel("App private key (PEM)").fill("not-a-valid-pem");
    await page.getByRole("button", { name: "Add Git connection" }).click();

    const errBanner = page.getByTestId("settings-error-banner");
    await expect(errBanner).toContainText(/no PEM block/i);

    await page.getByLabel("App private key (PEM)").fill(keys.rsaPrivateKey);
    await page.getByRole("button", { name: "Add Git connection" }).click();
    await expect(errBanner).toHaveCount(0);

    const afterApp = await (
      await request.get("/api/deployment/git-connections")
    ).json();
    const appConn = afterApp.find(
      (c: { scope?: string }) => c.scope === "e2e-app-org",
    );
    expect(appConn).toBeTruthy();
    createdCreds.push(appConn.credential);
    await expect(
      page.getByTestId(`git-conn-row-${appConn.credential}`),
    ).toContainText("via http://proxy.example.test:3128");

    // Remove asks first; Cancel keeps the row, and dialog stays inside the viewport with aligned buttons.
    await page
      .getByTestId(`git-conn-row-${sshConn.credential}`)
      .getByRole("button", { name: "Remove" })
      .click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toBeVisible();
    const vp = page.viewportSize()!;
    const dlgBox = (await dialog.boundingBox())!;
    expect(dlgBox.x).toBeGreaterThanOrEqual(0);
    expect(dlgBox.x + dlgBox.width).toBeLessThanOrEqual(vp.width);

    const cancelBox = (await dialog
      .getByRole("button", { name: "Cancel" })
      .boundingBox())!;
    const acceptBox = (await dialog
      .getByRole("button", { name: "Remove", exact: true })
      .boundingBox())!;
    expect(
      Math.abs(
        cancelBox.y +
          cancelBox.height / 2 -
          (acceptBox.y + acceptBox.height / 2),
      ),
    ).toBeLessThanOrEqual(4);

    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
    await expect(
      page.getByTestId(`git-conn-row-${sshConn.credential}`),
    ).toBeVisible();

    await page
      .getByTestId(`git-conn-row-${sshConn.credential}`)
      .getByRole("button", { name: "Remove" })
      .click();
    await dialog.getByRole("button", { name: "Remove", exact: true }).click();
    await expect(
      page.getByTestId(`git-conn-row-${sshConn.credential}`),
    ).toHaveCount(0);
  } finally {
    keys.cleanup();
    for (const cred of createdCreds) {
      await request
        .delete(`/api/deployment/git-connections/${encodeURIComponent(cred)}`)
        .catch(() => undefined);
    }
  }
});

test("Settings -> Capabilities, Engine & Sandboxes enforce confirmations, masked secrets, policy hints, legacy redirects, and real backend/network options", async ({
  page,
  request,
  axe,
}) => {
  const customServerId = "https://capabilities.demo.grain.example/e2e-next-srv";
  try {
    // Legacy redirects land on Capabilities.
    for (const oldPath of [
      "/next/admin/integrations",
      "/next/system/integrations",
      "/next/settings/credentials",
      "/next/settings/secrets",
      "/next/settings/capabilities/gcp",
    ]) {
      await page.goto(oldPath);
      await expect(page.getByTestId("settings-section-heading")).toHaveText(
        "Capabilities",
      );
    }
    expect(await axe()).toEqual([]);

    // Masked secret input + delete confirmation, and custom capability server Remove confirmation.
    const secInput = page.getByLabel("Secret value");
    await expect(secInput).toHaveAttribute("type", "password");
    await expect(secInput).toHaveAttribute("autocomplete", "new-password");

    await page.getByLabel("Secret name").fill("e2e-next-sec");
    await page.getByLabel("Secret key").fill("token");
    await secInput.fill("e2e-throwaway-secret");
    await page.getByRole("button", { name: "Store secret" }).click();
    const secRow = page.getByTestId("secret-row-e2e-next-sec");
    await expect(secRow).toBeVisible();
    await expect(secRow).not.toContainText("e2e-throwaway-secret");

    await page.getByTestId("delete-secret-e2e-next-sec").click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText("Delete secret e2e-next-sec?");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(secRow).toBeVisible();
    await page.getByTestId("delete-secret-e2e-next-sec").click();
    await dialog.getByRole("button", { name: "Delete secret" }).click();
    await expect(secRow).toHaveCount(0);

    const servers = await (await request.get("/api/capability-servers")).json();
    const demoCloud = servers.find(
      (s: { id: string }) =>
        s.id === "https://capabilities.demo.grain.example/cloud",
    );
    await page.getByLabel("Server ID").fill(customServerId);
    await page
      .getByLabel("HTTP URL or stdio cmd")
      .fill(demoCloud.config.serverUrl);
    await page.getByRole("button", { name: "Save capability server" }).click();
    await expect(
      page.getByTestId(`cap-server-row-${customServerId}`),
    ).toBeVisible();

    await page.getByTestId(`remove-cap-server-${customServerId}`).click();
    await expect(dialog).toContainText(customServerId);
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(
      page.getByTestId(`cap-server-row-${customServerId}`),
    ).toBeVisible();
    await page.getByTestId(`remove-cap-server-${customServerId}`).click();
    await dialog.getByRole("button", { name: "Remove", exact: true }).click();
    await expect(
      page.getByTestId(`cap-server-row-${customServerId}`),
    ).toHaveCount(0);

    // Engine: Poll interval placeholder is 10s, Max merges per repo branch placeholder is 5.
    await page.goto("/next/settings/engine");
    await expect(page.getByLabel("Poll interval")).toHaveAttribute(
      "placeholder",
      "10s",
    );
    await expect(page.getByLabel("Max merges per repo branch")).toHaveAttribute(
      "placeholder",
      "5",
    );

    // Sandboxes: real backends (unavailable disabled with reason, None shown) and open/none network access.
    await page.goto("/next/settings/sandboxes");
    await page.getByTestId("select-sandbox-backend").click();
    const noneOpt = page.getByTestId("select-sandbox-backend-opt-none");
    await expect(noneOpt).toHaveAttribute("aria-disabled", "true");
    await expect(noneOpt).toContainText("None (control plane only)");
    await expect(noneOpt).toContainText("disable-native-sandboxes");
    await page.keyboard.press("Escape");

    await page.getByTestId("select-sandbox-network").click();
    await page.getByTestId("select-sandbox-network-opt-none").click();
    await page.getByRole("button", { name: "Save changes" }).click();
    await expect
      .poll(
        async () =>
          (await (await request.get("/api/settings")).json()).networkAccess,
      )
      .toBe("none");

    await page.getByTestId("select-sandbox-network").click();
    await page.getByTestId("select-sandbox-network-opt-open").click();
    await page.getByRole("button", { name: "Save changes" }).click();
    await expect
      .poll(
        async () =>
          (await (await request.get("/api/settings")).json()).networkAccess,
      )
      .toBe("open");
  } finally {
    await request
      .post("/api/capability-servers/remove", { data: { id: customServerId } })
      .catch(() => undefined);
    await request.delete("/api/secrets/e2e-next-sec").catch(() => undefined);
    await request
      .post("/api/settings", { data: { networkAccess: "open" } })
      .catch(() => undefined);
  }
});
