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

import { useContext } from "react";
import { Alert, Link, Typography } from "@mui/material";
import {
  DEFAULT_GIT_IDENTITY_EMAIL,
  DEFAULT_GIT_IDENTITY_NAME,
} from "../../gitIdentity.js";
import { useDeployment } from "../../useDeployment.js";
import GitConnections from "./GitConnections.jsx";
import SettingsCard, { SettingsReadOnlyContext } from "./SettingsCard.jsx";

// GitSection is Settings → Git (docs/design/settings-panel.md §3.2): the
// deployment's git connections and the identity runs commit as, read off
// GET /api/deployment (useDeployment) and kept through the
// /api/deployment/git-connections routes.
//
// The connections' metadata and which repository uses which are
// config-repo only (docs/design/config-repo-settings.md §2.1), so the
// list is drawn read-only; a connection's token or key is secret
// material, still written only through the secret store (Settings →
// Legacy capabilities, /api/secrets), never into grain/config.
export default function GitSection({ onSaved, showError, onSection }) {
  const readOnly = useContext(SettingsReadOnlyContext);
  const { deployment, error, reloadDeployment } = useDeployment({
    onSaved,
    showError,
  });
  return (
    <SettingsCard label="Git connections" anchor="git">
      {error ? (
        <Alert severity="error" sx={{ mb: 2 }}>
          Could not load the deployment&apos;s connections:{" "}
          {error.message || String(error)}
        </Alert>
      ) : (
        <GitConnections
          deployment={deployment}
          deploymentGitName={DEFAULT_GIT_IDENTITY_NAME}
          deploymentGitEmail={DEFAULT_GIT_IDENTITY_EMAIL}
          canManageTokens={!readOnly}
          showError={showError}
          onDeploymentChanged={async () => {
            await reloadDeployment();
            await onSaved?.();
          }}
        />
      )}
      {readOnly && (
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ mt: 2 }}
          data-testid="git-secret-note"
        >
          A connection&apos;s token or key is never part of the config repo: it
          is set write-only under{" "}
          <Link
            href="/settings/credentials"
            onClick={(evt) => {
              if (!onSection || evt.button !== 0 || evt.metaKey || evt.ctrlKey)
                return;
              evt.preventDefault();
              onSection("credentials");
            }}
          >
            Settings → Legacy capabilities
          </Link>
          , or when a task asks for it.
        </Typography>
      )}
    </SettingsCard>
  );
}
