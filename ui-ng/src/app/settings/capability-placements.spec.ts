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

// Settings → Capabilities: the config-map server's config maps and
// secret maps, and the host-script server's scripts.

import { TestBed } from "@angular/core/testing";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";
import { CapabilityConfigMapsComponent } from "./capability-config-maps.component";
import { CapabilityHostScriptsComponent } from "./capability-host-scripts.component";
import {
  type PlacementServer,
  droppedPlacements,
  isConfigMapServer,
  isHostScriptServer,
  placementErrors,
  placementPayload,
  placementValue,
  scriptDraft,
  scriptDraftErrors,
  scriptFromDraft,
  scriptLimits,
  scriptsPayload,
} from "./capability-placements";

const CM_ID = "https://grain.dev/capabilities/config-map";
const HS_ID = "https://grain.dev/capabilities/host-script";

function configMapServer(): PlacementServer {
  return {
    id: CM_ID,
    builtin: { name: "config-map" },
    config: {
      serverUrl: "http://127.0.0.1:1/mcp",
      builtin: "config-map",
      config: {
        secret_values: "$secret:config-map-values",
        config_maps: [
          {
            name: "tools",
            dir: "/opt/tools",
            data: {
              "run.sh": { content: "echo hi\n", executable: true },
              "a.toml": { content: "x = 1\n", mode: "0640" },
            },
          },
        ],
        secrets: [
          { name: "deploy", keys: { token: {}, ca: { mode: "0400" } } },
        ],
      },
    },
    configuredSecretKeys: ["deploy/token"],
  };
}

function hostScriptServer(): PlacementServer {
  return {
    id: HS_ID,
    builtin: { name: "host-script" },
    config: {
      serverUrl: "http://127.0.0.1:2/mcp",
      builtin: "host-script",
      config: {
        scripts: [
          {
            name: "ops",
            description: "Echo",
            script: 'echo "$@"\n',
            timeout: 60,
          },
        ],
      },
    },
    status: { tools: ["test_script"] },
  };
}

describe("config-map placements", () => {
  it("recognises the two built-ins by name or by what they advertise", () => {
    expect(isConfigMapServer(configMapServer())).toBe(true);
    expect(isConfigMapServer(hostScriptServer())).toBe(false);
    expect(
      isConfigMapServer({
        id: "x",
        status: {
          configSchema: { properties: { config_maps: {}, secret_values: {} } },
        },
      }),
    ).toBe(true);
    expect(isHostScriptServer(hostScriptServer())).toBe(true);
    expect(
      isHostScriptServer({ id: "y", status: { tools: ["test_script"] } }),
    ).toBe(true);
    expect(isHostScriptServer(configMapServer())).toBe(false);
  });

  it("round-trips stored maps and sends only typed secret values", () => {
    const srv = configMapServer();
    const v = placementValue(srv.config?.config);
    expect(v.configMaps[0]?.files.map((f) => f.key)).toEqual([
      "a.toml",
      "run.sh",
    ]);
    expect(v.secretMaps[0]?.files.map((f) => f.key)).toEqual(["ca", "token"]);
    const ca = v.secretMaps[0]!.files[0]!;
    const body = placementPayload(srv, v, { [ca.id]: "pem" });
    expect(body.config["serverUrl"]).toBe("http://127.0.0.1:1/mcp");
    const cfg = body.config["config"] as Record<string, unknown>;
    // Its secret_values reference is kept, not dropped.
    expect(cfg["secret_values"]).toBe("$secret:config-map-values");
    expect(cfg["config_maps"]).toEqual([
      {
        name: "tools",
        dir: "/opt/tools",
        data: {
          "a.toml": { content: "x = 1\n", mode: "0640" },
          "run.sh": { content: "echo hi\n", executable: true },
        },
      },
    ]);
    expect(cfg["secrets"]).toEqual([
      { name: "deploy", keys: { ca: { mode: "0400" }, token: {} } },
    ]);
    expect(body.configSecrets).toEqual({
      secret_values: JSON.stringify({ "deploy/ca": "pem" }),
    });
    expect(placementPayload(srv, v, {}).configSecrets).toEqual({});
  });

  it("refuses unnamed or duplicate maps and keys, and new unset secrets", () => {
    const srv = configMapServer();
    const v = placementValue(srv.config?.config);
    // ca has no stored value and none typed.
    expect(placementErrors(v, srv)).toEqual({
      "secret:0:0:value": "Type this key's value.",
    });
    const ca = v.secretMaps[0]!.files[0]!;
    expect(placementErrors(v, srv, { [ca.id]: "x" })).toEqual({});
    v.configMaps.push({ ...v.configMaps[0]!, files: [] });
    v.configMaps[0]!.files[1]!.key = " a.toml ";
    v.secretMaps.push({ name: "", description: "", dir: "", files: [] });
    expect(placementErrors(v, srv, { [ca.id]: "x" })).toEqual({
      "config:1": "Another config map is named tools.",
      "config:0:1": "This map already places a.toml.",
      "secret:1": "Name the map.",
    });
  });

  it("names the saved maps and files a draft drops", () => {
    const stored = placementValue(configMapServer().config?.config);
    const draft = structuredClone(stored);
    draft.configMaps[0]!.files.pop();
    draft.secretMaps = [];
    expect(droppedPlacements(stored, draft)).toEqual([
      "config map tools: run.sh",
      "secret map deploy",
    ]);
    expect(droppedPlacements(stored, stored)).toEqual([]);
  });
});

