import { createContext, useContext, useMemo, useState } from "react";

const STORAGE_KEY = "grain.timelineOrder";
const ORDERS = ["newest", "oldest"];

// Which end of a task's timeline (DetailOverlay's own Timeline) is shown
// first: "newest" puts the most recent entry directly under the heading
// with the older ones trailing behind it, "oldest" is the strict
// chronological reading this replaced.
//
// Newest-first is the default because it is what somebody opening a task
// almost always came for -- what just happened -- and a task with a
// dozen attempts and a conversation behind it buries that under history
// they have already read.
//
// Per-browser (localStorage), the same treatment the theme mode gets
// (ThemeModeContext) and for the same reason: it changes nothing but how
// this reader's own screen is laid out, so it has no business in the
// deployment settings the daemon reads.
//
// A default context value (rather than null) means components that read
// this outside a TimelineOrderProvider -- notably DetailOverlay's and
// SettingsOverlay's existing tests, which render them standalone -- see
// the "newest" default instead of needing every render call updated to
// wrap one in.
const TimelineOrderContext = createContext({
  order: "newest",
  setOrder: () => {},
});

function readStoredOrder() {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return ORDERS.includes(stored) ? stored : "newest";
  } catch {
    return "newest";
  }
}

export function TimelineOrderProvider({ children }) {
  const [order, setOrderState] = useState(readStoredOrder);

  const setOrder = (next) => {
    setOrderState(next);
    try {
      // "newest" is the default, so it is represented by the key's
      // absence rather than an explicit value -- the same shape
      // ThemeModeContext stores its own default in.
      if (next === "newest") localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, next);
    } catch {
      // Storage can be unavailable (private browsing, disabled cookies);
      // the choice still applies for the rest of this session.
    }
  };

  const value = useMemo(() => ({ order, setOrder }), [order]);

  return (
    <TimelineOrderContext.Provider value={value}>
      {children}
    </TimelineOrderContext.Provider>
  );
}

export function useTimelineOrder() {
  return useContext(TimelineOrderContext);
}
