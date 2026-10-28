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

import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "./api.js";
import { statusLine, useSettingsSection } from "./useSettingsSection.js";
import { leavingLosesEdits } from "./unsavedEdits.js";

vi.mock("./api.js", () => ({ default: vi.fn() }));

async function loaded(
  initial = { pollInterval: "30s", showClosedByDefault: false },
) {
  api.mockImplementation((path, opts) => {
    if (path === "/api/settings" && !opts) return Promise.resolve(initial);
    return Promise.reject(new Error("unexpected " + path));
  });
  const onSaved = vi.fn();
  const showError = vi.fn();
  const dismissError = vi.fn();
  const hook = renderHook(() =>
    useSettingsSection({ onSaved, showError, dismissError }),
  );
  await act(() => hook.result.current.reloadSettings());
  return { ...hook, onSaved, showError, dismissError };
}

// docs/design/settings-panel.md §3.4: one save and dirty-state model.
describe("useSettingsSection", () => {
  afterEach(() => api.mockReset());

  it("reads the settings and starts out saved", async () => {
    const { result } = await loaded();
    expect(result.current.settings.pollInterval).toBe("30s");
    expect(result.current.status).toBe("saved");
    expect(statusLine(result.current)).toBe("All changes saved");
  });

  it("is saving while a PUT is in flight, and shows what the server answered", async () => {
    const { result, onSaved } = await loaded();
    let answer;
    api.mockImplementation(() => new Promise((resolve) => (answer = resolve)));
    let done;
    act(() => {
      done = result.current.saveChoice({ pollInterval: "1m" });
    });
    expect(result.current.status).toBe("saving");
    expect(statusLine(result.current)).toBe("Saving…");
    expect(result.current.settings.pollInterval).toBe("1m");
    await act(async () => {
      answer({ pollInterval: "60s" });
      await done;
    });
    expect(result.current.settings.pollInterval).toBe("60s");
    expect(result.current.status).toBe("saved");
    expect(onSaved).toHaveBeenCalled();
  });

  it("rolls a failed save back, says which field, and retries", async () => {
    const { result, showError } = await loaded();
    api.mockRejectedValueOnce(new Error("boom"));
    await act(() => result.current.saveChoice({ showClosedByDefault: true }));
    expect(result.current.settings.showClosedByDefault).toBe(false);
    expect(result.current.status).toBe("error");
    expect(statusLine(result.current)).toBe(
      "Couldn't save Show closed tasks by default",
    );
    expect(result.current.error.message).toBe("boom");
    expect(showError).toHaveBeenCalled();

    api.mockResolvedValueOnce({ showClosedByDefault: true });
    await act(() => result.current.error.retry());
    await waitFor(() => expect(result.current.status).toBe("saved"));
    expect(result.current.settings.showClosedByDefault).toBe(true);
  });

  it("stops saying a save failed once the box is put back to the saved value", async () => {
    const { result, dismissError } = await loaded({
      pollInterval: "10s",
      maxAgentTurns: 5,
    });
    api.mockRejectedValueOnce(new Error('time: invalid duration "banana"'));
    await act(async () =>
      result.current.onTextBlur("pollInterval")({
        target: { value: "banana" },
      }),
    );
    expect(statusLine(result.current)).toBe("Couldn't save Poll interval");
    expect(result.current.settings.pollInterval).toBe("10s");

    // Another field's box left alone keeps the error up.
    await act(async () =>
      result.current.onIntBlur("maxAgentTurns")({
        target: { value: "5", validity: {} },
      }),
    );
    expect(result.current.status).toBe("error");
    expect(dismissError).not.toHaveBeenCalled();

    api.mockClear();
    await act(async () =>
      result.current.onTextBlur("pollInterval")({ target: { value: "10s" } }),
    );
    expect(api).not.toHaveBeenCalled();
    expect(result.current.status).toBe("saved");
    expect(statusLine(result.current)).toBe("All changes saved");
    expect(dismissError).toHaveBeenCalledWith(
      'time: invalid duration "banana"',
    );
  });

  it("clears a failed number box's error when it is put back", async () => {
    const { result } = await loaded({ maxAgentTurns: 5, budget: 1.5 });
    api.mockRejectedValueOnce(new Error("boom"));
    const intBlur = result.current.onIntBlur("maxAgentTurns");
    await act(async () => intBlur({ target: { value: "-1", validity: {} } }));
    expect(result.current.status).toBe("error");
    await act(async () => intBlur({ target: { value: "5", validity: {} } }));
    expect(result.current.status).toBe("saved");

    api.mockRejectedValueOnce(new Error("boom"));
    const floatBlur = result.current.onFloatBlur("budget");
    await act(async () => floatBlur({ target: { value: "-2", validity: {} } }));
    expect(result.current.status).toBe("error");
    await act(async () =>
      floatBlur({ target: { value: "1.5", validity: {} } }),
    );
    expect(result.current.status).toBe("saved");
  });

  it("is dirty while a Save card holds an edit, and the leave guard asks", async () => {
    const { result, unmount } = await loaded();
    expect(leavingLosesEdits()).toBe(false);
    act(() => result.current.markDirty("Standing instructions", true));
    expect(result.current.status).toBe("dirty");
    expect(statusLine(result.current)).toBe(
      "Unsaved changes in Standing instructions",
    );
    expect(leavingLosesEdits()).toBe(true);

    act(() => result.current.markDirty("Standing instructions", false));
    expect(result.current.status).toBe("saved");
    expect(leavingLosesEdits()).toBe(false);
    unmount();
  });

  it("stops guarding the page once Settings is closed", async () => {
    const { result, unmount } = await loaded();
    act(() => result.current.markDirty("Setup script", true));
    expect(leavingLosesEdits()).toBe(true);
    unmount();
    expect(leavingLosesEdits()).toBe(false);
  });

  it("lists every Save card that holds an edit, and the error over them", async () => {
    const { result, unmount } = await loaded();
    act(() => {
      result.current.markDirty("Standing instructions", true);
      result.current.markDirty("Setup script", true);
    });
    expect(result.current.dirtyCards).toEqual([
      "Standing instructions",
      "Setup script",
    ]);
    api.mockRejectedValueOnce(new Error("boom"));
    await act(() => result.current.saveChoice({ showClosedByDefault: true }));
    expect(result.current.status).toBe("error");
    unmount();
  });

  it("saves a number box on blur only when it changed", async () => {
    const { result } = await loaded({ maxAgentTurns: 5 });
    api.mockResolvedValue({ maxAgentTurns: 7 });
    const blur = result.current.onIntBlur("maxAgentTurns");
    await act(async () => blur({ target: { value: "5", validity: {} } }));
    expect(api).not.toHaveBeenCalledWith("/api/settings", expect.anything());
    await act(async () => blur({ target: { value: "7", validity: {} } }));
    expect(api).toHaveBeenCalledWith("/api/settings", {
      method: "PUT",
      body: JSON.stringify({ maxAgentTurns: 7 }),
    });
  });

  it("refuses an out-of-range number box before saving, and names the label", async () => {
    const { result, showError } = await loaded({ maxAutoSubmitDepth: 2 });
    api.mockClear();
    const input = {
      value: "-1",
      defaultValue: "2",
      min: "1",
      validity: {},
    };
    await act(async () =>
      result.current.onIntBlur("maxAutoSubmitDepth", {
        defaultVal: 2,
        emptyVal: 2,
      })({ target: input }),
    );
    expect(api).not.toHaveBeenCalled();
    expect(input.value).toBe("2");
    expect(result.current.status).toBe("error");
    expect(statusLine(result.current)).toBe(
      "Couldn't save Max unreviewed follow-up depth",
    );
    expect(showError).toHaveBeenCalledWith(
      "Max unreviewed follow-up depth must be at least 1; -1 was not saved.",
    );
  });

  it("restores the default when a required number box is cleared while already at default", async () => {
    const { result } = await loaded({ maxAutoSubmitDepth: 2 });
    api.mockClear();
    const input = {
      value: "",
      defaultValue: "2",
      min: "1",
      validity: {},
    };
    await act(async () =>
      result.current.onIntBlur("maxAutoSubmitDepth", {
        defaultVal: 2,
        emptyVal: 2,
      })({ target: input }),
    );
    expect(api).not.toHaveBeenCalled();
    expect(input.value).toBe("2");
  });

  it("rewrites a leading API key in a server error to the field's label", async () => {
    const { result, showError } = await loaded({ maxAutoSubmitDepth: 2 });
    api.mockRejectedValueOnce(
      new Error("maxAutoSubmitDepth must be at least 1"),
    );
    await act(() => result.current.saveChoice({ maxAutoSubmitDepth: 0 }));
    expect(result.current.error.message).toBe(
      "Max unreviewed follow-up depth must be at least 1",
    );
    expect(showError).toHaveBeenCalledWith(
      "Max unreviewed follow-up depth must be at least 1",
    );
  });
});
