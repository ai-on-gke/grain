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
  resolveSettingsSection,
  sectionMeta,
  settingsAnchor,
} from "./settings-sections";

describe("Settings -> Hub", () => {
  it("is the hub section, titled Hub, and the old registry name still opens it", () => {
    expect(resolveSettingsSection("hub")).toBe("hub");
    expect(resolveSettingsSection("registry")).toBe("hub");
    expect(sectionMeta("hub").label).toBe("Hub");
    // An old /settings/registry link still lands on the status card.
    expect(settingsAnchor("registry", "")).toBe("registry-status");
    expect(settingsAnchor("hub", "registry-scopes")).toBe("registry-scopes");
  });
});
