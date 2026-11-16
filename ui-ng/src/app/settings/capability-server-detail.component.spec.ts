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

import { HttpErrorResponse } from "@angular/common/http";
import { Component, signal } from "@angular/core";
import { TestBed } from "@angular/core/testing";
import {
  DefaultUrlSerializer,
  Router,
  UrlSegment,
  provideRouter,
} from "@angular/router";
import { GrainApi, type RouteParams } from "../api/grain-api.service";
import type { GrainConfig } from "../api/models";
import { capabilityServerRouteMatcher } from "../app.routes";
import { AccessService } from "../services/access.service";
import { CapabilityServerDetailComponent } from "./capability-server-detail.component";
import {
  type CapabilityServer,
  type CapabilityServerUsage,
  type HostEntriesResponse,
  type HostdEntry,
  figmaRedirectURI,
} from "./capability-server-types";
import { SettingsStateService } from "./settings-state.service";

interface RecordedCall {
  method: "GET" | "POST" | "PUT";
  key: string;
  body?: unknown;
}

class FakeDetailApi {
  calls: RecordedCall[] = [];
  servers: CapabilityServer[] = [];
  usage: CapabilityServerUsage[] = [];
  hostEntries: HostEntriesResponse = {
    available: true,
    entries: [],
    missing: [],
  };
  hostds: HostdEntry[] = [
    {
      name: "lab-1",
      owner: "alice",
      connection: { connected: true, host: "lab-1.local" },
    },
  ];
  gchatUsage: {
    space: string;
    posted: number;
    refused: number;
    failed: number;
    last_hour: number;
  }[] = [];
  putHandler: (body: unknown) => Promise<unknown> = async () => ({});
  postHandler: (key: string, body: unknown) => Promise<unknown> = async (
    key,
  ) => {
    if (key === "POST /api/capability-servers/test") {
      return { ok: true, methods: [{ name: "run" }] };
    }
    if (key === "POST /api/capability-servers/test-script") {
      return {
        exit_code: 0,
        duration_ms: 42,
        stdout: "hello world\n",
        stderr: "",
      };
    }
    if (key === "POST /api/capability-servers/figma-test") {
      return { handle: "maya", email: "maya@example.com" };
    }
    if (key === "POST /api/capability-servers/gchat-test") {
      return { message: "spaces/ops/messages/9" };
    }
    return { ok: true };
  };

  config = () =>
    Promise.resolve({
      version: "v3.9.0",
      configRepoOnly: false,
      targetRepos: ["acme/app"],
      access: {
        role: "grain/operator",
        roles: ["grain/operator"],
        full: true,
        scopes: [],
      },
    } as GrainConfig);

  get = (key: string) => {
    this.calls.push({ method: "GET", key });
    if (key === "GET /api/capability-servers") {
      return Promise.resolve(structuredClone(this.servers));
    }
    if (key === "GET /api/capability-servers/usage") {
      return Promise.resolve(structuredClone(this.usage));
    }
    if (key === "GET /api/capability-servers/host-entries") {
      return Promise.resolve(structuredClone(this.hostEntries));
    }
    if (key === "GET /api/hostds") {
      return Promise.resolve(structuredClone(this.hostds));
    }
    if (key === "GET /api/capability-servers/gchat-usage") {
      return Promise.resolve(structuredClone(this.gchatUsage));
    }
    return Promise.resolve({});
  };

  put = (key: string, _params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "PUT", key, body });
    return this.putHandler(body);
  };

  post = (key: string, _params?: RouteParams, body?: unknown) => {
    this.calls.push({ method: "POST", key, body });
    return this.postHandler(key, body);
  };

  clearConditionalCache = () => {};
}

class FakeAccess {
  can = () => true;
}

class FakeSettingsState {
  readonly readOnly = signal(false);
  readonly config = signal<GrainConfig | null>({
    version: "v3.9.0",
    configRepoOnly: false,
    targetRepos: ["acme/app"],
  } as GrainConfig);
  sectionLocked = () => false;
  load = () => Promise.resolve();
}

