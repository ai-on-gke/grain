import { describe, expect, it } from "vitest";
import {
  GRANT_ARGS_MAX,
  builtinServerName,
  describeGrant,
  isSecretLooking,
  methodMenuLabel,
} from "./capabilityArgs.js";

describe("methodMenuLabel", () => {
  it("names a built-in server's method apart from the in-process capability", () => {
    expect(
      methodMenuLabel("https://grain.dev/capabilities/gcp#gcp-key", "gcp-key"),
    ).toBe("gcp server › gcp-key");
    expect(methodMenuLabel("https://example.com/caps/cloud#key", "key")).toBe(
      "key",
    );
    expect(builtinServerName("https://grain.dev/capabilities/config-map")).toBe(
      "config-map",
    );
    expect(builtinServerName("gcp-key")).toBe("");
    expect(builtinServerName("self-debug")).toBe("");
  });

  it("allows an array of enum secret names in with: arguments while hiding raw secret fields", () => {
    expect(
      isSecretLooking("secrets", {
        type: "array",
        items: { type: "string", enum: ["npmrc", "api-key"] },
      }),
    ).toBe(false);
    expect(
      isSecretLooking("secret_values", { type: "string", writeOnly: true }),
    ).toBe(true);
  });
});

describe("describeGrant", () => {
  const workspace = {
    ref: "https://grain.dev/capabilities/workspace#workspace",
    method: "workspace",
    args: {
      type: "object",
      properties: {
        create: { type: "boolean", default: false },
        delete_on_release: { type: "boolean", default: false },
      },
    },
  };

  it("names a built-in server's method by server and method", () => {
    const d = describeGrant(
      "https://grain.dev/capabilities/workspace#workspace",
    );
    expect(d.kind).toBe("method");
    expect(d.label).toBe("workspace › workspace");
    expect(d.args).toBe("");
    expect(d.lines).toEqual([
      "https://grain.dev/capabilities/workspace",
      "method: workspace",
      "no arguments",
    ]);
  });

  it("shows only the arguments that differ from their defaults", () => {
    const grant =
      'https://grain.dev/capabilities/workspace#workspace={"create":true,"delete_on_release":false}';
    const d = describeGrant(grant, { method: workspace });
    expect(d.label).toBe("workspace › workspace");
    expect(d.args).toBe("create: true");
    expect(d.full).toBe(grant);
    // The tooltip has every argument, defaults included.
    expect(d.lines).toEqual([
      "https://grain.dev/capabilities/workspace",
      "method: workspace",
      "create: true",
      "delete_on_release: false",
    ]);
    expect(
      describeGrant(
        'https://grain.dev/capabilities/workspace#workspace={"create":false}',
        { method: workspace },
      ).args,
    ).toBe("");
  });

  it("shows every argument when there is no schema to say what is a default", () => {
    const d = describeGrant(
      'https://grain.dev/capabilities/workspace#workspace={"create":true,"delete_on_release":false}',
    );
    expect(d.args).toBe("create: true, delete_on_release…");
    expect(d.args.length).toBe(GRANT_ARGS_MAX);
  });

  it("names a custom server by its host rather than its URL", () => {
    const d = describeGrant(
      'https://caps.example.com/servers/cloud#key={"project":"p1"}',
    );
    expect(d.label).toBe("caps.example.com › key");
    expect(d.args).toBe("project: p1");
    expect(d.lines[0]).toBe("https://caps.example.com/servers/cloud");
  });

  it("keeps an in-process capability's plain name", () => {
    const config = {
      capabilities: [{ id: "corp-research", name: "Corp research" }],
    };
    const d = describeGrant("corp-research", { config });
    expect(d.kind).toBe("capability");
    expect(d.label).toBe("Corp research");
    expect(d.lines).toEqual(["corp-research"]);
    expect(describeGrant("gcp-key").label).toBe("gcp-key");
  });

  it("reads an audit record's <id>#<method> as a method grant", () => {
    expect(describeGrant("host-script#host-script").label).toBe(
      "host-script › host-script",
    );
  });

  it("falls back to the raw string, cut, when the arguments do not parse", () => {
    const grant = `https://grain.dev/capabilities/workspace#workspace={"create":${"x".repeat(80)}`;
    const d = describeGrant(grant);
    expect(d.kind).toBe("raw");
    expect(d.label.endsWith("…")).toBe(true);
    expect(d.label.length).toBeLessThan(grant.length);
    expect(d.lines[0]).toBe(grant);
  });
});
