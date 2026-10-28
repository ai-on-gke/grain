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

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  TASK_DRAFT_STORAGE_KEY,
  clearTaskDraft,
  loadTaskDraft,
  saveTaskDraft,
} from "./taskDraft.js";

describe("taskDraft", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("returns null when no draft is stored", () => {
    expect(loadTaskDraft()).toBeNull();
  });

  it("saves and loads a valid draft", () => {
    const draft = {
      title: "Fix bug",
      description: "Details here",
      repo: "acme/repo",
      autoMerge: true,
    };
    saveTaskDraft(draft);
    expect(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).toBe(
      JSON.stringify(draft),
    );
    expect(loadTaskDraft()).toEqual(draft);
  });

  it("clears a stored draft", () => {
    saveTaskDraft({ title: "Draft to clear" });
    expect(loadTaskDraft()).not.toBeNull();
    clearTaskDraft();
    expect(loadTaskDraft()).toBeNull();
    expect(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).toBeNull();
  });

  it("handles corrupted or invalid stored JSON gracefully", () => {
    localStorage.setItem(TASK_DRAFT_STORAGE_KEY, "invalid json {");
    expect(loadTaskDraft()).toBeNull();

    localStorage.setItem(TASK_DRAFT_STORAGE_KEY, "null");
    expect(loadTaskDraft()).toBeNull();

    localStorage.setItem(TASK_DRAFT_STORAGE_KEY, "[1,2,3]");
    expect(loadTaskDraft()).toBeNull();
  });

  it("handles localStorage throwing errors gracefully", () => {
    const getItemSpy = vi
      .spyOn(Storage.prototype, "getItem")
      .mockImplementation(() => {
        throw new Error("QuotaExceededError");
      });
    const setItemSpy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new Error("QuotaExceededError");
      });
    const removeItemSpy = vi
      .spyOn(Storage.prototype, "removeItem")
      .mockImplementation(() => {
        throw new Error("QuotaExceededError");
      });

    expect(() => loadTaskDraft()).not.toThrow();
    expect(loadTaskDraft()).toBeNull();
    expect(() => saveTaskDraft({ title: "Test" })).not.toThrow();
    expect(() => clearTaskDraft()).not.toThrow();

    getItemSpy.mockRestore();
    setItemSpy.mockRestore();
    removeItemSpy.mockRestore();
  });
});