@Component({
  imports: [CapabilityServerDetailComponent],
  template: `<g-capability-server-detail [serverId]="serverId()" />`,
})
class HostComponent {
  readonly serverId = signal("https://grain.dev/capabilities/gcp");
}

async function mount(api: FakeDetailApi, serverId: string) {
  TestBed.configureTestingModule({
    imports: [HostComponent],
    providers: [
      provideRouter([
        { path: "settings/capabilities", component: HostComponent },
        { path: "task/:id", component: HostComponent },
        { path: "repos/:owner/:name/agents", component: HostComponent },
        { path: "settings/tasks", component: HostComponent },
      ]),
      { provide: GrainApi, useValue: api },
      { provide: AccessService, useValue: new FakeAccess() },
      { provide: SettingsStateService, useValue: new FakeSettingsState() },
    ],
  });
  const fixture = TestBed.createComponent(HostComponent);
  fixture.componentInstance.serverId.set(serverId);
  fixture.autoDetectChanges(true);
  await fixture.whenStable();
  await Promise.resolve();
  await fixture.whenStable();
  const el = fixture.nativeElement as HTMLElement;
  const q = <T extends Element = HTMLElement>(sel: string) =>
    el.querySelector<T & Element>(sel) as T | null;
  const router = TestBed.inject(Router);
  return { fixture, el, q, router, host: fixture.componentInstance };
}

describe("capabilityServerRouteMatcher", () => {
  const segs = (...parts: string[]) => parts.map((p) => new UrlSegment(p, {}));

  it("matches encoded, Router-parsed, and unencoded capability server IDs and rejects bare section routes", () => {
    expect(
      capabilityServerRouteMatcher(segs("settings", "capabilities")),
    ).toBeNull();
    expect(
      capabilityServerRouteMatcher(segs("settings", "tasks", "x")),
    ).toBeNull();

    // Direct percent-encoded segment.
    const encoded = capabilityServerRouteMatcher(
      segs(
        "settings",
        "capabilities",
        "https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fgcp",
      ),
    );
    expect(encoded?.posParams?.["serverId"]?.path).toBe(
      "https://grain.dev/capabilities/gcp",
    );

    // Real Angular DefaultUrlSerializer parse of an encoded URL.
    const serializer = new DefaultUrlSerializer();
    const parsedTree = serializer.parse(
      "/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fgcp",
    );
    const parsedSegs = parsedTree.root.children["primary"]!.segments;
    const fromRouter = capabilityServerRouteMatcher(parsedSegs);
    expect(fromRouter?.posParams?.["serverId"]?.path).toBe(
      "https://grain.dev/capabilities/gcp",
    );

    // Angular-encoded colon + percent-encoded slashes.
    const angularEncoded = capabilityServerRouteMatcher(
      segs(
        "settings",
        "capabilities",
        "https:%2F%2Fgrain.dev%2Fcapabilities%2Fhost-script",
      ),
    );
    expect(angularEncoded?.posParams?.["serverId"]?.path).toBe(
      "https://grain.dev/capabilities/host-script",
    );

    // Unencoded pasted slashes.
    const unencodedPasted = capabilityServerRouteMatcher(
      segs(
        "settings",
        "capabilities",
        "https:",
        "grain.dev",
        "capabilities",
        "gcp",
      ),
    );
    expect(unencodedPasted?.posParams?.["serverId"]?.path).toBe(
      "https://grain.dev/capabilities/gcp",
    );
  });
});

