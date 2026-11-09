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

import {
  Box,
  Checkbox,
  FormControlLabel,
  Link,
  TextField,
  Typography,
} from "@mui/material";
import { serverPath } from "../CapabilityServersPanel.jsx";
import SecretsPanel from "../SecretsPanel.jsx";
import SettingsCard, {
  ReadOnlyFieldset,
  settingHints,
} from "./SettingsCard.jsx";
import { identifierField } from "../../identifierField.js";
import { ConfigManagedNotice } from "../ConfigRepoLink.jsx";

const GCP_SERVER_PATH = serverPath("https://grain.dev/capabilities/gcp");

// CredentialsSection is the Deployment credentials card on Settings →
// Capabilities: the GitHub host, the GCP settings migrated gcp-key and
// gemini-key pools were seeded from, and every secret grain stores. It was
// the Legacy capabilities tab (design/design/settings-panel.design.md
// §3.2) until the last in-process capability was retired onto a
// capability server method and the tab's capability list had nothing left
// to show. `s` is the page's useSettingsSection().
export default function CredentialsSection({ s, claimedSecrets, secretsSeq }) {
  const {
    settings,
    saveChoice,
    onTextBlur,
    onIntBlur,
    onEnterKey,
    reloadSettings,
    showError,
    onSaved,
  } = s;
  // What is read-only whatever the role (external mode, a config-managed
  // section); the page's s.readOnly also folds in Capabilities' own
  // capabilities:admin, which this card does not follow.
  const readOnly = s.readOnlyForAnyRole ?? s.readOnly;
  const { restartRequired, restartHint, restartChip } = settingHints(settings);
  // The card sits on Capabilities, but its fields save through PUT
  // /api/settings (settings:write) and its secrets through /api/secrets
  // (secrets:write), so each part follows its own scope rather than
  // Capabilities' capabilities:admin (CapabilitiesSection clears the
  // section's refusal for it): a role holding capabilities:admin alone is
  // not offered controls the server refuses with 403, and one holding
  // settings:write and secrets:write without it is not refused controls
  // the server allows (2026-11-08 deployment credentials walkthrough).
  const settingsRefusal = s.settingsWriteRefusal || "";
  const secretsRefusal = s.secretsWriteRefusal || "";
  const cardRefusals = [settingsRefusal, secretsRefusal].filter(Boolean);
  const handleSecretsChanged = async () => {
    await reloadSettings();
    await onSaved?.();
  };
  return (
    <SettingsCard
      title="Deployment credentials"
      anchor="credentials"
      allowInExternalMode
    >
      <Box
        id="deployment-wide"
        component="section"
        aria-label="Deployment-wide settings"
        data-testid="deployment-credentials"
      >
        {cardRefusals.length > 0 && (
          <Typography
            variant="body2"
            color="text.secondary"
            data-testid="credentials-read-only"
            sx={{ mb: 1 }}
          >
            {settingsRefusal && secretsRefusal
              ? "Read-only for you."
              : settingsRefusal
                ? "The GitHub host and GCP settings are read-only for you."
                : "Secrets are read-only for you."}{" "}
            {cardRefusals.join(" ")} An operator can change your role in
            Settings &rarr; Access.
          </Typography>
        )}
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          Settings shared across the deployment -- the GitHub host grain talks
          to, and the GCP project and legacy service-account pool seeded onto
          the <Link href={GCP_SERVER_PATH}>gcp capability server&apos;s</Link>{" "}
          <code>gcp-key</code> and <code>gemini-key</code> methods -- and the
          secrets grain stores.
        </Typography>
        <>
          {/* Config-managed grain_config fields: refused by the API while
              settings are locked as well as in external mode
              (settings-mutability.spec.md D-2). The secrets below stay
              writable either way. */}
          <ReadOnlyFieldset
            disabled={
              Boolean(readOnly) ||
              Boolean(s.configLocked) ||
              Boolean(settingsRefusal)
            }
            title={settingsRefusal || undefined}
          >
            {!readOnly && !settingsRefusal && s.configLocked && (
              <ConfigManagedNotice
                what="The GitHub host and GCP settings"
                paths={["tables/grain_config.json"]}
              />
            )}
            <TextField
              key={`githubHost-${settings.githubHost || ""}`}
              name="githubHost"
              data-setting-id="githubHost"
              label="GitHub host"
              helperText={restartHint(
                "githubHost",
                "The GitHub grain talks to for pull requests, webhooks and the merge queue: a host name with an optional :port, such as github.com or ghe.example.com:8443 -- not a URL.",
              )}
              defaultValue={settings.githubHost || ""}
              onBlur={onTextBlur("githubHost")}
              onKeyDown={onEnterKey}
              autoComplete="off"
              inputProps={identifierField}
              fullWidth
              margin="normal"
              InputProps={{ endAdornment: restartChip("githubHost") }}
            />
            <FormControlLabel
              control={
                <Checkbox
                  name="githubInsecureHttp"
                  data-setting-id="githubInsecureHttp"
                  checked={!!settings.githubInsecureHttp}
                  onChange={(evt) =>
                    saveChoice({
                      githubInsecureHttp: evt.target.checked,
                    })
                  }
                />
              }
              label={
                <>
                  Speak plain HTTP to GitHub host{" "}
                  <span className="hint">
                    for a test or mock GitHub only; never for a real one
                  </span>
                  {restartChip("githubInsecureHttp")}
                  {restartRequired.has("githubInsecureHttp") && (
                    <span className="hint">
                      {restartHint("githubInsecureHttp", "")}
                    </span>
                  )}
                </>
              }
              sx={{ display: "flex", mt: 1 }}
            />

            <Typography id="gcp-settings" variant="subtitle2" sx={{ mt: 3 }}>
              GCP
            </Typography>
            <TextField
              key={`gcpProject-${settings.gcpProject || ""}`}
              name="gcpProject"
              data-setting-id="gcpProject"
              label="GCP project"
              helperText={
                <>
                  Optional. The deployment&apos;s Google Cloud project, such as
                  my-project-123, used for IAP reconciliation and to seed
                  migrated <code>gcp-key</code> and <code>gemini-key</code>{" "}
                  settings. Live <code>gcp-key</code> permission sets and{" "}
                  <code>gemini-key</code> projects are configured on the{" "}
                  <Link href={GCP_SERVER_PATH}>gcp capability server</Link>.
                </>
              }
              defaultValue={settings.gcpProject || ""}
              onBlur={onTextBlur("gcpProject")}
              onKeyDown={onEnterKey}
              autoComplete="off"
              inputProps={identifierField}
              fullWidth
              margin="normal"
            />
            <TextField
              key={`gcpEmail-${settings.gcpServiceAccountEmail || ""}`}
              name="gcpServiceAccountEmail"
              data-setting-id="gcpServiceAccountEmail"
              label="GCP service account email"
              helperText={
                <>
                  Optional. The legacy agent service account migrated{" "}
                  <code>gcp-key</code> pools mint keys for, such as
                  grain-agent@my-project-123.iam.gserviceaccount.com. Set the
                  minter credential on the{" "}
                  <Link href={GCP_SERVER_PATH}>gcp capability server</Link>{" "}
                  (Admin credential or Bind a secret), or store{" "}
                  <code>gcp-key-minter</code> /{" "}
                  <code>gcp-key-minter-&lt;name&gt;</code> (key{" "}
                  <code>value</code>) under{" "}
                  <Link href="#other-secrets">Other secrets</Link> below.
                </>
              }
              defaultValue={settings.gcpServiceAccountEmail || ""}
              onBlur={onTextBlur("gcpServiceAccountEmail")}
              onKeyDown={onEnterKey}
              autoComplete="off"
              inputProps={identifierField}
              fullWidth
              margin="normal"
            />
            <TextField
              key={`gcpCount-${settings.gcpServiceAccountCount ?? ""}`}
              name="gcpServiceAccountCount"
              data-setting-id="gcpServiceAccountCount"
              label="GCP service account count"
              helperText="How many service accounts to mint from (default 1). Above 1, grain uses <prefix>-0@..., <prefix>-1@... and so on in turn, which raises how many keys can be live at once."
              type="number"
              inputProps={{ min: 1, step: 1 }}
              defaultValue={String(settings.gcpServiceAccountCount || 1)}
              onBlur={onIntBlur("gcpServiceAccountCount", {
                defaultVal: 1,
                emptyVal: 1,
              })}
              onKeyDown={onEnterKey}
              autoComplete="off"
              fullWidth
              margin="normal"
            />
          </ReadOnlyFieldset>

          <SecretsPanel
            claimed={claimedSecrets}
            refreshKey={secretsSeq}
            showError={showError}
            onSecretsChanged={handleSecretsChanged}
            readOnly={Boolean(secretsRefusal)}
            readOnlyReason={secretsRefusal}
          />
        </>
      </Box>
    </SettingsCard>
  );
}
