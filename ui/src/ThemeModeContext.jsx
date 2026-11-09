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

import { createContext, useContext, useMemo, useState } from "react";
import { STORAGE_KEY, loadStoredMode } from "@grain/ui-shared";

// A default context value (rather than null) means components that read
// this outside a ThemeModeProvider -- notably SettingsOverlay's existing
// tests, which render it standalone -- just see the "auto" default
// instead of needing every render call updated to wrap one in.
const ThemeModeContext = createContext({ mode: "auto", setMode: () => {} });

export function ThemeModeProvider({ children }) {
  const [mode, setModeState] = useState(loadStoredMode);

  const setMode = (next) => {
    setModeState(next);
    try {
      // "auto" is the default, so it is represented by the key's absence
      // rather than an explicit value -- that way a deployment upgraded
      // from before this setting existed still reads as "auto".
      if (next === "auto") localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Storage can be unavailable (private browsing, disabled cookies);
      // the choice still applies for the rest of this session.
    }
  };

  const value = useMemo(() => ({ mode, setMode }), [mode]);

  return (
    <ThemeModeContext.Provider value={value}>
      {children}
    </ThemeModeContext.Provider>
  );
}

export function useThemeMode() {
  return useContext(ThemeModeContext);
}