describe("host scripts", () => {
  it("checks a draft as the server's parseHostScripts does", () => {
    const d = scriptDraft();
    expect(Object.keys(scriptDraftErrors(d, []))).toEqual([
      "name",
      "description",
      "script",
    ]);
    const ok = {
      ...d,
      name: "deploy_2",
      description: "Deploys",
      script: "true",
    };
    expect(scriptDraftErrors(ok, [])).toEqual({});
    expect(scriptDraftErrors(ok, ["deploy_2"]).name).toContain("already");
    expect(scriptDraftErrors({ ...ok, timeout: "0" }, []).timeout).toBeTruthy();
    expect(scriptDraftErrors({ ...ok, cwd: "/a/../b" }, []).cwd).toBeTruthy();
    expect(scriptDraftErrors({ ...ok, cwd: "/srv/" }, []).cwd).toBeTruthy();
    expect(scriptDraftErrors({ ...ok, cwd: "/srv/a.b" }, [])).toEqual({});
    expect(scriptDraftErrors({ ...ok, max_args: "0" }, [])).toEqual({});
    expect(
      scriptDraftErrors({ ...ok, max_calls: "0" }, []).max_calls,
    ).toBeTruthy();
    expect(scriptDraftErrors({ ...ok, env: "1BAD=x" }, []).env).toBeTruthy();
  });

  it("builds a script leaving blank limits to the server's defaults", () => {
    const s = scriptFromDraft({
      ...scriptDraft(),
      name: "deploy",
      description: "Deploys",
      script: "true\n",
      timeout: "30",
      max_calls: "3",
      env: "TOKEN=$env:DEPLOY_TOKEN\nA=b=c",
      approve: true,
    });
    expect(s).toEqual({
      name: "deploy",
      description: "Deploys",
      script: "true\n",
      timeout: 30,
      max_calls: 3,
      approve: true,
      env: { TOKEN: "$env:DEPLOY_TOKEN", A: "b=c" },
    });
    expect(scriptDraft(s).env).toBe("TOKEN=$env:DEPLOY_TOKEN\nA=b=c");
    expect(scriptLimits(s)).toEqual([
      "timeout 30s",
      "3 calls",
      "approval per call",
      "env TOKEN, A",
    ]);
    const body = scriptsPayload(hostScriptServer(), [s]);
    expect(body.config["serverUrl"]).toBe("http://127.0.0.1:2/mcp");
    expect(body.config["config"]).toEqual({ scripts: [s] });
  });
});

interface Call {
  method: string;
  key: string;
  body?: unknown;
}

class FakeApi {
  calls: Call[] = [];
  testAnswer: unknown = {
    stdout: "a b\n",
    stderr: "",
    exit_code: 0,
    duration_ms: 3,
  };
  put = (key: string, _p?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "PUT", key, body });
    return Promise.resolve({ ok: true });
  };
  post = (key: string, _p?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, body });
    return Promise.resolve(this.testAnswer);
  };
}

