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

import { useCallback, useEffect, useState } from "react";
import api from "./api.js";

// deploymentPath is the deployment's own record: its git connections,
// default model and model settings (GET/PUT /api/deployment), and the
// root its provider connections hang off
// (/api/deployment/agent-backends/{id}).
export const deploymentPath = "/api/deployment";

// useDeployment reads the deployment's record for Settings → Models and
// Git (design/design/settings-panel.design.md §7 stage a). A read that fails says
// so (error) instead of letting a section draw panes that would offer
// saves with nothing behind them.
//
// saveDeployment PUTs part of the record, optimistically, and puts the
// previous record back when the PUT fails. The save is done once the PUT
// has answered: onSaved (the config refresh) runs on its own rather than
// awaited, because a pane holds its controls disabled until this
// returns, and on a busy deployment /api/config can take long enough
// that every other remove button stayed greyed out after the first one
// was used (grain/task-1680).
export function useDeployment({ onSaved, showError } = {}) {
  const [deployment, setDeployment] = useState(null);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(true);

  const reloadDeployment = useCallback(async () => {
    try {
      const data = await api(deploymentPath);
      if (data && data.id) setDeployment(data);
      setError(null);
    } catch (err) {
      setError(err);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    reloadDeployment();
  }, [reloadDeployment]);

  const saveDeployment = async (payload) => {
    const previous = deployment ? { ...deployment } : {};
    setDeployment((prev) => ({ ...(prev || {}), ...payload }));
    try {
      const updated = await api(deploymentPath, {
        method: "PUT",
        body: JSON.stringify(payload),
      });
      if (updated && updated.id) {
        setDeployment(updated);
      }
      Promise.resolve()
        .then(() => onSaved?.())
        .catch((err) => {
          if (showError) showError(err);
        });
      return true;
    } catch (err) {
      setDeployment(previous);
      if (showError) showError(err);
      return false;
    }
  };

  return { deployment, error, loading, reloadDeployment, saveDeployment };
}
