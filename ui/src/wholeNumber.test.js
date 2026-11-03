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
import { wholeNumber } from "./wholeNumber.js";

describe("wholeNumber", () => {
  it("reads whole numbers, exponent notation included", () => {
    expect(wholeNumber("7")).toBe(7);
    expect(wholeNumber(" 12 ")).toBe(12);
    expect(wholeNumber("1e3")).toBe(1000);
    expect(wholeNumber("8E3")).toBe(8000);
    expect(wholeNumber(4)).toBe(4);
    expect(wholeNumber("-2")).toBe(-2);
  });

  it("is NaN for anything that is not a whole number", () => {
    for (const v of ["", "   ", null, undefined, "2.5", "abc", "1e", "12abc"]) {
      expect(wholeNumber(v)).toBeNaN();
    }
  });
});