async function mount<T>(
  cmp: new (...args: never[]) => T,
  server: PlacementServer,
  can = true,
) {
  const api = new FakeApi();
  TestBed.configureTestingModule({
    providers: [
      { provide: GrainApi, useValue: api },
      { provide: AccessService, useValue: { can: () => can } },
    ],
  });
  const fixture = TestBed.createComponent(cmp);
  let saved = 0;
  fixture.componentRef.setInput("server", server);
  (
    fixture.componentInstance as { saved: { subscribe(f: () => void): void } }
  ).saved.subscribe(() => saved++);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const settle = async () => {
    await new Promise((r) => setTimeout(r, 5));
    await fixture.whenStable();
  };
  const button = (name: string) => {
    const b = [...el.querySelectorAll<HTMLButtonElement>("button")].find(
      (x) =>
        (x.getAttribute("aria-label") ?? x.textContent ?? "").trim() === name,
    );
    if (!b) throw new Error(`no button ${name}`);
    return b;
  };
  const click = async (name: string) => {
    button(name).click();
    await settle();
  };
  const type = async (
    label: string,
    value: string,
    within: ParentNode = el,
  ) => {
    const lab = [...within.querySelectorAll("label")].find((l) =>
      l.textContent?.trim().startsWith(label),
    );
    const input = lab?.htmlFor
      ? el.querySelector<HTMLInputElement>(`[id="${lab.htmlFor}"]`)
      : null;
    if (!input) throw new Error(`no field ${label}`);
    input.value = value;
    input.dispatchEvent(new Event("input"));
    await settle();
  };
  return { el, api, button, click, type, settle, saved: () => saved };
}

describe("Config maps card", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("lists each map with where it lands and which secrets are set", async () => {
    const { el } = await mount(
      CapabilityConfigMapsComponent,
      configMapServer(),
    );
    const tools = el.querySelector('[aria-label="Config map tools"]')!;
    expect(tools.textContent).toContain("/opt/tools");
    expect(tools.textContent).toContain("run.sh");
    const deploy = el.querySelector('[aria-label="Secret map deploy"]')!;
    expect(deploy.textContent).toContain("token: set");
    expect(deploy.textContent).toContain("ca: not set");
    expect(deploy.textContent).toContain("/home/debian/.config-map/deploy");
  });

  it("adds a config map and a file, and saves both in one PUT", async () => {
    const m = await mount(CapabilityConfigMapsComponent, configMapServer());
    await m.click("Edit maps");
    // ca still needs its value, so Save waits for it.
    expect(m.button("Save maps").disabled).toBe(true);
    const ca = m.el.querySelector<HTMLElement>(
      '[aria-label="Secret map deploy file ca"]',
    )!;
    await m.type("Secret value", "pem-body", ca);
    await m.click("Add config map");
    const fresh = m.el.querySelector<HTMLElement>(
      '[aria-label="Config map 2"]',
    )!;
    await m.type("Name", "lint", fresh);
    const file = m.el.querySelector<HTMLElement>(
      '[aria-label="Config map lint file 1"]',
    )!;
    await m.type("Key", ".eslintrc", file);
    await m.type("File content", "{}", file);
    expect(m.button("Save maps").disabled).toBe(false);
    await m.click("Save maps");
    expect(m.api.calls).toHaveLength(1);
    const body = m.api.calls[0]!.body as {
      config: { config: { config_maps: { name: string }[] } };
      configSecrets: Record<string, string>;
    };
    expect(body.config.config.config_maps.map((c) => c.name)).toEqual([
      "tools",
      "lint",
    ]);
    expect(body.configSecrets).toEqual({
      secret_values: JSON.stringify({ "deploy/ca": "pem-body" }),
    });
    expect(m.saved()).toBe(1);
    expect(m.el.querySelector('[data-testid="config-map-editor"]')).toBeNull();
  });

  it("asks before a save drops a saved map", async () => {
    const m = await mount(CapabilityConfigMapsComponent, configMapServer());
    await m.click("Edit maps");
    await m.click("Remove Secret map deploy");
    await m.click("Save maps");
    expect(m.api.calls).toHaveLength(0);
    const dialog = document.querySelector('[role="dialog"], dialog')!;
    expect(dialog.textContent).toContain("secret map deploy");
    const accept = [...dialog.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "Save and remove",
    )!;
    accept.click();
    await m.settle();
    await m.settle();
    expect(m.api.calls).toHaveLength(1);
    const body = m.api.calls[0]!.body as {
      config: { config: { secrets: unknown[] } };
    };
    expect(body.config.config.secrets).toEqual([]);
  });

  it("offers no editing to a caller who cannot write servers", async () => {
    const m = await mount(
      CapabilityConfigMapsComponent,
      configMapServer(),
      false,
    );
    expect(m.button("Edit maps").disabled).toBe(true);
  });
});

