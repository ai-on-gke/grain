import { Alert } from "@mui/material";
import {
  DEFAULT_GIT_IDENTITY_EMAIL,
  DEFAULT_GIT_IDENTITY_NAME,
} from "../../gitIdentity.js";
import { useDeployment } from "../../useDeployment.js";
import GitConnections from "./GitConnections.jsx";
import SettingsCard from "./SettingsCard.jsx";

// GitSection is Settings → Git (docs/design/settings-panel.md §3.2): the
// deployment's git connections and the identity runs commit as, read off
// GET /api/deployment (useDeployment) and kept through the
// /api/deployment/git-connections routes.
export default function GitSection({ onSaved, showError }) {
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
          showError={showError}
          onDeploymentChanged={async () => {
            await reloadDeployment();
            await onSaved?.();
          }}
        />
      )}
    </SettingsCard>
  );
}
