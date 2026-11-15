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

// Gap G1 of the 2026-11-14 connections & infrastructure walkthrough
// (docs/test-runs/2026-11-14-connections-infrastructure-settings.md):
// the classic Settings → Git pieces /next/settings/git still lacked --
// GitHub's published keys as an SSH connection's default known_hosts,
// GitHub App fields with scope and proxy, nameless connections told
// apart in "Reuse authentication from", and a pointer from the GitHub
// host field to Settings → Capabilities. Driven against a real
// `grain demo`; every test puts back what it changed.

const gitWrite = (r) =>
  r.request().method() === "POST" &&
  new URL(r.url()).pathname === "/api/deployment/git-connections";

async function removeHost(request, host) {
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
}

async function chooseSSH(page) {
  await page.getByTestId("select-git-conn-type").click();
  await page.getByTestId("select-git-conn-type-opt-ssh").click();
}

test("SSH on github.com: known_hosts is optional and defaults to GitHub's published keys", async ({
  page,
  request,
}) => {
  const keys = (
    await (await request.get("/api/deployment/git-connections")).json()
  ).find((c) => c.status?.defaultKnownHosts)?.status.defaultKnownHosts;
  expect(keys).toContain("github.com ssh-ed25519");

  await page.goto("/next/settings/git");
  await chooseSSH(page);
  await expect(page.locator('label[for="gc-known-hosts"]')).toHaveText(
    "Known hosts (optional)",
  );
  await expect(page.getByTestId("git-known-hosts-hint")).toContainText(
    "GitHub's published host keys",
  );
  const known = page.getByTestId("input-git-conn-known-hosts");
  await page.getByTestId("fill-git-known-hosts").click();
  await expect(known).toHaveValue(keys.trim());
  await expect(page.getByTestId("fill-git-known-hosts")).toBeDisabled();

  // A key alone is enough to save on github.com: the server pins
  // GitHub's keys for a blank known_hosts.
  await known.fill("");
  await page
    .getByTestId("input-git-conn-ssh-key")
    .fill("-----BEGIN OPENSSH PRIVATE KEY-----");
  await expect(page.getByTestId("save-git-conn-btn")).toBeEnabled();
});

test("SSH on a plain git host needs known_hosts; a refused save stays until the next one goes through", async ({
  page,
  request,
}) => {
  const host = "ssh.walk.example.test";
  await page.goto("/next/settings/git");
  try {
    await page.getByTestId("input-git-conn-host").fill(host);
    await page.getByTestId("select-git-conn-kind").click();
    await page.getByTestId("select-git-conn-kind-opt-git").click();
    await chooseSSH(page);
    await expect(page.locator('label[for="gc-known-hosts"]')).toHaveText(
      "Known hosts",
    );
    await expect(page.getByTestId("git-known-hosts-hint")).toContainText(
      "never trusts a host key on first use",
    );
    await expect(page.getByTestId("fill-git-known-hosts")).toHaveCount(0);

    const save = page.getByTestId("save-git-conn-btn");
    await page
      .getByTestId("input-git-conn-ssh-key")
      .fill("not-a-real-private-key");
    await expect(save).toBeDisabled();
    const known = `${host} ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl`;
    await page.getByTestId("input-git-conn-known-hosts").fill(known);
    await expect(save).toBeEnabled();

    // The real server refuses the key, and says so beside the form.
    let saved = page.waitForResponse(gitWrite);
    await save.click();
    let res = await saved;
    expect(res.status()).toBe(400);
    expect(JSON.parse(res.request().postData())).toMatchObject({
      host,
      kind: "git",
      type: "ssh",
      ssh: { privateKey: "not-a-real-private-key", knownHosts: known },
    });
    const refusal = page.getByTestId("git-connections-error");
    await expect(refusal).toContainText("private key");

    // A token instead goes through, and takes the refusal down.
    await page.getByTestId("select-git-conn-type").click();
    await page.getByTestId("select-git-conn-type-opt-pat").click();
    await page.getByTestId("input-git-conn-secret").fill("walk-token-not-real");
    saved = page.waitForResponse(gitWrite);
    await save.click();
    res = await saved;
    expect(res.status()).toBe(200);
    await expect(refusal).toHaveCount(0);
  } finally {
    await removeHost(request, host);
  }
});

