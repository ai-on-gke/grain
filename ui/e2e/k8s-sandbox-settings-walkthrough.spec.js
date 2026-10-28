import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { signIn } from "./accesscode.js";
import { pageErrors } from "./pageerrors.js";
import { eventually, startDaemon } from "./realdaemon.js";

// End-to-end walkthrough and regression test for Kubernetes sandbox settings
// in UI, CLI, grain_config, and daemon restart adoption
// (docs/test-plans/2026-10-22.md §2, commit 4c7da2279).
const test = base.extend(pageErrors);
test.setTimeout(180_000);

async function ok(res) {
  expect(res.ok(), `${res.url()}: ${res.status()} ${await res.text()}`).toBe(
    true,
  );
  return res;
}

async function runGrainCLI(args, cwd) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  if (!bin) throw new Error("GRAIN_E2E_DEMO_BIN is not set");
  return await new Promise((resolve, reject) => {
    const child = spawn(bin, args, {
      cwd,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d.toString()));
    child.stderr.on("data", (d) => (stderr += d.toString()));
    child.once("error", reject);
    child.once("exit", (code) => {
      if (code === 0) {
        resolve({ stdout, stderr });
      } else {
        reject(
          new Error(
            `grain ${args.join(" ")} exited ${code}:\nstdout: ${stdout}\nstderr: ${stderr}`,
          ),
        );
      }
    });
  });
}

async function operatorPage(
  browser,
  url,
  viewport = { width: 1280, height: 800 },
) {
  const context = await browser.newContext({
    baseURL: url,
    viewport,
    storageState: await signIn(url),
  });
  return { context, page: await context.newPage() };
}

async function setInputAndBlur(page, settingId, value) {
  const input = page.locator(`[data-setting-id="${settingId}"] input`);
  await expect(input).toBeVisible();
  const putDone = page.waitForResponse(
    (res) =>
      res.url().endsWith("/api/settings") && res.request().method() === "PUT",
  );
  await input.fill(value);
  await input.blur();
  await ok(await putDone);
}

