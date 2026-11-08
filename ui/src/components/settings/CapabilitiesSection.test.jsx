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

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import CapabilitiesSection from "./CapabilitiesSection.jsx";

vi.mock("../CapabilityServersPanel.jsx", () => ({ default: () => null }));
vi.mock("../CapabilityPolicyEditor.jsx", () => ({ default: () => null }));
vi.mock("../ApiScopeSetsPanel.jsx", () => ({ default: () => null }));

// Every in-process provider the 2026-11-04 range retired keeps its bare
// name as a one-release alias, and Settings -> Capabilities says so for
// each (docs/test-plans/2026-11-04.md §8). The release server's three
// methods and grain#ci had no notice
// (docs/test-runs/2026-11-04-capabilities.md, F3).
describe("CapabilitiesSection deprecation notices", () => {
  it.each([
    ["gcp-key-deprecation-notice", ["gcp-key", "gcp#gcp-key"]],
    [
      "model-proxy-deprecation-notice",
      ["claude-proxy", "model-proxy#model-proxy"],
    ],
    ["gemini-key-deprecation-notice", ["gemini-key", "gcp#gemini-key"]],
    [
      "release-deprecation-notice",
      [
        "image-signing",
        "registry-push",
        "package-publish",
        "https://grain.dev/capabilities/release",
        "release#<method>",
      ],
    ],
    [
      "grain-ci-deprecation-notice",
      ["grain-ci", "https://grain.dev/capabilities/grain#ci"],
    ],
    [
      "bootstrap-playbooks-deprecation-notice",
      ["bootstrap-playbooks", "config-map#config-map"],
    ],
    [
      "github-sandbox-deprecation-notice",
      [
        "github-sandbox",
        "https://grain.dev/capabilities/github#sandbox-repo",
        "github#sandbox-repo",
      ],
    ],
  ])("shows %s", (testId, texts) => {
    render(<CapabilitiesSection />);
    const notice = screen.getByTestId(testId);
    expect(notice.textContent).toMatch(/^Deprecated:/);
    expect(notice.textContent).toMatch(/alias(es)? for one release/);
    for (const text of texts) expect(notice.textContent).toContain(text);
  });
});
