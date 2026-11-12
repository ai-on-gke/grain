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

import { versionLabel } from "./version";

describe("versionLabel", () => {
  it("reads the build stamp object the server sends", () => {
    expect(
      versionLabel({
        release: "v3.4.0",
        edition: "google",
        commit: "523c9ac3",
      }),
    ).toBe("v3.4.0 · google");
    expect(versionLabel({ commit: "096941fb005bac29" })).toBe("096941f");
  });

  it("passes a string through and shows nothing for nothing", () => {
    expect(versionLabel("v1.2.3")).toBe("v1.2.3");
    expect(versionLabel(undefined)).toBe("");
    expect(versionLabel({})).toBe("");
  });
});
