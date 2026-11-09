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
  settingsAliasAnchor,
  settingsAliasAnchorAt,
  settingsSectionFor,
} from "./settingsSections.js";

// 2026-11-08 connections walkthrough: the retired Legacy capabilities
// addresses landed at the top of Capabilities, some 5,900px above the
// Deployment credentials card they name.
describe("settingsAliasAnchor", () => {
  it("names the card a retired alias stands for", () => {
    expect(settingsSectionFor("credentials")).toBe("capabilities");
    expect(settingsAliasAnchor("credentials")).toBe("credentials");
    expect(settingsAliasAnchor("Legacy-Capabilities")).toBe("credentials");
    expect(settingsAliasAnchor("secrets")).toBe("other-secrets");
    // A section, or an alias for a whole section, names no card.
    expect(settingsAliasAnchor("capabilities")).toBe("");
    expect(settingsAliasAnchor("capability-servers")).toBe("");
    expect(settingsAliasAnchor("hosts")).toBe("");
    expect(settingsAliasAnchor("")).toBe("");
  });

  it("reads the alias off an address unless a #hash names a target", () => {
    expect(settingsAliasAnchorAt("/settings/credentials", "")).toBe(
      "credentials",
    );
    expect(settingsAliasAnchorAt("/settings/legacy-capabilities/", "")).toBe(
      "credentials",
    );
    expect(settingsAliasAnchorAt("/settings/secrets", "#")).toBe(
      "other-secrets",
    );
    expect(settingsAliasAnchorAt("/settings/credentials", "#githubHost")).toBe(
      "",
    );
    expect(settingsAliasAnchorAt("/settings/capabilities", "")).toBe("");
    expect(settingsAliasAnchorAt("/settings/credentials/x", "")).toBe("");
    expect(settingsAliasAnchorAt("/repos/credentials", "")).toBe("");
  });

  // 2026-11-08 deployment credentials card walkthrough: /admin/integrations
  // and /system/integrations -- the tab the GitHub host and secrets lived
  // on before Legacy capabilities -- redirected to the top of
  // Capabilities, 5,800px above the card, while /settings/credentials
  // scrolled to it.
  it("scrolls the old Integrations tab's addresses to the card too", () => {
    expect(settingsAliasAnchorAt("/admin/integrations", "")).toBe(
      "credentials",
    );
    expect(settingsAliasAnchorAt("/system/Integrations/", "")).toBe(
      "credentials",
    );
    expect(settingsAliasAnchorAt("/admin/integrations", "#githubHost")).toBe(
      "",
    );
    expect(settingsAliasAnchorAt("/admin/general", "")).toBe("");
    expect(settingsAliasAnchorAt("/system/health", "")).toBe("");
  });
});