test("Kubernetes sandbox backends default availability, UI/CLI/grain_config round-trip, and daemon restart adoption", async ({
  browser,
}) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-k8s-"));
  const dataDir = path.join(root, "data");
  const sandboxDir = path.join(root, "sandboxes");
  const fakeBin = path.join(root, "bin");
  await fs.mkdir(dataDir, { recursive: true });
  await fs.mkdir(sandboxDir, { recursive: true });
  await fs.mkdir(fakeBin, { recursive: true });
  await fs.writeFile(path.join(root, "gemini-key"), "test-only-key\n", {
    mode: 0o600,
  });

  // Provide an executable stub kubectl in fakeBin (which startDaemon prepends to PATH)
  // so daemon restart under kubernetes / kubernetes-granule succeeds cleanly.
  const stubKubectl = path.join(fakeBin, "kubectl");
  await fs.writeFile(
    stubKubectl,
    `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    *json*)
      printf '{"items":[]}\\n'
      exit 0
      ;;
  esac
done
exit 0
`,
    { mode: 0o755 },
  );

  let daemon;
  try {
    // =========================================================================
    // 1. Default availability without -k8s-* startup flags
    // =========================================================================
    daemon = await startDaemon(root, dataDir, sandboxDir, [
      "-disable-native-sandboxes",
    ]);

    const { context: desktopCtx, page: desktopPage } = await operatorPage(
      browser,
      daemon.url,
      { width: 1280, height: 800 },
    );
    const { context: phoneCtx, page: phonePage } = await operatorPage(
      browser,
      daemon.url,
      { width: 390, height: 844 },
    );

    const initialSettings = await (
      await ok(await desktopCtx.request.get("/api/settings"))
    ).json();
    expect(initialSettings.sandboxBackend).toBe("none");
    expect(initialSettings.sandboxBackends).toEqual(
      expect.arrayContaining(["kubernetes", "kubernetes-granule", "none"]),
    );
    expect(initialSettings.pendingRestart ?? []).toEqual([]);

    for (const pg of [desktopPage, phonePage]) {
      await pg.goto("/settings/sandboxes");
      const backendSelect = pg.locator(
        '[data-setting-id="sandboxBackend"] select',
      );
      await expect(backendSelect).toBeVisible();
      await expect(backendSelect).toHaveValue("none");
      await expect(
        backendSelect.locator('option[value="kubernetes"]'),
      ).toHaveText("Kubernetes — container pod per run (no VM)");
      await expect(
        backendSelect.locator('option[value="kubernetes-granule"]'),
      ).toHaveText("Kubernetes — Granule VM per run");
      await expect(
        backendSelect.locator('option[value="container"]'),
      ).toBeDisabled();
      await expect(
        backendSelect.locator('option[value="container"]'),
      ).toContainText("needs a git proxy host (-container-git-proxy-host)");
      await expect(
        backendSelect.locator('option[value="granule"]'),
      ).toBeDisabled();
      await expect(
        backendSelect.locator('option[value="granule"]'),
      ).toContainText("needs SSH user/workspace/git proxy host for granule");
      await expect(pg.locator("#kubernetes")).toBeVisible();
    }

    // =========================================================================
    // 2. Editing kubernetes and kubernetes-granule fields in UI & restartPending
    // =========================================================================
    const backendSelect = desktopPage.locator(
      '[data-setting-id="sandboxBackend"] select',
    );
    const selectK8sDone = desktopPage.waitForResponse(
      (res) =>
        res.url().endsWith("/api/settings") && res.request().method() === "PUT",
    );
    await backendSelect.selectOption("kubernetes");
    await ok(await selectK8sDone);

    // Verify build-pinned defaults and prefilled values for kubernetes backend in API and DOM
    const k8sSelectedSettings = await (
      await ok(await desktopCtx.request.get("/api/settings"))
    ).json();
    expect(k8sSelectedSettings.sandboxBackend).toBe("kubernetes");
    expect(k8sSelectedSettings.daemonBuildVersion).toBe("v1.2.0");
    expect(k8sSelectedSettings.k8sNamespaceDefault).toBe("grain");
    expect(k8sSelectedSettings.k8sWorkspaceDefault).toBe("/home/agent");
    expect(k8sSelectedSettings.k8sDeploymentDefault).toBe("grain");
    expect(k8sSelectedSettings.k8sContainerDefault).toBe("grain");
    expect(k8sSelectedSettings.k8sKubectlDefault).toBe("kubectl");
    expect(k8sSelectedSettings.k8sSandboxImageDefault).toBe(
      "ghcr.io/bwsalmon/grain/k8s-sandbox:v1.2.0",
    );
    expect(k8sSelectedSettings.k8sGranuleImageDefault).toBe(
      "ghcr.io/bwsalmon/grain/guest:v1.2.0",
    );
    expect(k8sSelectedSettings.sources).toMatchObject({
      k8sNamespace: "default",
      k8sSandboxImage: "default",
      k8sGranuleImage: "default",
      k8sWorkspace: "default",
      k8sDeployment: "default",
      k8sContainer: "default",
      k8sKubectl: "default",
    });

    await expect(
      desktopPage.locator('[data-setting-id="k8sNamespace"] input'),
    ).toHaveValue("grain");
    await expect(
      desktopPage.locator('[data-setting-id="k8sWorkspace"] input'),
    ).toHaveValue("/home/agent");
    await expect(
      desktopPage.locator('[data-setting-id="k8sDeployment"] input'),
    ).toHaveValue("grain");
    await expect(
      desktopPage.locator('[data-setting-id="k8sContainer"] input'),
    ).toHaveValue("grain");
    await expect(
      desktopPage.locator('[data-setting-id="k8sKubectl"] input'),
    ).toHaveValue("kubectl");
    await expect(
      desktopPage.locator('[data-setting-id="k8sSandboxImage"] input'),
    ).toHaveValue("ghcr.io/bwsalmon/grain/k8s-sandbox:v1.2.0");
    await expect(
      desktopPage.getByTestId("k8sNamespace-source-chip"),
    ).toHaveText("default");
    await expect(
      desktopPage.getByTestId("k8sSandboxImage-source-chip"),
    ).toHaveText("default");
    await expect(
      desktopPage.getByText(/matches this daemon's build v1\.2\.0/).first(),
    ).toBeVisible();

    // Verify customising a field flips chip to "custom" and Reset to default round-trips to empty stored value
    await setInputAndBlur(desktopPage, "k8sNamespace", "temp-custom-ns");
    await expect(
      desktopPage.getByTestId("k8sNamespace-source-chip"),
    ).toHaveText("custom");
    const resetNsPut = desktopPage.waitForResponse(
      (res) =>
        res.url().endsWith("/api/settings") && res.request().method() === "PUT",
    );
    await desktopPage.getByTestId("k8sNamespace-reset").click();
    await ok(await resetNsPut);
    await expect(
      desktopPage.locator('[data-setting-id="k8sNamespace"] input'),
    ).toHaveValue("grain");
    await expect(
      desktopPage.getByTestId("k8sNamespace-source-chip"),
    ).toHaveText("default");
    const afterNsReset = await (
      await ok(await desktopCtx.request.get("/api/settings"))
    ).json();
    expect(afterNsReset.k8sNamespace).toBe("");
    expect(afterNsReset.sources.k8sNamespace).toBe("default");

    // Populate Kubernetes text fields
    await setInputAndBlur(desktopPage, "k8sNamespace", "grain-sandboxes");
    await setInputAndBlur(desktopPage, "k8sKubectl", stubKubectl);
    await setInputAndBlur(
      desktopPage,
      "k8sSandboxImage",
      "ghcr.io/bwsalmon/grain/k8s-sandbox:v2",
    );
    await setInputAndBlur(desktopPage, "k8sWorkspace", "/workspace/agent");
    await setInputAndBlur(desktopPage, "k8sGitProxyHost", "10.244.0.10");
    await setInputAndBlur(desktopPage, "k8sSandboxService", "grain-sandbox");
    await setInputAndBlur(
      desktopPage,
      "k8sServiceAccount",
      "grain-sandbox-runner",
    );
    await setInputAndBlur(
      desktopPage,
      "k8sNodeSelector",
      "pool=pods, zone=us-central1-b",
    );
    await setInputAndBlur(desktopPage, "k8sDeployment", "grain-daemon-custom");
    await setInputAndBlur(desktopPage, "k8sContainer", "grain-custom");

    // Check k8sPrivileged and k8sNetworkPolicy checkboxes
    for (const checkboxId of ["k8sPrivileged", "k8sNetworkPolicy"]) {
      const cb = desktopPage.locator(`[data-setting-id="${checkboxId}"] input`);
      const putCb = desktopPage.waitForResponse(
        (res) =>
          res.url().endsWith("/api/settings") &&
          res.request().method() === "PUT",
      );
      await cb.check();
      await ok(await putCb);
    }

    // Verify GET /api/settings persists all values and tracks pendingRestart keys
    const afterK8sEdit = await (
      await ok(await desktopCtx.request.get("/api/settings"))
    ).json();
    expect(afterK8sEdit).toMatchObject({
      sandboxBackend: "kubernetes",
      k8sNamespace: "grain-sandboxes",
      k8sKubectl: stubKubectl,
      k8sSandboxImage: "ghcr.io/bwsalmon/grain/k8s-sandbox:v2",
      k8sWorkspace: "/workspace/agent",
      k8sGitProxyHost: "10.244.0.10",
      k8sSandboxService: "grain-sandbox",
      k8sServiceAccount: "grain-sandbox-runner",
      k8sNodeSelector: ["pool=pods", "zone=us-central1-b"],
      k8sPrivileged: true,
      k8sNetworkPolicy: true,
      k8sDeployment: "grain-daemon-custom",
      k8sContainer: "grain-custom",
    });

    const expectedPendingK8sKeys = [
      "sandboxBackend",
      "k8sNamespace",
      "k8sKubectl",
      "k8sSandboxImage",
      "k8sWorkspace",
      "k8sGitProxyHost",
      "k8sSandboxService",
      "k8sServiceAccount",
      "k8sNodeSelector",
      "k8sPrivileged",
      "k8sNetworkPolicy",
      "k8sDeployment",
      "k8sContainer",
    ];
    expect(afterK8sEdit.pendingRestart).toEqual(
      expect.arrayContaining(expectedPendingK8sKeys),
    );

    // Verify restart needed indicators in /settings/sandboxes and /system
    await expect(
      desktopPage.getByTestId("settings-restart-chip"),
    ).toBeVisible();
    await expect(
      desktopPage.getByTestId("settings-restart-chip"),
    ).toContainText("Restart needed");

    await desktopPage.goto("/system");
    const healthRestartBanner = desktopPage.getByTestId(
      "health-pending-restart",
    );
    await expect(healthRestartBanner).toBeVisible();
    await expect(healthRestartBanner).toContainText(
      "Saved, not applied until restart:",
    );
    for (const label of [
      "Sandbox backend",
      "Kubernetes namespace",
      "Kubectl binary",
      "Kubernetes sandbox pod image",
      "Kubernetes workspace directory",
      "Kubernetes git & model proxy host",
      "Kubernetes sandbox Service",
      "Kubernetes sandbox ServiceAccount",
      "Kubernetes node selector",
      "Run Kubernetes sandbox containers privileged",
      "Cluster CNI enforces NetworkPolicy for network: none runs",
      "Kubernetes Deployment",
      "Kubernetes container",
    ]) {
      await expect(healthRestartBanner).toContainText(label);
    }

    // Switch backend to kubernetes-granule and verify k8sWorkspaceDefault and k8sGranuleImage
    await desktopPage.goto("/settings/sandboxes");
    const selectGranuleDone = desktopPage.waitForResponse(
      (res) =>
        res.url().endsWith("/api/settings") && res.request().method() === "PUT",
    );
    await backendSelect.selectOption("kubernetes-granule");
    await ok(await selectGranuleDone);

    await eventually(async () => {
      const s = await (
        await ok(await desktopCtx.request.get("/api/settings"))
      ).json();
      return (
        s.sandboxBackend === "kubernetes-granule" &&
        s.k8sWorkspaceDefault === "/home/debian"
      );
    }, "k8sWorkspaceDefault did not update to /home/debian for kubernetes-granule");

    await expect(
      desktopPage.locator('[data-setting-id="k8sWorkspace"] input'),
    ).toHaveAttribute("placeholder", "/home/debian");

    const granuleImageInput = desktopPage.locator(
      '[data-setting-id="k8sGranuleImage"] input',
    );
    await expect(granuleImageInput).toBeVisible();
    await expect(granuleImageInput).toHaveValue(
      "ghcr.io/bwsalmon/grain/guest:v1.2.0",
    );
    await expect(
      desktopPage.getByTestId("k8sGranuleImage-source-chip"),
    ).toHaveText("default");

    await setInputAndBlur(
      desktopPage,
      "k8sGranuleImage",
      "ghcr.io/bwsalmon/grain/guest:v2",
    );

    const afterGranuleEdit = await (
      await ok(await desktopCtx.request.get("/api/settings"))
    ).json();
    expect(afterGranuleEdit.sandboxBackend).toBe("kubernetes-granule");
    expect(afterGranuleEdit.k8sWorkspaceDefault).toBe("/home/debian");
    expect(afterGranuleEdit.k8sGranuleImage).toBe(
      "ghcr.io/bwsalmon/grain/guest:v2",
    );
    expect(afterGranuleEdit.pendingRestart).toEqual(
      expect.arrayContaining([...expectedPendingK8sKeys, "k8sGranuleImage"]),
    );

    // =========================================================================
    // 3. grain/config export and schema check
    // =========================================================================
    await runGrainCLI(["state", "-data-dir", dataDir, "sync"], root);

    const stateRepoDir = path.join(dataDir, "state-repo");
    const grainConfigPath = path.join(
      stateRepoDir,
      "tables",
      "grain_config.json",
    );
    const exportedRows = JSON.parse(await fs.readFile(grainConfigPath, "utf8"));
    expect(exportedRows).toHaveLength(1);
    expect(exportedRows[0]).toMatchObject({
      sandbox_backend: "kubernetes-granule",
      k8s_namespace: "grain-sandboxes",
      k8s_kubectl: stubKubectl,
      k8s_sandbox_image: "ghcr.io/bwsalmon/grain/k8s-sandbox:v2",
      k8s_granule_image: "ghcr.io/bwsalmon/grain/guest:v2",
      k8s_workspace: "/workspace/agent",
      k8s_git_proxy_host: "10.244.0.10",
      k8s_sandbox_service: "grain-sandbox",
      k8s_service_account: "grain-sandbox-runner",
      k8s_node_selector: "pool=pods,zone=us-central1-b",
      k8s_privileged: 1,
      k8s_network_policy: 1,
      k8s_deployment: "grain-daemon-custom",
      k8s_container: "grain-custom",
    });

    const checkRes = await runGrainCLI(["state", "check", stateRepoDir], root);
    expect(checkRes.stdout).toContain("ok: grain can load this");

    // =========================================================================
    // 4. CLI grain settings -k8s-* and grain/config round-trip
    // =========================================================================
    // First flip privileged & network policy off via UI so we can verify CLI sets them back to true
    for (const checkboxId of ["k8sPrivileged", "k8sNetworkPolicy"]) {
      const cb = desktopPage.locator(`[data-setting-id="${checkboxId}"] input`);
      const putCb = desktopPage.waitForResponse(
        (res) =>
          res.url().endsWith("/api/settings") &&
          res.request().method() === "PUT",
      );
      await cb.uncheck();
      await ok(await putCb);
    }

    const token = (
      await fs.readFile(path.join(dataDir, "local-api-token"), "utf8")
    ).trim();
    const cliRes = await runGrainCLI(
      [
        "-server",
        daemon.url,
        "-token",
        token,
        "settings",
        "-k8s-namespace=grain-cli-ns",
        "-k8s-node-selector=pool=sandboxes,zone=us-central1-a",
        "-k8s-sandbox-service-account=cli-sandbox-sa",
        "-k8s-network-policy=true",
        "-k8s-sandbox-privileged=true",
      ],
      root,
    );
    expect(cliRes.stdout).toContain("k8s namespace:  grain-cli-ns");
    expect(cliRes.stdout).toContain(
      "k8s node selector: pool=sandboxes, zone=us-central1-a",
    );
    expect(cliRes.stdout).toContain("k8s service account: cli-sandbox-sa");
    expect(cliRes.stdout).toContain("k8s network policy: true");
    expect(cliRes.stdout).toContain("k8s privileged: true");

    // Verify grain/config table exports the CLI-updated settings and passes state check
    await runGrainCLI(["state", "-data-dir", dataDir, "sync"], root);
    const cliExportedRows = JSON.parse(
      await fs.readFile(grainConfigPath, "utf8"),
    );
    expect(cliExportedRows[0]).toMatchObject({
      k8s_namespace: "grain-cli-ns",
      k8s_node_selector: "pool=sandboxes,zone=us-central1-a",
      k8s_service_account: "cli-sandbox-sa",
      k8s_network_policy: 1,
      k8s_privileged: 1,
    });
    const cliCheckRes = await runGrainCLI(
      ["state", "check", stateRepoDir],
      root,
    );
    expect(cliCheckRes.stdout).toContain("ok: grain can load this");

    // Reload /settings/sandboxes at both desktop (1280x800) and phone (390x844) viewports
    for (const pg of [desktopPage, phonePage]) {
      await pg.goto("/settings/sandboxes");
      await expect(
        pg.locator('[data-setting-id="sandboxBackend"] select'),
      ).toHaveValue("kubernetes-granule");
      await expect(
        pg.locator('[data-setting-id="k8sNamespace"] input'),
      ).toHaveValue("grain-cli-ns");
      await expect(
        pg.locator('[data-setting-id="k8sNodeSelector"] input'),
      ).toHaveValue("pool=sandboxes, zone=us-central1-a");
      await expect(
        pg.locator('[data-setting-id="k8sServiceAccount"] input'),
      ).toHaveValue("cli-sandbox-sa");
      await expect(
        pg.locator('[data-setting-id="k8sPrivileged"] input'),
      ).toBeChecked();
      await expect(
        pg.locator('[data-setting-id="k8sNetworkPolicy"] input'),
      ).toBeChecked();
      await expect(
        pg.locator('[data-setting-id="k8sSandboxImage"] input'),
      ).toHaveValue("ghcr.io/bwsalmon/grain/k8s-sandbox:v2");
      await expect(
        pg.locator('[data-setting-id="k8sGranuleImage"] input'),
      ).toHaveValue("ghcr.io/bwsalmon/grain/guest:v2");
      await expect(
        pg.locator('[data-setting-id="k8sWorkspace"] input'),
      ).toHaveValue("/workspace/agent");
      await expect(
        pg.locator('[data-setting-id="k8sGitProxyHost"] input'),
      ).toHaveValue("10.244.0.10");
      await expect(
        pg.locator('[data-setting-id="k8sSandboxService"] input'),
      ).toHaveValue("grain-sandbox");
      await expect(
        pg.locator('[data-setting-id="k8sDeployment"] input'),
      ).toHaveValue("grain-daemon-custom");
      await expect(
        pg.locator('[data-setting-id="k8sContainer"] input'),
      ).toHaveValue("grain-custom");
      await expect(
        pg.locator('[data-setting-id="k8sKubectl"] input'),
      ).toHaveValue(stubKubectl);
    }

    await desktopCtx.close();
    await phoneCtx.close();

    // =========================================================================
    // 5. Daemon restart adoption
    // =========================================================================
    // Clear k8sSandboxService before restarting with -sandbox-port-base=0
    // (ephemeral ports), since a ClusterIP Service requires a fixed port block.
    await runGrainCLI(
      [
        "-server",
        daemon.url,
        "-token",
        token,
        "settings",
        "-k8s-sandbox-service=",
      ],
      root,
    );

    await daemon.stop();
    const imageRefFile = path.join(root, "image-ref");
    await fs.writeFile(
      imageRefFile,
      "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/google-grain:main\n",
    );
    daemon = await startDaemon(root, dataDir, sandboxDir, [
      "-disable-native-sandboxes",
      "-upgrade-image=ghcr.io/bwsalmon/grain/google-grain:main",
      `-upgrade-image-ref-file=${imageRefFile}`,
    ]);

    const { context: restartedCtx, page: restartedPage } = await operatorPage(
      browser,
      daemon.url,
      { width: 1280, height: 800 },
    );

    const restartedSettings = await (
      await ok(await restartedCtx.request.get("/api/settings"))
    ).json();
    expect(restartedSettings).toMatchObject({
      sandboxBackend: "kubernetes-granule",
      k8sNamespace: "grain-cli-ns",
      k8sKubectl: stubKubectl,
      k8sSandboxImage: "ghcr.io/bwsalmon/grain/k8s-sandbox:v2",
      k8sGranuleImage: "ghcr.io/bwsalmon/grain/guest:v2",
      k8sWorkspace: "/workspace/agent",
      k8sGitProxyHost: "10.244.0.10",
      k8sSandboxService: "",
      k8sServiceAccount: "cli-sandbox-sa",
      k8sNodeSelector: ["pool=sandboxes", "zone=us-central1-a"],
      k8sPrivileged: true,
      k8sNetworkPolicy: true,
      k8sDeployment: "grain-daemon-custom",
      k8sContainer: "grain-custom",
    });
    // Effective settings on the restarted daemon now match grain_config
    expect(restartedSettings.pendingRestart ?? []).toEqual([]);
    // And startup backend ("none") remains available alongside kubernetes and kubernetes-granule
    expect(restartedSettings.sandboxBackends).toEqual(
      expect.arrayContaining(["kubernetes", "kubernetes-granule", "none"]),
    );

    await restartedPage.goto("/settings/sandboxes");
    await expect(
      restartedPage.locator('[data-setting-id="sandboxBackend"] select'),
    ).toHaveValue("kubernetes-granule");
    await expect(
      restartedPage.getByText(
        /Each run boots a Granule microVM with its own guest kernel over \/dev\/kvm inside a privileged Kubernetes pod\./i,
      ),
    ).toBeVisible();
    await expect(
      restartedPage.getByTestId("settings-restart-chip"),
    ).toHaveCount(0);
    await expect(restartedPage.getByText("restart to apply")).toHaveCount(0);

    await restartedPage.goto("/system");
    await expect(
      restartedPage.getByTestId("health-pending-restart"),
    ).toHaveCount(0);

    await restartedCtx.close();
  } catch (err) {
    if (daemon) err.message += `\n\nDaemon output:\n${daemon.output()}`;
    throw err;
  } finally {
    if (daemon) await daemon.stop();
    await fs.rm(root, { recursive: true, force: true });
  }
});