test("GitHub App: app id, installation id and private key go with scope and proxy", async ({
  page,
}) => {
  // The App save is answered here: a real one would parse the key.
  let body = null;
  await page.route("**/api/deployment/git-connections", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    body = JSON.parse(route.request().postData());
    await route.fulfill({
      status: 400,
      json: { error: "app private key will not parse" },
    });
  });
  await page.goto("/next/settings/git");
  await page.getByTestId("select-git-conn-type").click();
  await page.getByTestId("select-git-conn-type-opt-app").click();
  await page.getByTestId("input-git-conn-scope").fill("acme/*");
  await page
    .getByTestId("input-git-conn-proxy")
    .fill("http://proxy.example.test:3128");
  const save = page.getByTestId("save-git-conn-btn");
  await page
    .getByTestId("input-git-app-private-key")
    .fill("-----BEGIN RSA PRIVATE KEY-----");
  await expect(save).toBeDisabled();
  await page.getByTestId("input-git-app-id").fill("12345");
  await page.getByTestId("input-git-app-install-id").fill("678");
  await expect(save).toBeEnabled();
  await save.click();
  await expect(page.getByTestId("git-connections-error")).toContainText(
    "app private key will not parse",
  );
  expect(body).toEqual({
    credential: "",
    host: "github.com",
    kind: "github",
    type: "app",
    scope: "acme/*",
    proxy: "http://proxy.example.test:3128",
    app: {
      appId: "12345",
      installationId: "678",
      privateKey: "-----BEGIN RSA PRIVATE KEY-----",
    },
  });
});

test("Reuse authentication from tells nameless connections on one host apart", async ({
  page,
  request,
}) => {
  const host = "reuse.walk.example.test";
  try {
    for (const scope of ["acme/one", "acme/two"]) {
      const res = await request.post("/api/deployment/git-connections", {
        data: {
          credential: "",
          host,
          kind: "git",
          type: "pat",
          value: "walk-token-not-real",
          scope,
        },
      });
      expect(res.status()).toBe(200);
    }
    const creds = (
      await (await request.get("/api/deployment/git-connections")).json()
    ).filter((c) => c.host === host);
    expect(creds).toHaveLength(2);

    await page.goto("/next/settings/git");
    await page.getByTestId("select-git-conn-mode").click();
    await page.getByTestId("select-git-conn-mode-opt-existing").click();
    await page.getByTestId("select-git-conn-reuse").click();
    const labels = [];
    for (const c of creds) {
      expect(c.credential).toMatch(/^grain-git-/);
      const opt = page.getByTestId("select-git-conn-reuse-opt-" + c.credential);
      await expect(opt).toContainText(`${host} · git host · ${c.scope} (pat)`);
      await expect(opt).toContainText(`stored as ${c.credential}`);
      labels.push(await opt.innerText());
    }
    expect(labels[0]).not.toEqual(labels[1]);
    await page
      .getByTestId("select-git-conn-reuse-opt-" + creds[1].credential)
      .click();
    await expect(page.getByTestId("input-git-conn-name")).toHaveValue(
      creds[1].credential,
    );
  } finally {
    await removeHost(request, host);
  }
});

test("The GitHub host field points at Settings → Capabilities credentials", async ({
  page,
}) => {
  await page.goto("/next/settings/git");
  const link = page
    .getByTestId("git-host-credentials-pointer")
    .getByRole("link", { name: "Settings → Capabilities" });
  await expect(link).toHaveAttribute(
    "href",
    "/next/settings/capabilities#credentials",
  );
  await link.click();
  await expect(page).toHaveURL(/\/next\/settings\/capabilities#credentials$/);
});

test("SSH known_hosts and its hint stay inside the card on a phone @phone @dark", async ({
  page,
}) => {
  await page.goto("/next/settings/git");
  await chooseSSH(page);
  await page.getByTestId("fill-git-known-hosts").scrollIntoViewIfNeeded();
  const card = await page.getByTestId("card-git-connections").boundingBox();
  for (const id of [
    "input-git-conn-known-hosts",
    "git-known-hosts-hint",
    "fill-git-known-hosts",
  ]) {
    const b = await page.getByTestId(id).boundingBox();
    expect(b.x, id).toBeGreaterThanOrEqual(card.x - 0.5);
    expect(b.x + b.width, id).toBeLessThanOrEqual(card.x + card.width + 0.5);
  }
  const hostHint = await page
    .getByTestId("git-host-credentials-pointer")
    .boundingBox();
  const identity = await page.getByTestId("card-git-identity").boundingBox();
  expect(hostHint.x + hostHint.width).toBeLessThanOrEqual(
    identity.x + identity.width + 0.5,
  );
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  await page.getByTestId("card-git-connections").screenshot({
    path: test.info().outputPath(`git-ssh-form.png`),
  });
});
