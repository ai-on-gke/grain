import { describe, expect, it } from "vitest";
import { repoApiRoot, repoParts, storedOwner } from "./repoApi.js";

const depot = "git.forge.example/experimental/bsalmon-test";

describe("repoApiRoot", () => {
  it("routes a default-host repo by its owner and name", () => {
    expect(repoApiRoot("acme/widgets")).toBe("/api/repos/acme/widgets");
  });

  // The server's {owner} wildcard holds model.RepoRef.StoredOwner, so a
  // repo on another host carries its host in that one segment, escaped,
  // rather than being split into owner git.forge.example and name
  // experimental.
  it("puts a host-qualified repo's host in the owner segment, escaped", () => {
    expect(repoApiRoot(depot)).toBe(
      "/api/repos/git.forge.example%2Fexperimental/bsalmon-test",
    );
  });

  it("percent-encodes each segment", () => {
    expect(repoApiRoot("acme/wid gets")).toBe("/api/repos/acme/wid%20gets");
  });
});

describe("repoParts", () => {
  it("reads host, owner and name", () => {
    expect(repoParts(depot)).toEqual({
      host: "git.forge.example",
      owner: "experimental",
      name: "bsalmon-test",
    });
    expect(repoParts("acme/widgets")).toEqual({
      host: "",
      owner: "acme",
      name: "widgets",
    });
    expect(storedOwner(depot)).toBe("git.forge.example/experimental");
    expect(storedOwner("acme/widgets")).toBe("acme");
  });
});
