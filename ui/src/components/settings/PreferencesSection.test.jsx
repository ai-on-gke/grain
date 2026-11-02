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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import PreferencesSection from "./PreferencesSection.jsx";
import { ThemeModeProvider } from "../../ThemeModeContext.jsx";
import { TimelineOrderProvider } from "../../TimelineOrderContext.jsx";
import { PreferencesProvider } from "../../PreferencesContext.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

// Settings → Preferences is this browser's own: what it chooses is kept
// in localStorage and applies at once, and nothing is sent to the
// deployment's settings (design/design/settings-panel.design.md §3.2).
const section = (props = {}) => (
  <PreferencesProvider>
    <ThemeModeProvider>
      <TimelineOrderProvider>
        <PreferencesSection {...props} />
      </TimelineOrderProvider>
    </ThemeModeProvider>
  </PreferencesProvider>
);

describe("PreferencesSection", () => {
  beforeEach(() => {
    api.mockResolvedValue({});
  });
  afterEach(() => {
    api.mockReset();
    localStorage.clear();
  });

  // With no choice made here, times follow the deployment's clock when it
  // has one, and the unset option says so rather than "Browser default".
  it("names the deployment's clock as the default when it has one", () => {
    render(section({ deploymentTimeZone: "Europe/Paris" }));
    const zone = screen.getByRole("combobox", { name: "Time zone" });
    expect(zone).toHaveValue("");
    expect(
      screen.getByRole("option", { name: "Deployment default (Europe/Paris)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/follow this deployment's clock, Europe\/Paris/),
    ).toBeInTheDocument();
  });

  it("defaults to Auto and applies an appearance at once without saving", async () => {
    const user = userEvent.setup();
    render(section());

    expect(screen.getByRole("radio", { name: "Auto" })).toBeChecked();
    await user.click(screen.getByRole("radio", { name: "Dark" }));

    expect(screen.getByRole("radio", { name: "Dark" })).toBeChecked();
    expect(localStorage.getItem("grain.themeMode")).toBe("dark");
    expect(api).not.toHaveBeenCalledWith(
      "/api/settings",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("reflects a stored appearance and timeline order", () => {
    localStorage.setItem("grain.themeMode", "light");
    localStorage.setItem("grain.timelineOrder", "oldest");
    render(section());

    expect(screen.getByRole("radio", { name: "Light" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Oldest first" })).toBeChecked();
  });

  it("defaults the timeline to newest first and applies a choice at once", async () => {
    const user = userEvent.setup();
    render(section());

    expect(screen.getByRole("radio", { name: "Newest first" })).toBeChecked();
    await user.click(screen.getByRole("radio", { name: "Oldest first" }));

    expect(localStorage.getItem("grain.timelineOrder")).toBe("oldest");
    expect(api).not.toHaveBeenCalledWith(
      "/api/settings",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("picks a time zone from a select whose label sits above it", async () => {
    const user = userEvent.setup();
    render(section());

    const zone = screen.getByRole("combobox", { name: "Time zone" });
    const label = zone.closest(".MuiFormControl-root").querySelector("label");
    expect(label.className).toContain("MuiInputLabel-shrink");
    await user.selectOptions(zone, "America/New_York");
    expect(zone.value).toBe("America/New_York");
  });

  it("opens Customize sidebar", async () => {
    const user = userEvent.setup();
    render(section());

    await user.click(screen.getByRole("button", { name: "Customize sidebar" }));
    expect(
      screen.getByText("Customize sidebar", { selector: "h2" }),
    ).toBeInTheDocument();
  });
});
