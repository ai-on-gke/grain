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
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import {
  effectiveTimeZone,
  PreferencesProvider,
  usePreferences,
} from "./PreferencesContext.jsx";
import { browserTimeZone } from "./time.js";

function Probe() {
  const { timeZone, setTimeZone } = usePreferences();
  return (
    <div>
      <span data-testid="zone">{timeZone}</span>
      <button onClick={() => setTimeZone(" Europe/Paris ")}>Paris</button>
      <button onClick={() => setTimeZone("")}>Clear</button>
    </div>
  );
}

describe("PreferencesContext", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("reads the time zone from the deployment-wide key", () => {
    localStorage.setItem("grain.timeZone", "Asia/Tokyo");
    render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    );
    expect(screen.getByTestId("zone")).toHaveTextContent("Asia/Tokyo");
  });

  it("updates and persists the time zone, and clears it", async () => {
    const user = userEvent.setup();
    render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    );
    expect(screen.getByTestId("zone")).toHaveTextContent("");

    await user.click(screen.getByRole("button", { name: "Paris" }));
    expect(screen.getByTestId("zone")).toHaveTextContent("Europe/Paris");
    expect(localStorage.getItem("grain.timeZone")).toBe("Europe/Paris");

    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(localStorage.getItem("grain.timeZone")).toBeNull();
  });

  it("takes the browser's own zone as effective on Browser default, and the picked one otherwise", () => {
    expect(effectiveTimeZone()).toBe(browserTimeZone());
    localStorage.setItem("grain.timeZone", "Asia/Tokyo");
    expect(effectiveTimeZone()).toBe("Asia/Tokyo");
  });

  it("falls back to storage outside a provider", () => {
    localStorage.setItem("grain.timeZone", "UTC");
    render(<Probe />);
    expect(screen.getByTestId("zone")).toHaveTextContent("UTC");
  });

  it("keeps no username", () => {
    let prefs;
    function Grab() {
      prefs = usePreferences();
      return null;
    }
    render(
      <PreferencesProvider>
        <Grab />
      </PreferencesProvider>,
    );
    expect(Object.keys(prefs).sort()).toEqual(["setTimeZone", "timeZone"]);
  });
});
