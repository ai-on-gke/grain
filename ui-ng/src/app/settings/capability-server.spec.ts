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
  type CapabilityServerDetail,
  type JsonSchema,
  boolDefault,
  configFromForm,
  fieldApplies,
  fieldKind,
  formFromServer,
  gcpCredentialMode,
  holderLink,
  hostChoice,
  linesToPairs,
  newItem,
  normalizeConfigErrors,
  removeMessage,
  renameMessage,
  renderedConfigPaths,
  schemaFields,
  secretFieldState,
} from "./capability-server";
import { capServerState } from "./settings-work.component";

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

describe("validation paths, boolean defaults, and runtime status chips", () => {
  it("normalizes bracket validation paths to schema-form dot paths", () => {
    expect(
      normalizeConfigErrors({
        "scripts[0].description": "is required",
        "permission_sets[2].roles": "grants no roles",
        "permission_sets[2]": "bad lease",
        "config.grain_url": "must be https",
        "": "refused",
      }),
    ).toEqual({
      "scripts.0.description": "is required",
      "permission_sets.2.roles": "grants no roles",
      "permission_sets.2": "bad lease",
      grain_url: "must be https",
      "": "refused",
    });
  });

  it("tracks rendered dot paths and defaults person_granted true when service_account is set", () => {
    const schema: JsonSchema = {
      type: "object",
      properties: {
        permission_sets: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: { type: "string" },
              service_account: { type: "string" },
              person_granted: { type: "boolean" },
              pool_max: { type: "integer" },
              credential: {
                type: "string",
                enum: ["token", "key"],
                default: "token",
              },
            },
          },
        },
      },
    };
    const itemSchema = schema.properties!["permission_sets"].items!;
    const pgField = schemaFields(schema, itemSchema).find(
      (f) => f.name === "person_granted",
    )!;
    expect(boolDefault(pgField, {}, itemSchema)).toBe(false);
    expect(
      boolDefault(
        pgField,
        { service_account: "sa@proj.iam.gserviceaccount.com" },
        itemSchema,
      ),
    ).toBe(true);
    const paths = renderedConfigPaths(schema, schema, {
      permission_sets: [
        { name: "org", service_account: "sa@proj.iam.gserviceaccount.com" },
      ],
    });
    expect(paths.has("permission_sets.0")).toBe(true);
    expect(paths.has("permission_sets.0.person_granted")).toBe(true);
    expect(paths.has("permission_sets.0.pool_max")).toBe(false);
  });

  it("reports startError, waiting, and configRefused on capServerState", () => {
    expect(
      capServerState({
        id: "https://grain.dev/capabilities/git",
        status: {
          state: "failed",
          startError: "could not start: port 4254 in use",
        },
      }),
    ).toEqual({
      label: "failed (could not start: port 4254 in use)",
      tone: "error",
    });
    expect(
      capServerState({
        id: "https://grain.dev/capabilities/gcp",
        status: {
          state: "needs-config",
          waiting: "ADC renewal",
        },
      }),
    ).toEqual({
      label: "waiting for ADC renewal",
      tone: "blast",
    });
    expect(
      capServerState({
        id: "https://grain.dev/capabilities/git",
        status: {
          state: "needs-config",
          configRefused: true,
        },
      }),
    ).toEqual({
      label: "config refused",
      tone: "error",
    });
  });
});
