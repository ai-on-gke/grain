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
import { hubOriginLabel, hubStateBadge } from "./settings-instance.component";
import { resolveSettingsSection, settingsAnchor } from "./settings-sections";

describe("Settings -> Hub", () => {
  it("is the hub section, and the old registry name still opens it", () => {
    expect(resolveSettingsSection("hub")).toBe("hub");
    expect(resolveSettingsSection("registry")).toBe("hub");
    expect(settingsAnchor("registry", "")).toBe("registry-status");
    expect(settingsAnchor("hub", "registry-scopes")).toBe("registry-scopes");
  });

  it("says the connection state in words with a tone", () => {
    expect(hubStateBadge("connected", true)).toEqual({
      label: "Connected",
      tone: "healthy",
    });
    expect(hubStateBadge("degraded", true).tone).toBe("blast");
    expect(hubStateBadge("error", true)).toEqual({
      label: "Heartbeat error",
      tone: "error",
    });
    expect(hubStateBadge("connecting", true).label).toBe("Connecting");
    // Off, or reported disabled, is not configured whatever the state.
    expect(hubStateBadge("connected", false).label).toBe("Not configured");
    expect(hubStateBadge("disabled", true).label).toBe("Not configured");
    // A state nobody has mapped shows as itself.
    expect(hubStateBadge("paused", true).label).toBe("paused");
  });

  it("names where an advertised scope came from", () => {
    expect(hubOriginLabel("override")).toBe("override");
    expect(hubOriginLabel("added")).toBe("added in settings");
    expect(hubOriginLabel("repo_config")).toBe("from repo config");
    expect(hubOriginLabel(undefined)).toBe("from repo config");
  });
});
