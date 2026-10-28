import { describe, expect, it } from "vitest";
import {
  lastRestartReason,
  stagingLink,
  stagingProxied,
  stagingSandboxes,
  stagingState,
} from "./staging.js";

const at = (href) => new URL(href);

describe("stagingLink", () => {
  it("uses an explicit uiLink override when given", () => {
    const link = stagingLink(
      {
        name: "grain-staging",
        uiLink: "https://staging.grain.example.com/",
        hostPort: 4280,
      },
      { listenPort: 4242, proxied: true },
      at("https://grain.example.com/tasks"),
    );
    expect(link).toEqual({
      href: "https://staging.grain.example.com/",
      label: "https://staging.grain.example.com/",
      sameOrigin: false,
    });
  });

  it("links a UI sub-path root-relative, whatever the page's host", () => {
    const link = stagingLink(
      { name: "docs", uiSubpath: "/staging/docs", hostPort: 4280 },
      { listenPort: 4242, proxied: true },
      at("https://grain.example.com/tasks"),
    );
    expect(link).toMatchObject({
      href: "/staging/docs/",
      sameOrigin: true,
    });
    expect(link.warning).toBeUndefined();
  });

  it("links a host port on the host the page came from", () => {
    const link = stagingLink(
      { name: "grain-staging", hostPort: 4280 },
      { listenPort: 4242 },
      at("http://10.0.0.5:4242/"),
    );
    expect(link.href).toBe("http://10.0.0.5:4280/");
    expect(link.sameOrigin).toBe(false);
    expect(link.warning).toBeUndefined();
  });

  it("warns, rather than linking silently, behind a proxy", () => {
    const sb = { name: "grain-staging", hostPort: 4280 };
    // The daemon saw forwarding headers.
    const forwarded = stagingLink(
      sb,
      { listenPort: 4242, proxied: true },
      at("https://grain.corp.example/"),
    );
    expect(forwarded.href).toBe("https://grain.corp.example:4280/");
    expect(forwarded.warning).toMatch(/ui_subpath/);
    expect(forwarded.warning).toMatch(/proxy rule for port 4280/);
    // The page's port is not the one the daemon listens on.
    const moved = stagingLink(
      sb,
      { listenPort: 4242 },
      at("https://grain.corp.example/"),
    );
    expect(moved.warning).toBeTruthy();
  });

  it("reads the scheme's default port when the URL names none", () => {
    expect(stagingProxied({ listenPort: 443 }, at("https://h/"))).toBe(false);
    expect(stagingProxied({ listenPort: 80 }, at("http://h/"))).toBe(false);
    expect(stagingProxied({ listenPort: 8080 }, at("http://h/"))).toBe(true);
    // Nothing to compare against: not proxied.
    expect(stagingProxied({}, at("http://h:1234/"))).toBe(false);
  });

  it("has nothing to link for a sandbox exposing nothing", () => {
    expect(stagingLink({ name: "x" }, {}, at("http://h/")).href).toBe("");
  });
});

describe("stagingSandboxes and stagingState", () => {
  it("lists enabled sandboxes exposing a port", () => {
    const list = [
      { name: "a", enabled: true, hostPort: 1 },
      { name: "b", enabled: true, uiSubpath: "/staging/b" },
      { name: "c", enabled: false, hostPort: 2 },
      { name: "d", enabled: true },
    ];
    expect(stagingSandboxes(list).map((s) => s.name)).toEqual(["a", "b"]);
    expect(stagingSandboxes(undefined)).toEqual([]);
  });

  it("calls a sandbox restarting again and again crash-looping", () => {
    expect(stagingState({ enabled: true, state: "running" })).toBe("running");
    expect(
      stagingState({ enabled: true, state: "restarting", restarts: 1 }),
    ).toBe("restarting");
    expect(
      stagingState({ enabled: true, state: "restarting", restarts: 4 }),
    ).toBe("crash-looping");
    expect(stagingState({ enabled: false, state: "running" })).toBe("stopped");
  });

  it("finds the last restart's reason in the events", () => {
    expect(
      lastRestartReason({
        events: [
          { kind: "exit", message: "exited 1" },
          { kind: "start", message: "running" },
        ],
      }),
    ).toBe("exited 1");
    expect(lastRestartReason({ lastError: "boom" })).toBe("boom");
  });
});
