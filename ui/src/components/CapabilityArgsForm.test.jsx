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

import { useState } from "react";
import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import CapabilityArgsForm from "./CapabilityArgsForm.jsx";
import {
  argumentRows,
  grantLabel,
  initialValue,
  parseGrant,
  unsupportedReason,
  validateValue,
} from "../capabilityArgs.js";

// FIXTURE uses every keyword the form renders (docs/capabilities-spec.md
// §4.2.1): enum, const, boolean, a string with pattern/minLength/
// maxLength, bounded integer and number, an array with items/minItems/
// maxItems/uniqueItems, a nested object through $ref/$defs, oneOf and
// anyOf, descriptions, defaults and required -- and one secret-looking
// field, which is never offered.
const FIXTURE = {
  type: "object",
  additionalProperties: false,
  required: ["account", "project", "mode"],
  properties: {
    account: { enum: ["staging", "prod"], description: "Which account." },
    kind: { const: "service" },
    read_only: { type: "boolean", default: true, description: "Viewer only." },
    project: {
      type: "string",
      pattern: "^[a-z-]+$",
      minLength: 3,
      maxLength: 12,
    },
    ttl: { type: "integer", minimum: 5, maximum: 60, default: 15 },
    ratio: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 1 },
    scopes: {
      type: "array",
      items: { type: "string", minLength: 2 },
      minItems: 1,
      maxItems: 2,
      uniqueItems: true,
      default: ["all"],
    },
    labels: { $ref: "#/$defs/labels" },
    mode: {
      oneOf: [
        { title: "Named", type: "string", minLength: 1 },
        { title: "Numbered", type: "integer", minimum: 1 },
      ],
    },
    region: { anyOf: [{ const: "eu" }, { const: "us" }] },
    api_token: { type: "string" },
    credential_source: { type: "string", enum: ["configured", "ambient"] },
  },
  $defs: {
    labels: {
      type: "object",
      additionalProperties: false,
      properties: { team: { type: "string", maxLength: 5 } },
    },
  },
};

function Harness({ schema, initial }) {
  const [value, setValue] = useState(
    initial ?? initialValue(schema, schema) ?? {},
  );
  const errors = unsupportedReason(schema)
    ? {}
    : validateValue(schema, schema, value);
  return (
    <>
      <CapabilityArgsForm
        schema={schema}
        value={value}
        onChange={setValue}
        errors={errors}
        idPrefix="t"
      />
      <pre data-testid="value">{JSON.stringify(value)}</pre>
    </>
  );
}

const value = () => JSON.parse(screen.getByTestId("value").textContent);

