import { createContext, useContext } from "react";

// The deployment's own time zone, handed to every component that prints
// a timestamp without threading a prop through half the tree
// (grain/task-368). App reads it off GET /api/config -- which is the one
// call made before the first paint, so no screen ever shows a time in
// the wrong zone and corrects itself afterwards.
//
// The default is "", the reading time.js gives to "nobody has said":
// format in whatever zone this browser is in, which is exactly what
// every one of these timestamps did before this existed. That is what a
// component rendered outside a provider sees -- notably the component
// tests, which render one standalone -- so a missing provider is the old
// behaviour rather than a crash, the same choice ThemeModeContext makes.
const TimeZoneContext = createContext("");

export function TimeZoneProvider({ zone, children }) {
  return (
    <TimeZoneContext.Provider value={zone || ""}>
      {children}
    </TimeZoneContext.Provider>
  );
}

export function useTimeZone() {
  return useContext(TimeZoneContext);
}
