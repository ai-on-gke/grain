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

import { Injectable, computed, effect, signal } from "@angular/core";

export type ThemeMode = "light" | "dark" | "auto";

// The same localStorage key ui/src/ThemeModeContext.jsx (via
// ui-shared/themeMode.js) reads, so one setting themes both UIs.
export const THEME_STORAGE_KEY = "grain.themeMode";
// The per-browser default for pages that exist in both UIs: "1" sends
// the classic page to its /next/ counterpart (design doc, "Moving pages
// over"). The classic page reads the same key.
export const PREFER_NEXT_STORAGE_KEY = "grain.ui.preferNext";

export function readStorage(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeStorage(key: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // Storage unavailable: the choice lasts for this page only.
  }
}

function storedMode(): ThemeMode {
  const raw = readStorage(THEME_STORAGE_KEY);
  return raw === "light" || raw === "dark" || raw === "auto" ? raw : "auto";
}

function systemPrefersDark(): boolean {
  return (
    typeof window !== "undefined" &&
    !!window.matchMedia &&
    window.matchMedia("(prefers-color-scheme: dark)").matches
  );
}

@Injectable({ providedIn: "root" })
export class ThemeService {
  private readonly _mode = signal<ThemeMode>(storedMode());
  private readonly _systemDark = signal(systemPrefersDark());
  private readonly _preferNext = signal(
    readStorage(PREFER_NEXT_STORAGE_KEY) === "1",
  );

  readonly mode = this._mode.asReadonly();
  readonly preferNext = this._preferNext.asReadonly();
  readonly resolved = computed<"light" | "dark">(() => {
    const m = this._mode();
    if (m !== "auto") return m;
    return this._systemDark() ? "dark" : "light";
  });

  constructor() {
    if (typeof window !== "undefined" && window.matchMedia) {
      window
        .matchMedia("(prefers-color-scheme: dark)")
        .addEventListener?.("change", (e) => this._systemDark.set(e.matches));
    }
    effect(() => {
      if (typeof document !== "undefined") {
        document.documentElement.dataset["theme"] = this.resolved();
      }
    });
  }

  toggle(): void {
    this.setMode(this.resolved() === "dark" ? "light" : "dark");
  }

  setMode(mode: ThemeMode): void {
    this._mode.set(mode);
    writeStorage(THEME_STORAGE_KEY, mode);
  }

  setPreferNext(on: boolean): void {
    this._preferNext.set(on);
    writeStorage(PREFER_NEXT_STORAGE_KEY, on ? "1" : null);
  }
}
