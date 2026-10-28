import {
  Box,
  Checkbox,
  FormControlLabel,
  TextField,
  Typography,
} from "@mui/material";
import CapabilitiesPanel from "../CapabilitiesPanel.jsx";
import SecretsPanel from "../SecretsPanel.jsx";
import SettingsCard, { settingHints } from "./SettingsCard.jsx";
import { identifierField } from "../../identifierField.js";

// CredentialsSection is Settings → Legacy capabilities (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function CredentialsSection({ s, claimedSecrets }) {
  const {
    settings,
    saveChoice,
    onTextBlur,
    onIntBlur,
    onEnterKey,
    reloadSettings,
    showError,
    onSaved,
    readOnly,
  } = s;
  const { restartRequired, restartHint, restartChip } = settingHints(settings);
  const handleSecretsChanged = async () => {
    await reloadSettings();
    await onSaved?.();
  };
  return (
    <SettingsCard
      title="Legacy capabilities"
      anchor="credentials"
      allowInExternalMode
    >
      <CapabilitiesPanel
        capabilities={settings.capabilities}
        showError={showError}
        onSecretsChanged={handleSecretsChanged}
        deploymentWide={
          <>
            <Box
              component="fieldset"
              disabled={Boolean(readOnly)}
              sx={{ border: 0, p: 0, m: 0, minInlineSize: 0 }}
            >
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

              <Typography variant="subtitle2" sx={{ mt: 3 }}>
                GCP
              </Typography>
              <TextField
                key={`gcpProject-${settings.gcpProject || ""}`}
                name="gcpProject"
                data-setting-id="gcpProject"
                label="GCP project"
                helperText="Optional. The Google Cloud project gcp-key and gemini-key mint short-lived keys in, such as my-project-123. Either capability is not ready until it is set."
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
                helperText="Optional. The service account those keys are minted for, such as grain-minter@my-project-123.iam.gserviceaccount.com. The minter's own key is set on the GCP key capability's page, in the list above."
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
            </Box>

            <SecretsPanel
              claimed={claimedSecrets}
              showError={showError}
              onSecretsChanged={handleSecretsChanged}
            />
          </>
        }
      />
    </SettingsCard>
  );
}
