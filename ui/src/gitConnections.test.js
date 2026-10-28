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
  distinctConnections,
  repoPinnedConnection,
  resolveRepoConnection,
  hasAnyGitConnection,
  hasGitCredential,
} from "./gitConnections.js";

describe("hasAnyGitConnection", () => {
  it("counts a connection on any host, and never anonymous", () => {
    expect(hasAnyGitConnection(null)).toBe(false);
    expect(hasAnyGitConnection({ hasGitCredential: false })).toBe(false);
    expect(hasAnyGitConnection({ hasGitCredential: true })).toBe(true);
    expect(
      hasAnyGitConnection({
        hasGitCredential: false,
        gitConnections: [
          { host: "depot.code.forge.test", credential: "forgecli" },
        ],
      }),
    ).toBe(true);
    expect(
      hasAnyGitConnection({
        hasGitCredential: false,
        gitConnections: [{ host: "github.com", credential: "anonymous" }],
      }),
    ).toBe(false);
  });
});

describe("hasGitCredential", () => {
  it("reads the server's answer when there is one", () => {
    expect(hasGitCredential({ hasGitCredential: true })).toBe(true);
    // The server's false wins over a connection on another host.
    expect(
      hasGitCredential({
        hasGitCredential: false,
        gitConnections: [{ credential: "gl", host: "gitlab.example" }],
      }),
    ).toBe(false);
  });

  it("counts connections as well as the legacy credential", () => {
    expect(hasGitCredential(null)).toBe(false);
    expect(hasGitCredential({})).toBe(false);
    expect(hasGitCredential({ gitCredential: " " })).toBe(false);
    expect(hasGitCredential({ gitCredential: "grain-team-x" })).toBe(true);
    expect(
      hasGitCredential({
        gitCredential: "",
        gitConnections: [{ credential: "a" }, { credential: "b" }],
      }),
    ).toBe(true);
    expect(
      hasGitCredential({
        gitConnections: [{ credential: "a", host: "GitHub.com" }],
      }),
    ).toBe(true);
  });

  it("does not count anonymous, which pushes nothing", () => {
    expect(hasGitCredential({ gitCredential: "anonymous" })).toBe(false);
    expect(
      hasGitCredential({ gitConnections: [{ credential: "anonymous" }] }),
    ).toBe(false);
  });

  it("does not count a connection on another host only", () => {
    expect(
      hasGitCredential({
        gitConnections: [{ credential: "gl", host: "gitlab.example" }],
      }),
    ).toBe(false);
  });
});

describe("resolveRepoConnection", () => {
  const conns = [
    { credential: "default-a", host: "github.com" },
    { credential: "default-b", host: "github.com" },
    { credential: "acme", host: "github.com", scope: "Acme" },
    { credential: "widgets", host: "github.com", scope: "acme/widgets" },
    { credential: "gitlab", host: "gitlab.example.com" },
  ];

  it("prefers the repository's own connection, then its owner's, then the host default", () => {
    expect(resolveRepoConnection(conns, "acme/widgets")).toEqual({
      connection: conns[3],
      match: "repo",
    });
    expect(resolveRepoConnection(conns, "ACME/gadgets")).toEqual({
      connection: conns[2],
      match: "owner",
    });
    // The earlier of two equally specific connections wins.
    expect(resolveRepoConnection(conns, "other/thing")).toEqual({
      connection: conns[0],
      match: "default",
    });
  });

  it("never falls back across hosts", () => {
    expect(
      resolveRepoConnection(conns, "gitlab.example.com/group/proj"),
    ).toEqual({ connection: conns[4], match: "default" });
    expect(resolveRepoConnection(conns.slice(4), "acme/widgets")).toBeNull();
    expect(resolveRepoConnection([], "acme/widgets")).toBeNull();
  });

  it("lists each credential once, as its broadest entry", () => {
    const pinned = [
      { credential: "a", scope: "acme/widgets" },
      { credential: "a" },
      { credential: "b", scope: "acme" },
    ];
    expect(distinctConnections(pinned)).toEqual([
      { credential: "a" },
      { credential: "b", scope: "acme" },
    ]);
    expect(repoPinnedConnection(pinned, "acme/widgets")).toEqual(pinned[0]);
    expect(repoPinnedConnection(pinned, "acme/gadgets")).toBeNull();
  });
});
