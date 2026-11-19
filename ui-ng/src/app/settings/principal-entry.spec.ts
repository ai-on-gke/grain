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
import { validatePrincipalEntry } from "./principal-entry";

describe("validatePrincipalEntry", () => {
  it("lowercases and keeps a well-formed entry", () => {
    expect(validatePrincipalEntry(" user:Alice@Example.com ")).toEqual({
      canonical: "user:alice@example.com",
      error: "",
    });
    expect(
      validatePrincipalEntry("serviceAccount:SA@p.iam.gserviceaccount.com"),
    ).toEqual({
      canonical: "serviceAccount:sa@p.iam.gserviceaccount.com",
      error: "",
    });
  });

  it("refuses what the server refuses, by name", () => {
    expect(validatePrincipalEntry("alice@example.com").error).toMatch(
      /must start with user:, group: or serviceAccount:/,
    );
    expect(validatePrincipalEntry("user:not an email").error).toMatch(
      /valid email address/,
    );
    expect(
      validatePrincipalEntry("serviceAccount:bot@example.com").error,
    ).toMatch(/\*\.gserviceaccount\.com/);
    expect(
      validatePrincipalEntry("user:sa@p.iam.gserviceaccount.com").error,
    ).toMatch(/serviceAccount:sa@p\.iam\.gserviceaccount\.com/);
    expect(validatePrincipalEntry("domain:example.com").error).toMatch(
      /not supported/,
    );
    expect(validatePrincipalEntry("").error).not.toBe("");
  });
});
