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
import Sparkline from "./Sparkline.jsx";

describe("Sparkline", () => {
  it("renders an empty placeholder with fewer than two points", () => {
    render(<Sparkline data={[1]} />);
    expect(screen.getByLabelText("Not enough data yet")).toBeInTheDocument();
  });

  it("renders a placeholder when given no data at all", () => {
    render(<Sparkline data={null} />);
    expect(screen.getByLabelText("Not enough data yet")).toBeInTheDocument();
  });

  it("shows a formatted latest value even before there is enough history", () => {
    render(<Sparkline data={[37]} valueLabel={(value) => `${value}%`} />);
    expect(screen.getByText("37%")).toBeInTheDocument();
  });

  it("shows the latest value beside a populated chart", () => {
    render(
      <Sparkline data={[1, 2, 3]} valueLabel={(value) => `${value} MB`} />,
    );
    expect(screen.getByText("3 MB")).toBeInTheDocument();
  });

  it("draws a polyline through every point", () => {
    render(<Sparkline data={[1, 2, 3]} />);
    const svg = screen.getByLabelText("Trend, latest value 3");
    const polyline = svg.querySelector("polyline");
    expect(polyline.getAttribute("points").trim().split(" ")).toHaveLength(3);
  });

  it("handles a flat series without dividing by zero", () => {
    render(<Sparkline data={[5, 5, 5]} />);
    const svg = screen.getByLabelText("Trend, latest value 5");
    expect(svg.querySelector("polyline")).toBeInTheDocument();
  });
});
