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
