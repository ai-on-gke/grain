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

import { createContext, useContext } from "react";

// Who is reading: GET /api/whoami's actor label ("alice@example.com",
// "operator (access code)"), the same label the daemon writes on a
// comment that person posts. TaskChat puts it on a message's "Sending…"
// bubble, so a message sent in a console session -- whose task grain
// itself created -- reads as the sender's until the server's copy
// replaces it, rather than as grain's.
//
// The default is "", and TaskChat falls back to the task's own author
// then, which is what a component rendered outside a provider (a unit
// test) has always seen.
const ViewerContext = createContext("");

export function ViewerProvider({ whoami, children }) {
  const label = String(whoami?.actor?.label || whoami?.actor?.id || "");
  return (
    <ViewerContext.Provider value={label}>{children}</ViewerContext.Provider>
  );
}

export function useViewerLabel() {
  return useContext(ViewerContext);
}
