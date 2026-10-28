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
import { browserTimeZone } from "./time.js";

// This browser's own preferences that have no context of their own --
// today the preferred time zone. It was UserContext.jsx's, beside the
// browser-chosen username that docs/design/settings-panel.md §6 removed:
// grain has no users, so nothing here is kept per user.
// storageMigration.js moved the old grain.userTimeZone key here.
export const TIMEZONE_STORAGE_KEY = "grain.timeZone";

export function readStoredTimeZone() {
  try {
    const stored = localStorage.getItem(TIMEZONE_STORAGE_KEY);
    return stored ? stored.trim() : "";
  } catch {
    return "";
  }
}

// effectiveTimeZone is the zone the person at this browser is in, as far
// as grain can tell: the one picked in Settings → Preferences, or -- left
// on "Browser default" -- the browser's own. It is what api.js tells the
// server on every write (X-Grain-Time-Zone), so a task filed or messaged
// from here tells its run the time in this zone rather than in UTC.
// Deliberately not the deployment's clock: that is the server's to fall
// back on for tasks nobody filed from a browser.
export function effectiveTimeZone() {
  return readStoredTimeZone() || browserTimeZone();
}

function storeTimeZone(zone) {
  try {
    if (zone) {
      localStorage.setItem(TIMEZONE_STORAGE_KEY, zone);
    } else {
      localStorage.removeItem(TIMEZONE_STORAGE_KEY);
    }
  } catch {
    // Storage can be unavailable (private browsing, disabled cookies);
    // the choice still applies for the rest of this session.
  }
}

const PreferencesContext = createContext(null);

export function PreferencesProvider({ children }) {
  const [timeZone, setTimeZoneState] = useState(readStoredTimeZone);
  const value = useMemo(
    () => ({
      timeZone,
      setTimeZone: (next) => {
        const trimmed = (next || "").trim();
        setTimeZoneState(trimmed);
        storeTimeZone(trimmed);
      },
    }),
    [timeZone],
  );
  return (
    <PreferencesContext.Provider value={value}>
      {children}
    </PreferencesContext.Provider>
  );
}

export function usePreferences() {
  const ctx = useContext(PreferencesContext);
  if (!ctx) {
    return {
      timeZone: readStoredTimeZone(),
      setTimeZone: (next) => storeTimeZone((next || "").trim()),
    };
  }
  return ctx;
}
