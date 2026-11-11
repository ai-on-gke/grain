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
import { accessDeniedReason, canAccess } from "./access.js";

// AccessConfigContext carries GET /api/config down to the controls that
// decide what to show from its `access` (access.js), so a write button
// deep in a pane need not be handed config by every component above it.
// Its default is null -- an unlimited caller -- so a component rendered
// on its own, as a unit test does, shows everything as it always has.
const AccessConfigContext = createContext(null);

export function AccessConfigProvider({ config, children }) {
  return (
    <AccessConfigContext.Provider value={config || null}>
      {children}
    </AccessConfigContext.Provider>
  );
}

// useAccess answers canAccess and accessDeniedReason for the provided
// config, or for override when a component already holds one.
export function useAccess(override) {
  const provided = useContext(AccessConfigContext);
  const config = override || provided;
  return {
    config,
    can: (scope, repo) => canAccess(config, scope, repo),
    reason: (scope, repo) => accessDeniedReason(config, scope, repo),
  };
}