describe("capabilityArgs", () => {
  it("renders the fixture without falling back", () => {
    expect(unsupportedReason(FIXTURE)).toBe("");
  });

  it("starts from defaults and consts, leaving optional objects out", () => {
    expect(initialValue(FIXTURE, FIXTURE)).toEqual({
      kind: "service",
      read_only: true,
      ttl: 15,
      scopes: ["all"],
    });
  });

  it("checks every keyword, keyed as the server keys its field errors", () => {
    const errs = validateValue(FIXTURE, FIXTURE, {
      account: "qa",
      kind: "other",
      project: "AB",
      ttl: 99,
      ratio: 1,
      scopes: ["x", "x", "yy"],
      labels: { team: "toolong" },
      mode: -1,
      region: "ap",
      extra: 1,
    });
    expect(Object.keys(errs).sort()).toEqual(
      [
        "account",
        "kind",
        "project",
        "ttl",
        "ratio",
        "scopes",
        "scopes[0]",
        "scopes[1]",
        "labels.team",
        "mode",
        "region",
        "extra",
      ].sort(),
    );
    expect(validateValue(FIXTURE, FIXTURE, {})).toMatchObject({
      account: "is required",
      project: "is required",
      mode: "is required",
    });
    expect(
      validateValue(FIXTURE, FIXTURE, {
        account: "prod",
        project: "web-app",
        mode: 3,
      }),
    ).toEqual({});
  });

  it("falls back for a keyword it cannot draw, naming it", () => {
    expect(
      unsupportedReason({
        type: "object",
        properties: { a: { allOf: [{ type: "string" }] } },
      }),
    ).toMatch(/args\.a.*allOf/);
    expect(
      unsupportedReason({
        type: "object",
        additionalProperties: { type: "integer" },
      }),
    ).toMatch(/additionalProperties/);
    expect(
      unsupportedReason({
        type: "object",
        properties: { a: { type: "string" } },
        additionalProperties: { type: "string" },
      }),
    ).toMatch(/additionalProperties/);
    expect(
      unsupportedReason({
        type: "object",
        properties: { a: { $ref: "#/$defs/missing" } },
      }),
    ).toMatch(/\$ref/);
  });

  it("labels a chip with the arguments that differ from their defaults", () => {
    const g = parseGrant(
      'https://x.example/c#key={"account":"staging","read_only":true,"ttl":30}',
    );
    expect(g.ref).toBe("https://x.example/c#key");
    expect(grantLabel({ ref: g.ref }, g.with, FIXTURE)).toBe(
      "key · account: staging, ttl: 30",
    );
  });

  it("tabulates arguments for Settings, nested ones dotted", () => {
    const rows = argumentRows(FIXTURE);
    const row = (n) => rows.find((r) => r.name === n);
    expect(row("account")).toMatchObject({
      required: true,
      allowed: '"staging", "prod"',
    });
    expect(row("project").allowed).toBe("/^[a-z-]+$/; length ≥ 3; length ≤ 12");
    expect(row("ttl")).toMatchObject({ type: "integer", default: "15" });
    expect(row("scopes").type).toBe("string[]");
    expect(row("labels.team").allowed).toBe("length ≤ 5");
    expect(row("mode").type).toBe("string | integer");
    expect(row("api_token").secret).toBe(true);
    // Named like a secret, but confined to the schema's own values.
    expect(row("credential_source").secret).toBeFalsy();
  });
});

// HOST_SCRIPTS is host-script's configure schema in little
// (pkg/capability/intree/hostscript.go): an array of objects with
// multi-line usage and script, and env as a string map.
const HOST_SCRIPTS = {
  type: "object",
  additionalProperties: false,
  properties: {
    scripts: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["name", "script"],
        properties: {
          name: { type: "string" },
          usage: { type: "string", format: "textarea" },
          script: { type: "string", format: "code" },
          env: {
            type: "object",
            additionalProperties: {
              type: "string",
              examples: ["$env:DEPLOY_TOKEN"],
            },
          },
        },
      },
    },
  },
};

describe("CapabilityArgsForm string maps and multi-line text", () => {
  it("renders host-script's schema as a form, not JSON", () => {
    expect(unsupportedReason(HOST_SCRIPTS)).toBe("");
  });

  it("checks a string map's values", () => {
    expect(
      validateValue(HOST_SCRIPTS, HOST_SCRIPTS, {
        scripts: [{ name: "a", script: "x", env: { OK: "v", BAD: 3 } }],
      }),
    ).toEqual({ "scripts[0].env.BAD": "must be a string" });
  });

  it("edits multi-line text and name/value rows", async () => {
    const user = userEvent.setup();
    let latest;
    function Harness() {
      const [v, setV] = useState({
        scripts: [{ name: "deploy", script: "echo hi" }],
      });
      latest = v;
      return (
        <CapabilityArgsForm
          schema={HOST_SCRIPTS}
          value={v}
          onChange={setV}
          secrets={{ fields: {}, values: {}, onChange: () => {} }}
        />
      );
    }
    render(<Harness />);
    expect(
      screen.getByRole("group", { name: /^scripts 1: deploy/ }),
    ).toBeTruthy();
    const script = screen.getByLabelText(/^script \*/);
    expect(script.tagName).toBe("TEXTAREA");
    await user.click(script);
    await user.keyboard("{End}{Enter}echo two");
    expect(latest.scripts[0].script).toBe("echo hi\necho two");
    expect(screen.getByLabelText(/^usage/).tagName).toBe("TEXTAREA");

    await user.click(screen.getByRole("button", { name: "Add entry" }));
    await user.type(screen.getByLabelText("Name"), "TOKEN");
    expect(screen.getByLabelText("Value").getAttribute("placeholder")).toBe(
      "$env:DEPLOY_TOKEN",
    );
    await user.type(screen.getByLabelText("Value"), "$env:DEPLOY_TOKEN");
    expect(latest.scripts[0].env).toEqual({ TOKEN: "$env:DEPLOY_TOKEN" });
    await user.click(screen.getByRole("button", { name: "Remove TOKEN" }));
    expect(latest.scripts[0].env).toBeUndefined();
  });
});

