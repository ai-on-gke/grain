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
import { hasAnyGitConnection } from "../shared/no-git-connection-notice.component";
import { gitCheckLines } from "./settings-code.component";
import {
  hostdConnectionLine,
  parseHostCapacity,
} from "./settings-compute.component";

describe("Settings → Hosts helpers", () => {
  it("reads a blank capacity as 0, so clearing the field clears it", () => {
    expect(parseHostCapacity("", "Capacity")).toEqual({ value: 0 });
    expect(parseHostCapacity(" 4 ", "Capacity")).toEqual({ value: 4 });
    expect(parseHostCapacity("abc", "Capacity").error).toBe(
      'Capacity "abc" must be a non-negative whole number',
    );
    expect(parseHostCapacity("-1", "Capacity").error).toBeTruthy();
  });

  it("says where a connected host dials in from, as whom, on what build", () => {
    expect(
      hostdConnectionLine({
        name: "box",
        connection: {
          connected: true,
          host: "10.0.0.7",
          principal: "alice@example.com",
          build: "v1.2.3",
        },
      }),
    ).toBe("from 10.0.0.7 · as alice@example.com · v1.2.3");
    expect(hostdConnectionLine({ name: "local" })).toBe("Co-located (socket)");
    expect(hostdConnectionLine({ name: "box" })).toBe("Never connected");
    expect(
      hostdConnectionLine({ name: "box", revoked_at: "2026-11-01T00:00:00Z" }),
    ).toBe("");
  });
});

describe("Settings → Git helpers", () => {
  it("lists who a tested credential acts as and each repository's access", () => {
    expect(
      gitCheckLines({
        ok: true,
        login: "octo",
        host: "github.com",
        scopes: ["repo"],
        repos: [
          { repo: "acme/a", read: true, push: true },
          { repo: "acme/b", read: true, push: false },
        ],
        skipped: 2,
      }),
    ).toEqual([
      "Accepted as @octo by github.com",
      "scopes: repo",
      "✓ acme/a: read, push",
      "✗ acme/b: read only -- cannot push or open pull requests",
      "2 more repositories were not checked.",
    ]);
    expect(gitCheckLines({ ok: false, error: "bad credentials" })).toEqual([
      "bad credentials",
    ]);
  });

  it("counts a git connection on any host, but not anonymous", () => {
    expect(hasAnyGitConnection({})).toBe(false);
    expect(
      hasAnyGitConnection({ gitConnections: [{ credential: "anonymous" }] }),
    ).toBe(false);
    expect(
      hasAnyGitConnection({
        hasGitCredential: false,
        gitConnections: [{ host: "git.example.test", credential: "plain" }],
      }),
    ).toBe(true);
  });
});
