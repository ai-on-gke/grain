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
import { recommendRecovery } from "./settings-security.component";

describe("Settings → Access: Access code card", () => {
  it("recommends recovery only when it would take effect", () => {
    const on = {
      mode: "on",
      viewerVerified: true,
      verifiedAdminCanSignIn: true,
    };
    expect(recommendRecovery(on)).toBe(true);
    // A verified grain/viewer, or a person only a group admits: nobody
    // with a full role can sign in, so recovery would not be in force.
    expect(recommendRecovery({ ...on, verifiedAdminCanSignIn: false })).toBe(
      false,
    );
    expect(recommendRecovery({ ...on, viewerVerified: false })).toBe(false);
    expect(recommendRecovery({ ...on, mode: "recovery" })).toBe(false);
    expect(recommendRecovery(null)).toBe(false);
  });
});
