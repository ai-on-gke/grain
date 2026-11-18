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

import { describe, expect, it } from "vitest";
import {
  hostScriptLimits,
  hostScripts,
  isHostScriptServer,
  type CapabilityServerDetail,
  type JsonSchema,
  configFromForm,
  fieldApplies,
  fieldKind,
  formFromServer,
  gcpCredentialMode,
  holderLink,
  hostChoice,
  linesToPairs,
  newItem,
  removeMessage,
  renameMessage,
  schemaFields,
  secretFieldState,
} from "./capability-server";

describe("connection form", () => {
  it("round-trips a stdio entry placed on a named host", () => {
    const srv: CapabilityServerDetail = {
      id: "https://example.test/cap",
      config: {
        command: "my-cap",
        args: ["serve", "--fast"],
        env: { TOKEN: "$secret:tok" },
        run: "hostd",
        hostd: "build-runner",
        disabled: true,
        config: { a: 1 },
      },
    };
    const f = formFromServer(srv);
    expect(f.run).toBe("host");
    expect(f.hostd).toBe("build-runner");
    expect(configFromForm(f, srv.config)).toEqual(srv.config);
  });

  it("places on the default host as run: host, and HTTP drops stdio fields", () => {
    const f = { ...formFromServer(null), command: "x", run: "host" as const };
    expect(configFromForm(f)).toEqual({ command: "x", run: "host" });
    const h = {
      ...formFromServer(null),
      transport: "http" as const,
      serverUrl: " https://cap.example.test/mcp ",
      headers: "Authorization=Bearer $secret:t",
    };
    expect(configFromForm(h)).toEqual({
      serverUrl: "https://cap.example.test/mcp",
      headers: { Authorization: "Bearer $secret:t" },
    });
  });

  it("refuses a line that is not NAME=VALUE", () => {
    expect(() => linesToPairs("NOPE")).toThrow(/NAME=VALUE/);
    expect(linesToPairs("A=b=c\n\n")).toEqual({ A: "b=c" });
  });

  it("offers a host placement only where the host agent can run it", () => {
    expect(
      hostChoice({ available: false, reason: "no host" }, "x", "").why,
    ).toBe("no host");
    expect(hostChoice({ available: true, entries: ["a"] }, "b", "").ok).toBe(
      false,
    );
    expect(hostChoice({ available: true, entries: ["a"] }, "a", "").ok).toBe(
      true,
    );
    // A named host is always allowed: the server waits for it.
    expect(hostChoice({ available: false }, "", "h1").ok).toBe(true);
  });
});

describe("configure schema", () => {
  const gcp: JsonSchema = {
    type: "object",
    properties: {
      admin_credential_source: {
        type: "string",
        enum: ["ambient", "configured"],
        default: "ambient",
      },
      admin_credential: { type: "string", writeOnly: true },
      admins: { type: "array", deprecated: true },
      _grain_deployment_id: { type: "string" },
      permission_sets: {
        type: "array",
        items: {
          type: "object",
          required: ["name"],
          properties: {
            name: { type: "string" },
            credential: {
              type: "string",
              enum: ["token", "key"],
              default: "token",
            },
            pool_max: { type: "integer" },
            service_account: { type: "string" },
            roles: { type: "array", items: { type: "string" } },
          },
        },
      },
    },
  };

  it("draws fields by kind, leaving deprecated and injected ones out", () => {
    const f = schemaFields(gcp, gcp);
    expect(f.map((x) => [x.name, x.kind, x.secret])).toEqual([
      ["admin_credential_source", "enum", false],
      ["admin_credential", "string", true],
      ["permission_sets", "objects", false],
    ]);
    const items = gcp.properties!["permission_sets"].items!;
    expect(fieldKind(gcp, items.properties!["roles"])).toBe("lines");
    expect(fieldKind(gcp, items.properties!["pool_max"])).toBe("number");
  });

  it("starts a new permission set keyless, and key-only fields follow the mode", () => {
    const items = gcp.properties!["permission_sets"].items!;
    const fresh = newItem(gcp, items);
    expect(fresh).toEqual({ credential: "token" });
    expect(fieldApplies("pool_max", fresh, items)).toBe(false);
    expect(fieldApplies("pool_max", { credential: "key" }, items)).toBe(true);
    expect(
      fieldApplies(
        "pool_max",
        { credential: "key", service_account: "sa@p.iam.gserviceaccount.com" },
        items,
      ),
    ).toBe(false);
    expect(fieldApplies("admin_credential", {}, gcp)).toBe(false);
    expect(
      fieldApplies(
        "admin_credential",
        { admin_credential_source: "configured" },
        gcp,
      ),
    ).toBe(true);
  });

  it("says what each credential mode does, not token for an unset one", () => {
    expect(gcpCredentialMode({})).toMatch(/No credential mode stored/);
    expect(gcpCredentialMode({ credential: "token" })).toMatch(/^Keyless/);
    expect(
      gcpCredentialMode({ credential: "token", service_account: "a@b" }),
    ).toMatch(/impersonates a@b/);
    expect(gcpCredentialMode({ credential: "key" })).toMatch(/^Key mode/);
  });

  it("chips a secret field set, missing or unset from the store", () => {
    const srv: CapabilityServerDetail = {
      id: "x",
      config: { config: { a: "$secret:s/a", b: "$secret:s/b" } },
      secrets: [
        { name: "s/a", present: true },
        { name: "s/b", present: false },
      ],
    };
    expect(secretFieldState(srv, "a")).toBe("set");
    expect(secretFieldState(srv, "b")).toBe("missing");
    expect(secretFieldState(srv, "c")).toBe("unset");
  });
});