describe("Host scripts card", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("tests a saved script with each argument passed as one", async () => {
    const m = await mount(CapabilityHostScriptsComponent, hostScriptServer());
    await m.click("Test ops");
    await m.click("Add argument");
    await m.type("Argument 1", "a b");
    await m.click("Add argument");
    await m.type("Argument 2", "$(x)");
    await m.click("Run ops");
    await m.settle();
    expect(m.api.calls).toEqual([
      {
        method: "POST",
        key: "POST /api/capability-servers/test-script",
        body: { id: HS_ID, script: "ops", argv: ["a b", "$(x)"] },
      },
    ]);
    const result = m.el.querySelector('[data-testid="host-script-result"]')!;
    expect(result.textContent).toContain("exit 0");
    expect(result.textContent).toContain("a b");
  });

  it("shows a refusal rather than an exit code", async () => {
    const m = await mount(CapabilityHostScriptsComponent, hostScriptServer());
    m.api.testAnswer = { refused: "too many arguments" };
    await m.click("Test ops");
    await m.click("Run ops");
    await m.settle();
    const result = m.el.querySelector('[data-testid="host-script-result"]')!;
    expect(result.textContent).toContain("Not run: too many arguments");
  });

  it("edits a script and stores the whole list", async () => {
    const m = await mount(CapabilityHostScriptsComponent, hostScriptServer());
    await m.click("Edit ops");
    await m.type("Description", "Echo its arguments");
    await m.type("Max calls per grant", "0");
    expect(m.el.textContent).toContain("A whole number, 1 or more.");
    expect(m.button("Save script").disabled).toBe(true);
    await m.type("Max calls per grant", "5");
    await m.click("Save script");
    await m.settle();
    expect(m.api.calls).toHaveLength(1);
    expect(m.api.calls[0]!.body).toEqual({
      id: HS_ID,
      config: {
        serverUrl: "http://127.0.0.1:2/mcp",
        builtin: "host-script",
        config: {
          scripts: [
            {
              name: "ops",
              description: "Echo its arguments",
              script: 'echo "$@"\n',
              timeout: 60,
              max_calls: 5,
            },
          ],
        },
      },
    });
    expect(m.saved()).toBe(1);
  });

  it("adds a script, refusing a name already taken", async () => {
    const m = await mount(CapabilityHostScriptsComponent, hostScriptServer());
    await m.click("Add script");
    await m.type("Name", "ops");
    expect(m.el.textContent).toContain("A script named ops is already");
    await m.type("Name", "deploy");
    await m.type("Description", "Deploys");
    await m.type("Script (bash)", "true");
    await m.click("Save script");
    await m.settle();
    const body = m.api.calls[0]!.body as {
      config: { config: { scripts: { name: string }[] } };
    };
    expect(body.config.config.scripts.map((s) => s.name)).toEqual([
      "ops",
      "deploy",
    ]);
  });

  it("asks before removing a script", async () => {
    const m = await mount(CapabilityHostScriptsComponent, hostScriptServer());
    await m.click("Remove ops");
    expect(m.api.calls).toHaveLength(0);
    const dialog = document.querySelector('[role="dialog"], dialog')!;
    expect(dialog.textContent).toContain("grain host-script ops");
    [...dialog.querySelectorAll("button")]
      .find((b) => b.textContent?.trim() === "Remove")!
      .click();
    await m.settle();
    await m.settle();
    const body = m.api.calls[0]!.body as {
      config: { config: { scripts: unknown[] } };
    };
    expect(body.config.config.scripts).toEqual([]);
  });
});
