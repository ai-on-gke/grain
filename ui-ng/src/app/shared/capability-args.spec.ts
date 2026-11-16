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

import {
  argumentRows,
  builtinServerName,
  deref,
  fieldKind,
  hasArguments,
  initialValue,
  isCapabilityRef,
  isFixedList,
  isHiddenProperty,
  isSecretField,
  isSecretLooking,
  isStringMap,
  methodMenuLabel,
  parseGrant,
  pickVariant,
  splitRef,
  unsupportedReason,
  validateValue,
  type JsonSchema,
} from "./capability-args";

describe("capability-args helpers", () => {
  it("parses capability refs and grant strings", () => {
    expect(isCapabilityRef("https://grain.dev/capabilities/gcp#gcp-key")).toBe(
      true,
    );
    expect(isCapabilityRef("api:ci:read")).toBe(false);

    expect(
      parseGrant(
        'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"ro"}',
      ),
    ).toEqual({
      ref: "https://grain.dev/capabilities/gcp#gcp-key",
      with: { permission_set: "ro" },
    });
    expect(parseGrant("https://grain.dev/capabilities/gcp#gcp-key")).toEqual({
      ref: "https://grain.dev/capabilities/gcp#gcp-key",
      with: null,
    });
    expect(parseGrant("https://example.com/cap#m={bad")).toEqual({
      ref: "https://example.com/cap#m",
      with: null,
      raw: "{bad",
    });

    expect(splitRef("https://grain.dev/capabilities/gcp#gcp-key")).toEqual({
      capability: "https://grain.dev/capabilities/gcp",
      method: "gcp-key",
    });
    expect(splitRef("https://grain.dev/capabilities/gcp")).toEqual({
      capability: "https://grain.dev/capabilities/gcp",
      method: "",
    });
    expect(builtinServerName("https://grain.dev/capabilities/gcp")).toBe("gcp");
    expect(builtinServerName("https://acme.example/cap")).toBe("");
    expect(
      methodMenuLabel("https://grain.dev/capabilities/gcp#gcp-key", "gcp-key"),
    ).toBe("gcp server › gcp-key");
  });

  it("resolves $defs and definitions refs and detects cycles", () => {
    const root: JsonSchema = {
      $defs: {
        role: { type: "string", enum: ["reader", "writer"] },
        cycle: { $ref: "#/$defs/cycle" },
      },
      definitions: {
        port: { type: "integer", minimum: 1 },
      },
    };
    expect(
      deref(root, { $ref: "#/$defs/role", description: "Role to assign" }),
    ).toEqual({
      type: "string",
      enum: ["reader", "writer"],
      description: "Role to assign",
    });
    expect(deref(root, { $ref: "#/definitions/port" })).toEqual({
      type: "integer",
      minimum: 1,
    });
    expect(deref(root, { $ref: "#/$defs/missing" })).toBeNull();
    expect(deref(root, { $ref: "#/$defs/cycle" })).toBeNull();
  });

  it("classifies field kinds, string maps, fixed lists, secrets, and hidden properties", () => {
    expect(fieldKind({ enum: ["a", "b"] })).toBe("choice");
    expect(fieldKind({ const: "fixed" })).toBe("choice");
    expect(fieldKind({ oneOf: [{ type: "string" }] })).toBe("variant");
    expect(fieldKind({ type: "boolean" })).toBe("boolean");
    expect(fieldKind({ type: "integer" })).toBe("number");
    expect(fieldKind({ properties: { a: { type: "string" } } })).toBe("object");

    expect(
      isStringMap(null, {
        type: "object",
        additionalProperties: { type: "string" },
      }),
    ).toBe(true);
    expect(
      isStringMap(null, {
        type: "object",
        properties: { a: { type: "string" } },
        additionalProperties: { type: "string" },
      }),
    ).toBe(false);

    expect(
      isFixedList(null, {
        type: "array",
        uniqueItems: true,
        minItems: 2,
        maxItems: 2,
        items: { type: "string", enum: ["read", "write"] },
      }),
    ).toBe(true);

    expect(isSecretField({ type: "string", writeOnly: true })).toBe(true);
    expect(isSecretField({ type: "string", format: "password" })).toBe(true);
    expect(isSecretLooking("api_token", { type: "string" })).toBe(true);
    expect(isSecretLooking("token_count", { type: "integer" })).toBe(false);
    expect(
      isSecretLooking("token_mode", { type: "string", enum: ["a", "b"] }),
    ).toBe(false);

    expect(isHiddenProperty("old", { type: "string", deprecated: true })).toBe(
      true,
    );
    expect(isHiddenProperty("grainDeploymentId", { type: "string" })).toBe(
      true,
    );
    expect(isHiddenProperty("_grain_internal", { type: "string" })).toBe(true);
    expect(isHiddenProperty("project", { type: "string" })).toBe(false);
  });

  it("builds initial values skipping hidden and secret-looking fields", () => {
    const schema: JsonSchema = {
      type: "object",
      required: ["mode", "project"],
      properties: {
        mode: { type: "string", enum: ["keyless", "key"], default: "keyless" },
        project: { type: "string" },
        service_account_key: { type: "string", writeOnly: true },
        grainInstanceId: { type: "string", default: "inst-1" },
        legacy: { type: "string", deprecated: true, default: "x" },
      },
    };
    expect(hasArguments(schema)).toBe(true);
    expect(initialValue(schema, schema)).toEqual({
      mode: "keyless",
    });
  });

  it("validates values against schema constraints and picks variants", () => {
    const schema: JsonSchema = {
      type: "object",
      required: ["name", "tags"],
      additionalProperties: false,
      properties: {
        name: { type: "string", minLength: 3, pattern: "^[a-z]+$" },
        count: { type: "integer", minimum: 1, maximum: 5 },
        tags: {
          type: "array",
          minItems: 1,
          uniqueItems: true,
          items: { type: "string" },
        },
      },
    };
    expect(
      validateValue(schema, schema, { name: "abc", count: 2, tags: ["x"] }),
    ).toEqual({});

    const errs = validateValue(schema, schema, {
      name: "A1",
      count: 9,
      tags: ["x", "x"],
      extra: true,
    });
    expect(errs["name"]).toContain("at least 3");
    expect(errs["count"]).toContain("at most 5");
    expect(errs["tags[1]"]).toBe("repeats an earlier item");
    expect(errs["extra"]).toBe("is not an accepted argument");

    const variants: JsonSchema[] = [
      { type: "string" },
      {
        type: "object",
        required: ["host"],
        properties: { host: { type: "string" } },
      },
    ];
    expect(pickVariant(null, variants, { host: "lab-1" })).toBe(1);
    expect(pickVariant(null, variants, "hello")).toBe(0);
    expect(
      unsupportedReason(null, {
        type: "string",
        customKeyword: true,
      }),
    ).toContain("customKeyword");
  });

  it("produces flattened argument rows excluding hidden fields", () => {
    const schema: JsonSchema = {
      type: "object",
      required: ["permission_set"],
      properties: {
        permission_set: {
          type: "string",
          enum: ["ro", "rw"],
          description: "Permission set name",
        },
        nested: {
          type: "object",
          properties: {
            token: { type: "string", writeOnly: true },
          },
        },
        _grain_hidden: { type: "string" },
      },
    };
    const rows = argumentRows(schema);
    expect(rows.map((r) => r.name)).toEqual([
      "permission_set",
      "nested",
      "nested.token",
    ]);
    expect(rows[0]).toMatchObject({
      name: "permission_set",
      required: true,
      allowed: '"ro", "rw"',
      secret: false,
    });
    expect(rows[2]?.secret).toBe(true);
  });
});
