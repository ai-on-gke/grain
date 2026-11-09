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

export const STORAGE_KEY = "grain.themeMode";
export const VALID_MODES = ["light", "dark", "auto"];

export function loadStoredMode() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return VALID_MODES.includes(stored) ? stored : "auto";
  } catch {
    return "auto";
  }
}

export function getSystemDark() {
  if (typeof window === "undefined" || !window.matchMedia) return false;
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}
