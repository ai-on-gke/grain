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

// taskDraft.js stores and loads the draft of a task in localStorage under
// TASK_DRAFT_STORAGE_KEY (grain/task-91).
//
// If a user leaves the task authoring window, their work (title, description,
// repo, base branch, extra repos, custom fields and settings) is preserved
// so they can return to it. Submitting the task or explicitly discarding
// the draft clears the stored draft.

export const TASK_DRAFT_STORAGE_KEY = "grain.task.draft";

// loadTaskDraft returns the stored draft from localStorage, or null if
// none is stored or the stored value is invalid.
export function loadTaskDraft() {
  let raw = null;
  try {
    raw = localStorage.getItem(TASK_DRAFT_STORAGE_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed;
    }
    return null;
  } catch {
    return null;
  }
}

// saveTaskDraft saves a task draft to localStorage, or clears it if draft is null.
export function saveTaskDraft(draft) {
  try {
    if (!draft || typeof draft !== "object") {
      localStorage.removeItem(TASK_DRAFT_STORAGE_KEY);
    } else {
      localStorage.setItem(TASK_DRAFT_STORAGE_KEY, JSON.stringify(draft));
    }
  } catch {
    // Storage can be unavailable (private browsing, disabled storage, quota); ignore.
  }
}

// clearTaskDraft removes the stored task draft from localStorage.
export function clearTaskDraft() {
  try {
    localStorage.removeItem(TASK_DRAFT_STORAGE_KEY);
  } catch {
    // Storage can be unavailable; ignore.
  }
}