describe("holder links", () => {
  it("links tasks to their classic page and the rest to /next/", () => {
    expect(
      holderLink({ kind: "task", id: "42", name: "Fix", grant: "g" }),
    ).toMatchObject({ label: "Task #42", href: "/tasks/42" });
    expect(
      holderLink({ kind: "schedule", id: "s-1", name: "Nightly", grant: "g" }),
    ).toMatchObject({ route: ["/schedules", "s-1"] });
    expect(
      holderLink({ kind: "template", id: "t-1", grant: "g" }),
    ).toMatchObject({ route: ["/templates", "t-1"] });
    expect(
      holderLink({ kind: "default", id: "deployment", grant: "g" }),
    ).toMatchObject({
      label: "Deployment defaults",
      route: ["/settings", "tasks"],
    });
    expect(
      holderLink({
        kind: "default",
        id: "r",
        repo: "acme/widgets",
        grant: "g",
      }),
    ).toMatchObject({ route: ["/repos", "acme", "widgets"] });
  });
});

describe("remove and rename confirms", () => {
  const h = holderLink({ kind: "task", id: "7", grant: "g" });
  it("never claims nothing grants a server whose usage could not be read", () => {
    expect(removeMessage("x", null)).toMatch(/could not be read/);
    expect(removeMessage("x", null)).not.toMatch(/Nothing grants/);
    expect(renameMessage("x", "y", null)).toMatch(/could not be read/);
    expect(removeMessage("x", [])).toMatch(/Nothing grants it now/);
    expect(removeMessage("x", [h])).toMatch(/these grants/);
    expect(renameMessage("x", "y", [h])).toMatch(/leaves these grants/);
  });
});

describe("host scripts", () => {
  const srv = (config: unknown, tools: string[] = ["test_script"]) =>
    ({
      id: "https://grain.dev/capabilities/host-script",
      config: { config },
      status: { tools },
    }) as unknown as Parameters<typeof hostScripts>[0];

  it("is a host-script server only when it advertises test_script", () => {
    expect(isHostScriptServer(srv({}))).toBe(true);
    expect(isHostScriptServer(srv({}, ["test_account"]))).toBe(false);
  });

  it("lists only named scripts, and none when scripts is not a list", () => {
    expect(
      hostScripts(srv({ scripts: [{ name: "ops" }, { script: "x" }] })),
    ).toEqual([{ name: "ops" }]);
    expect(hostScripts(srv({ scripts: "ops" }))).toEqual([]);
    expect(hostScripts(srv(undefined))).toEqual([]);
  });

  it("says each limit the script sets, and env names but never values", () => {
    expect(hostScriptLimits({ name: "ops" })).toEqual([]);
    expect(
      hostScriptLimits({
        name: "ops",
        timeout: 60,
        max_output: 4096,
        max_calls: 3,
        max_args: 0,
        max_arg_len: 128,
        cwd: "/srv",
        approve: true,
        env: { TOKEN: "$env:DEPLOY_TOKEN", REGION: "eu" },
      }),
    ).toEqual([
      "timeout 60s",
      "output ≤ 4096 B",
      "3 calls",
      "≤ 0 args",
      "arg ≤ 128 B",
      "cwd /srv",
      "approval each call",
      "env TOKEN, REGION",
    ]);
  });
});
