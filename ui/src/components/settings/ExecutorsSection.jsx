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

import { useEffect, useState } from "react";
import { Link, Typography } from "@mui/material";
import api from "../../api.js";
import ExecutorsPanel from "../ExecutorsPanel.jsx";
import SettingsCard from "./SettingsCard.jsx";

// ExecutorsSection is Settings → Executors: the join tokens, which are a
// setting. Which executors are connected is status, and is on System →
// Sandboxes (design/design/settings-panel.design.md §2.1).
//
// A join token is a runtime credential, not configuration: the config
// repo holds none and its routes take writes in external mode, so the
// card stays usable there (settings-mutability.spec.md D-9).
export default function ExecutorsSection({ showError, onOpenSystem }) {
  const [connected, setConnected] = useState(null);
  useEffect(() => {
    let live = true;
    api("/api/executors")
      .then((list) => live && setConnected((list || []).length))
      .catch(() => live && setConnected(null));
    return () => {
      live = false;
    };
  }, []);
  return (
    <SettingsCard
      title="Executor join tokens"
      anchor="executors"
      allowInExternalMode
    >
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        {connected === null ? "" : `${connected} connected · `}
        <Link
          component="button"
          type="button"
          onClick={() => onOpenSystem?.("sandboxes")}
          data-testid="executors-system-link"
        >
          see System → Sandboxes
        </Link>
      </Typography>
      <ExecutorsPanel showError={showError} />
    </SettingsCard>
  );
}
