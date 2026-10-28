import { createContext, useContext, useMemo, useState } from "react";

const STORAGE_KEY = "grain.themeMode";
const MODES = ["light", "dark", "auto"];

// A default context value (rather than null) means components that read
// this outside a ThemeModeProvider -- notably SettingsOverlay's existing
// tests, which render it standalone -- just see the "auto" default
// instead of needing every render call updated to wrap one in.
const ThemeModeContext = createContext({ mode: "auto", setMode: () => {} });

function readStoredMode() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return MODES.includes(stored) ? stored : "auto";
  } catch {
    return "auto";
  }
}

export function ThemeModeProvider({ children }) {
  const [mode, setModeState] = useState(readStoredMode);

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
