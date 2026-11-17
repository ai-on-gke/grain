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
import { retiredGrant, retiredNotices } from "./capability-retired";
import { listedSecrets, needsHostWarning } from "./settings-work.component";

describe("retired in-process capabilities", () => {
  it("names the canonical grant each retired name moved onto", () => {
    for (const name of [
      "gcp-key",
      "gemini-key",
      "claude-proxy",
      "image-signing",
      "registry-push",
      "grain-ci",
      "bootstrap-playbooks",
      "github-sandbox",
      "git-push-any-ref",
    ]) {
      expect(retiredGrant(name), name).not.toBeNull();
    }
    expect(retiredGrant("memory")?.grant).toBe(
      "https://grain.dev/capabilities/api#memory",
    );
    expect(retiredGrant("self-debug")?.grant).toBe(
      "https://grain.dev/capabilities/api#self-debug + https://grain.dev/capabilities/grain#source={}",
    );
    expect(retiredGrant("deployment-health")?.grant).toBe(
      "https://grain.dev/capabilities/api#deployment:read + https://grain.dev/capabilities/grain#health={}",
    );
    expect(retiredGrant("grain-log")?.grant).toBe(
      "https://grain.dev/capabilities/grain#journal={}",
    );
    expect(retiredGrant("grain-ci")?.grant).toBe(
      "https://grain.dev/capabilities/grain#ci={}",
    );
    expect(retiredGrant("gcp-key")?.grant).toBe(
      'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}',
    );
    expect(retiredGrant("gcp-key:default")?.grant).toBe(
      'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}',
    );
    expect(retiredGrant("gcp-key:deployer")?.grant).toBe(
      'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"deployer","primary":true}',
    );
    for (const bad of [
      "gcp-key:",
      "gcp-key:a b",
      "gcp-key:a/b",
      "gcp-key:a\\b",
      "gcp-key:a:b",
      "gcp-key:a*b",
      "gcp-key:a?b",
      "gcp-key:a[b",
    ]) {
      expect(retiredGrant(bad), bad).toBeNull();
    }
    expect(retiredGrant("git-push-any-ref")?.grant).toBe(
      'https://grain.dev/capabilities/repo#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}',
    );
    expect(retiredGrant("jira-mcp")).toBeNull();
    expect(retiredGrant("toString")).toBeNull();
  });

  it("lists only retired catalogue entries, with what each still does", () => {
    const notices = retiredNotices({
      inProcess: {
        "jira-mcp": { enabled: false },
        "gemini-key": {},
        "gcp-key": { enabled: false, restrictions: { repos: ["acme/*"] } },
      },
    });
    expect(notices.map((n) => n.name)).toEqual(["gcp-key", "gemini-key"]);
    expect(notices[0].details).toEqual(["disabled", "repos acme/*"]);
    expect(notices[1].details).toEqual(["configured"]);
    expect(notices[1].moveTo).toContain("gcp#gemini-key");
    expect(retiredNotices(null)).toEqual([]);
  });
});

describe("Settings → Capabilities helpers", () => {
  it("warns only for an enabled server waiting on its host", () => {
    expect(
      needsHostWarning({ id: "p", status: { state: "needs-hostd:mac-mini" } }),
    ).toContain("host mac-mini, which is not connected");
    expect(
      needsHostWarning({
        id: "p",
        config: { disabled: true },
        status: { state: "needs-hostd:mac-mini" },
      }),
    ).toBeNull();
    expect(
      needsHostWarning({ id: "p", status: { state: "attached" } }),
    ).toBeNull();
  });

  it("keeps secrets other controls own off Stored secrets, and retired ones on it", () => {
    const got = listedSecrets(
      [
        { name: "agent-backend-claude", keys: ["value"] },
        { name: "registry-registration-token", keys: ["value"] },
        { name: "repo-release-acme-app", keys: ["value"] },
        { name: "gcp-key-minter", keys: ["value"] },
        { name: "figma", keys: ["token"], capabilityServers: ["figma"] },
      ],
      ["claude"],
    );
    expect(got.map((s) => s.name)).toEqual(["gcp-key-minter", "figma"]);
  });
});
