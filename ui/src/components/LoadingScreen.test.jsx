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
import { describe, expect, it } from "vitest";
import LoadingScreen from "./LoadingScreen.jsx";

// setupTests.js stubs HTMLCanvasElement.prototype.getContext to return
// null project-wide, so GrainMark falls back to its still <img> here the
// same way it does in any other test that doesn't stub its own canvas
// context -- this is asserting the hero size actually reaches GrainMark,
// not exercising the animated path itself (see GrainMark.test.jsx).
describe("LoadingScreen", () => {
  it("shows the brand mark at hero size", () => {
    render(<LoadingScreen />);

    const mark = screen.getByTitle("grain");
    expect(mark.tagName).toBe("IMG");
    expect(mark).toHaveAttribute("width", "320");
    expect(mark).toHaveAttribute("height", "320");
  });
});