describe("CapabilityArgsForm", () => {
  it("renders each kind of field and edits the with: object", async () => {
    const user = userEvent.setup();
    render(<Harness schema={FIXTURE} />);

    // The secret warning, and the secret-looking field withheld.
    expect(screen.getByText(/Never put a secret/)).toBeTruthy();
    expect(screen.queryByLabelText(/api_token/)).toBeNull();
    expect(screen.getByText(/api_token looks like a secret/)).toBeTruthy();
    expect(
      screen.queryByText(/credential_source looks like a secret/),
    ).toBeNull();

    // enum: a select, required marked (its error until it is set), its
    // description in an (i) popover rather than inline.
    expect(screen.getAllByText("is required").length).toBeGreaterThan(0);
    await user.click(screen.getByRole("combobox", { name: /account/ }));
    await user.click(screen.getByRole("option", { name: "prod" }));
    expect(value().account).toBe("prod");
    expect(screen.queryByText("Which account.")).toBeNull();
    await user.hover(screen.getByRole("button", { name: "About account" }));
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Which account.",
    );
    await user.unhover(screen.getByRole("button", { name: "About account" }));

    // Boolean field description is in an (i) popover, not inline.
    expect(screen.queryByText("Viewer only.")).toBeNull();
    const readOnlyInfo = screen.getByRole("button", {
      name: "About read_only",
    });
    readOnlyInfo.focus();
    await expect
      .poll(() => screen.queryByRole("tooltip")?.textContent)
      .toContain("Viewer only.");
    readOnlyInfo.blur();

    // boolean: a switch, on from its default.
    const sw = screen.getByRole("switch", { name: /read_only/ });
    expect(sw.checked).toBe(true);
    await user.click(sw);
    expect(value().read_only).toBe(false);

    // string with pattern: live validation.
    const project = screen.getByLabelText(/^project/);
    await user.type(project, "AB");
    expect(screen.getByText("must be at least 3 characters")).toBeTruthy();
    await user.clear(project);
    await user.type(project, "web-app");
    expect(value().project).toBe("web-app");
    expect(screen.queryByText(/must match/)).toBeNull();

    // integer with bounds: a number field, checked live.
    const ttl = screen.getByLabelText(/^ttl/);
    expect(ttl.value).toBe("15");
    await user.clear(ttl);
    await user.type(ttl, "99");
    expect(screen.getByText("must be at most 60")).toBeTruthy();
    expect(value().ttl).toBe(99);

    // array: add/remove, maxItems disables Add, uniqueItems checked.
    const scopes = screen.getByRole("group", { name: "scopes" });
    const add = within(scopes).getByRole("button", { name: "Add scopes" });
    await user.click(add);
    await user.type(within(scopes).getAllByRole("textbox")[1], "all");
    expect(within(scopes).getByText("repeats an earlier item")).toBeTruthy();
    expect(add.disabled).toBe(true);
    await user.click(
      within(scopes).getByRole("button", { name: "Remove scopes 2" }),
    );
    expect(value().scopes).toEqual(["all"]);

    // nested object through $ref.
    await user.type(screen.getByLabelText(/^team/), "core");
    expect(value().labels).toEqual({ team: "core" });

    // oneOf: a variant picker switching the field's kind.
    await user.click(screen.getByRole("combobox", { name: /^mode/ }));
    await user.click(screen.getByRole("option", { name: "Numbered" }));
    await user.type(screen.getByLabelText(/^Numbered/), "3");
    expect(value().mode).toBe(3);
  });

  it("falls back to a JSON editor rather than dropping a field", async () => {
    const user = userEvent.setup();
    const schema = {
      type: "object",
      properties: {
        a: { type: "string" },
        b: { allOf: [{ type: "string" }, { minLength: 2 }] },
      },
    };
    render(<Harness schema={schema} initial={{ a: "x" }} />);
    expect(screen.getByText(/cannot draw/)).toBeTruthy();
    const editor = screen.getByLabelText("Arguments (JSON)");
    expect(JSON.parse(editor.value)).toEqual({ a: "x" });
    await user.clear(editor);
    await user.type(editor, '{{"a":"y","b":"zz"}');
    expect(value()).toEqual({ a: "y", b: "zz" });
    await user.clear(editor);
    await user.type(editor, "[[1]");
    expect(screen.getByText("Arguments must be a JSON object.")).toBeTruthy();
  });

  it("shows a server field error against its field", () => {
    render(
      <CapabilityArgsForm
        schema={FIXTURE}
        value={{ account: "prod" }}
        onChange={() => {}}
        errors={{ project: "is required", "": "matches none of anyOf" }}
      />,
    );
    expect(screen.getByText("is required")).toBeTruthy();
    expect(screen.getByText("matches none of anyOf")).toBeTruthy();
  });

  it("omits deprecated and daemon-injected fields from the form and argument rows", () => {
    const gcpConfigureSchema = {
      type: "object",
      additionalProperties: false,
      properties: {
        admin_credential_source: {
          type: "string",
          title: "Admin credential source",
          enum: ["ambient", "configured"],
          default: "ambient",
        },
        admin_credential: {
          type: "string",
          title: "Admin credential",
          writeOnly: true,
        },
        permission_sets: {
          type: "array",
          title: "Permission sets",
          items: {
            type: "object",
            required: ["name", "project", "roles"],
            properties: {
              name: { type: "string" },
              admin: { type: "string", deprecated: true },
              project: { type: "string" },
              roles: { type: "array", items: { type: "string" } },
              accounts: {
                type: "array",
                items: { type: "string" },
                deprecated: true,
              },
            },
          },
        },
        admins: { type: "array", items: { type: "object" }, deprecated: true },
        project: { type: "string", deprecated: true },
        accounts: {
          type: "array",
          items: { type: "object" },
          deprecated: true,
        },
        maxKeyAge: { type: "string", deprecated: true },
        minterKey: { type: "string", writeOnly: true, deprecated: true },
        _grain_deployment_id: { type: "string" },
        _grain_instance_id: { type: "string" },
        grainDeploymentId: { type: "string" },
        grainInstanceId: { type: "string" },
      },
    };
    const rows = argumentRows(gcpConfigureSchema);
    expect(rows.map((r) => r.name)).toEqual([
      "admin_credential_source",
      "admin_credential",
      "permission_sets",
    ]);

    render(
      <CapabilityArgsForm
        schema={gcpConfigureSchema}
        value={{}}
        onChange={() => {}}
        secrets={{ fields: {}, values: {}, onChange: () => {} }}
      />,
    );
    expect(
      screen.getByRole("combobox", { name: /Admin credential source/ })
        .textContent,
    ).toBe("ambient");
    expect(screen.getByLabelText("Admin credential")).toBeTruthy();
    expect(screen.getByRole("group", { name: "Permission sets" })).toBeTruthy();
    expect(screen.queryByText("admins")).toBeNull();
    expect(screen.queryByLabelText("project")).toBeNull();
    expect(screen.queryByText("accounts")).toBeNull();
    expect(screen.queryByLabelText("maxKeyAge")).toBeNull();
    expect(screen.queryByLabelText("minterKey")).toBeNull();
    expect(screen.queryByLabelText("_grain_deployment_id")).toBeNull();
    expect(screen.queryByLabelText("grainDeploymentId")).toBeNull();
  });
});