describe("CapabilityServerDetailComponent", () => {
  afterEach(() => TestBed.resetTestingModule());

  it("renders built-in gcp detail, schema form with enum labels, and inline field errors", async () => {
    const api = new FakeDetailApi();
    api.servers = [
      {
        id: "https://grain.dev/capabilities/gcp",
        builtin: {
          name: "gcp",
          summary: "Issue short-lived GCP credentials.",
        },
        config: {
          command: "grain-cap-gcp",
          config: { auth_mode: "keyless" },
        },
        status: {
          state: "attached",
          methodDetails: [
            {
              name: "gcp-key",
              description: "Mint a short-lived GCP token.",
              args: {
                type: "object",
                required: ["permission_set"],
                properties: {
                  permission_set: {
                    type: "string",
                    description: "Named GCP permission set.",
                  },
                },
              },
            },
          ],
          configSchema: {
            type: "object",
            properties: {
              auth_mode: {
                type: "string",
                title: "Credential mode",
                description: "Choose keyless or service-account key.",
                enum: ["keyless", "service_account_key"],
                "x-grain-enum-labels": {
                  keyless: "Keyless token",
                  service_account_key: "Service-account key",
                },
              },
            },
          },
        },
      },
    ];
    api.putHandler = async () => {
      throw new HttpErrorResponse({
        status: 400,
        error: {
          error: "invalid server settings",
          fields: { auth_mode: "key JSON required for this mode" },
        },
      });
    };

    const { q, el, fixture } = await mount(
      api,
      "https://grain.dev/capabilities/gcp",
    );

    expect(q('[data-testid="capability-server-detail-page"]')).not.toBeNull();
    expect(el.textContent).toContain("gcp");
    expect(el.textContent).toContain("gcp-key");

    // Open Server settings editor.
    q<HTMLButtonElement>(
      '[data-testid="capability-edit-settings-btn"]',
    )!.click();
    await fixture.whenStable();
    expect(q('[data-testid="capability-server-config"]')).not.toBeNull();
    expect(el.textContent).toContain("Keyless token");

    // Save triggers PUT and shows inline field error from fieldErrors.
    q<HTMLButtonElement>('[data-testid="save-server-settings-btn"]')!.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();
    expect(api.calls.some((c) => c.key === "PUT /api/capability-servers")).toBe(
      true,
    );
    expect(el.textContent).toContain("key JSON required for this mode");
  });

  it("runs host-script Test runner and displays exit code, duration, and stdout", async () => {
    const api = new FakeDetailApi();
    api.servers = [
      {
        id: "https://grain.dev/capabilities/host-script",
        builtin: { name: "host-script", summary: "Run approved host scripts." },
        config: {
          command: "grain-cap-host-script",
          config: {
            scripts: [
              {
                name: "ops",
                summary: "Run ops helper",
                command: "/usr/local/bin/ops",
                timeout: "30s",
                argv: [{ name: "env", required: true, pattern: "^[a-z]+$" }],
              },
            ],
          },
        },
        status: { state: "attached", tools: ["test_script"] },
      },
    ];

    const { q, el, fixture } = await mount(
      api,
      "https://grain.dev/capabilities/host-script",
    );

    expect(q('[data-testid="host-script-list"]')).not.toBeNull();
    expect(el.textContent).toContain("ops");

    const testBtn = [
      ...el.querySelectorAll<HTMLButtonElement>(
        '[data-testid="host-script-list"] button',
      ),
    ].find((b) => b.textContent?.trim() === "Test")!;
    testBtn.click();
    await fixture.whenStable();

    const addArgBtn = [
      ...el.querySelectorAll<HTMLButtonElement>(
        '[data-testid="host-script-test"] button',
      ),
    ].find((b) => b.textContent?.includes("Add argument"))!;
    addArgBtn.click();
    await fixture.whenStable();

    const argvInput = el.querySelector<HTMLInputElement>(
      '[data-host-script-arg="0"]',
    )!;
    argvInput.value = "staging";
    argvInput.dispatchEvent(new Event("input"));
    await fixture.whenStable();

    const runBtn = [
      ...el.querySelectorAll<HTMLButtonElement>(
        '[data-testid="host-script-test"] button',
      ),
    ].find((b) => b.textContent?.trim() === "Run ops")!;
    runBtn.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();

    expect(
      api.calls.find(
        (c) => c.key === "POST /api/capability-servers/test-script",
      )?.body,
    ).toEqual({
      id: "https://grain.dev/capabilities/host-script",
      script: "ops",
      argv: ["staging"],
    });
    expect(q('[data-testid="host-script-exit"]')?.textContent).toContain("0");
    expect(q('[data-testid="host-script-stdout"]')?.textContent).toContain(
      "hello world",
    );
  });

  it("renders figma OAuth instructions + Test and gchat usage + Send test message", async () => {
    const api = new FakeDetailApi();
    api.servers = [
      {
        id: "https://grain.dev/capabilities/figma",
        builtin: { name: "figma", summary: "Read Figma designs." },
        config: {
          config: {
            accounts: [
              {
                name: "design",
                auth_mode: "oauth",
                allow_sandbox_token: true,
              },
            ],
          },
        },
        status: { state: "attached", tools: ["test_account"] },
      },
      {
        id: "https://grain.dev/capabilities/gchat",
        builtin: { name: "gchat", summary: "Post to chat spaces." },
        config: {
          config: {
            spaces: [{ name: "ops", description: "Ops alerts" }],
          },
        },
        status: { state: "attached", tools: ["test_post"] },
      },
    ];
    api.gchatUsage = [
      { space: "ops", posted: 3, refused: 0, failed: 0, last_hour: 2 },
    ];

    const { q, el, fixture, host } = await mount(
      api,
      "https://grain.dev/capabilities/figma",
    );

    expect(q('[data-testid="figma-account-list"]')).not.toBeNull();
    expect(el.textContent).toContain("Connect Figma");
    expect(el.textContent).toContain(figmaRedirectURI());

    const figmaTestBtn = [
      ...el.querySelectorAll<HTMLButtonElement>(
        '[data-figma-account="design"] button',
      ),
    ].find((b) => b.textContent?.trim() === "Test")!;
    figmaTestBtn.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();
    expect(q('[data-testid="figma-account-result"]')?.textContent).toContain(
      "maya <maya@example.com>",
    );

    // Switch to gchat server detail.
    host.serverId.set("https://grain.dev/capabilities/gchat");
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();

    expect(q('[data-testid="gchat-space-list"]')).not.toBeNull();
    expect(q('[data-testid="gchat-space-usage"]')?.textContent).toContain(
      "3 posted",
    );
    const sendBtn = [
      ...el.querySelectorAll<HTMLButtonElement>(
        '[data-testid="gchat-space-list"] button',
      ),
    ].find((b) => b.textContent?.includes("Send test message"))!;
    sendBtn.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();
    expect(q('[data-testid="gchat-test-result"]')?.textContent).toContain(
      "spaces/ops/messages/9",
    );
  });

  it("edits config-map placements and saves normalized config", async () => {
    const api = new FakeDetailApi();
    api.servers = [
      {
        id: "https://grain.dev/capabilities/config-map",
        builtin: {
          name: "config-map",
          summary: "Mount files into sandboxes.",
        },
        config: {
          config: {
            config_maps: [
              {
                name: "tooling",
                data: {
                  toolrc: {
                    path: ".toolrc",
                    content: "v=1",
                    executable: false,
                  },
                },
              },
            ],
            secrets: [
              {
                name: "creds",
                keys: {
                  netrc: { path: ".netrc" },
                },
              },
            ],
          },
        },
        status: { state: "attached", configSchema: { type: "object" } },
      },
    ];

    const { q, el, fixture } = await mount(
      api,
      "https://grain.dev/capabilities/config-map",
    );

    expect(q('[data-testid="config-map-list"]')).not.toBeNull();
    expect(el.textContent).toContain("tooling");
    expect(el.textContent).toContain("netrc");

    q<HTMLButtonElement>(
      '[data-testid="capability-edit-settings-btn"]',
    )!.click();
    await fixture.whenStable();
    expect(q('[data-testid="config-map-editor"]')).not.toBeNull();

    // Toggle Executable switch on first config map file and save.
    const sw = el.querySelector<HTMLButtonElement>(
      '[data-testid="config-map-editor"] button[role="switch"].g-switch',
    )!;
    sw.click();
    await fixture.whenStable();

    q<HTMLButtonElement>('[data-testid="save-server-settings-btn"]')!.click();
    await fixture.whenStable();
    await Promise.resolve();

    const putCall = api.calls.find(
      (c) => c.key === "PUT /api/capability-servers",
    );
    expect(putCall?.body).toMatchObject({
      id: "https://grain.dev/capabilities/config-map",
      config: {
        config: {
          config_maps: [
            {
              name: "tooling",
              data: {
                toolrc: { path: ".toolrc", content: "v=1", executable: true },
              },
            },
          ],
        },
      },
    });
  });

  it("supports custom server connection edit, hostd missing note, holder links, and remove confirmation", async () => {
    const api = new FakeDetailApi();
    api.servers = [
      {
        id: "https://acme.example/capabilities/cloud",
        config: {
          command: "cloud-cap",
          run: "host",
        },
        status: { state: "error", error: "host entry missing" },
      },
    ];
    api.hostEntries = {
      available: true,
      entries: [],
      missing: [
        {
          name: "cloud-cap",
          command: "/opt/cloud-cap",
          entry: "cloud-cap: /opt/cloud-cap",
        },
      ],
    };
    api.usage = [
      {
        id: "https://acme.example/capabilities/cloud",
        holders: [
          {
            kind: "task",
            id: "42",
            repo: "acme/app",
            grant: "https://acme.example/capabilities/cloud#deploy",
          },
          {
            kind: "default",
            id: "repo-def",
            repo: "acme/app",
            grant: "https://acme.example/capabilities/cloud#deploy",
          },
        ],
      },
    ];

    const { q, el, fixture } = await mount(
      api,
      "https://acme.example/capabilities/cloud",
    );

    // Hostd missing note renders allowlist entry guidance.
    expect(q('[data-testid="hostd-entry-missing"]')).not.toBeNull();
    expect(q('[data-testid="hostd-entry-text"]')?.textContent).toContain(
      "cloud-cap: /opt/cloud-cap",
    );

    // Holder links point to /task/42 and /repos/acme/app/agents.
    const links = [
      ...el.querySelectorAll<HTMLAnchorElement>(
        '[data-testid="capability-usage"] a',
      ),
    ];
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "/task/42",
      "/repos/acme/app/agents",
    ]);

    // Edit connection loads hostds and displays ConnectionHostdField.
    q<HTMLButtonElement>(
      '[data-testid="capability-edit-connection-btn"]',
    )!.click();
    await fixture.whenStable();
    await Promise.resolve();
    await fixture.whenStable();
    expect(q('[data-testid="capability-server-edit"]')).not.toBeNull();
    expect(api.calls.some((c) => c.key === "GET /api/hostds")).toBe(true);

    // Remove opens confirm dialog naming holders and calls POST remove on confirm.
    q<HTMLButtonElement>(
      '[data-testid="capability-server-remove-btn"]',
    )!.click();
    await fixture.whenStable();
    expect(el.textContent).toContain("Task #42");
    const confirmBtn = el.querySelector<HTMLButtonElement>(
      '[data-testid="confirm-accept"]',
    )!;
    confirmBtn.click();
    await fixture.whenStable();
    await Promise.resolve();
    expect(
      api.calls.find((c) => c.key === "POST /api/capability-servers/remove")
        ?.body,
    ).toEqual({ id: "https://acme.example/capabilities/cloud" });
  });

  it("renders in-process server detail and unknown server ID warning", async () => {
    const api = new FakeDetailApi();
    api.servers = [
      {
        id: "https://grain.dev/capabilities/repo",
        inProcess: { name: "repo", summary: "Repository access grants." },
        status: {
          state: "attached",
          methodDetails: [{ name: "read", description: "Read repository" }],
        },
      },
    ];

    const { q, el, fixture, host } = await mount(
      api,
      "https://grain.dev/capabilities/repo",
    );
    expect(q('[data-testid="capability-server-in-process"]')).not.toBeNull();
    expect(q('[data-testid="capability-server-remove-btn"]')).toBeNull();
    expect(el.textContent).toContain("read");

    // Unknown server ID shows warning.
    host.serverId.set("https://grain.dev/capabilities/does-not-exist");
    await fixture.whenStable();
    expect(el.textContent).toContain("does-not-exist");
    expect(q('[data-testid="capability-servers-back"]')).not.toBeNull();
  });
});
