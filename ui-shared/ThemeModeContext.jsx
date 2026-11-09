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

import React, {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { getSystemDark, loadStoredMode, STORAGE_KEY } from "./themeMode.js";

export const ThemeModeContext = createContext({
  mode: "auto",
  resolvedMode: "light",
  setMode: () => {},
});

export function ThemeModeProvider({ children }) {
  const [mode, setModeState] = useState(loadStoredMode);
  const [systemDark, setSystemDark] = useState(getSystemDark);

  useEffect(() => {
    if (typeof window === "undefined" || !window.matchMedia) return undefined;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (e) => setSystemDark(e.matches);
    setSystemDark(mq.matches);
    if (mq.addEventListener) {
      mq.addEventListener("change", onChange);
      return () => mq.removeEventListener("change", onChange);
    }
    if (mq.addListener) {
      mq.addListener(onChange);
      return () => mq.removeListener(onChange);
    }
    return undefined;
  }, []);

  const setMode = (next) => {
    setModeState(next);
    try {
      if (next === "auto") localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Ignore storage errors.
    }
  };

  const resolvedMode =
    mode === "auto" || mode === "system"
      ? systemDark
        ? "dark"
        : "light"
      : mode;

  useEffect(() => {
    if (typeof document === "undefined") return;
    document.documentElement.dataset.theme = resolvedMode;
  }, [resolvedMode]);

  const value = useMemo(
    () => ({ mode, resolvedMode, setMode }),
    [mode, resolvedMode],
  );

  return (
    <ThemeModeContext.Provider value={value}>
      {children}
    </ThemeModeContext.Provider>
  );
}

export const SharedThemeModeProvider = ThemeModeProvider;

export function useThemeMode() {
  return useContext(ThemeModeContext);
}
