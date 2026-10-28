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

import { Alert, Button, TextField, Typography } from "@mui/material";
import SettingsCard, { MultilineSetting } from "./SettingsCard.jsx";
import {
  DEFAULT_GIT_IDENTITY_EMAIL,
  DEFAULT_GIT_IDENTITY_NAME,
} from "../../gitIdentity.js";

// unsluggedTaskBranchTemplate is the template a new deployment started
// with before {slug} (grain/task-56): branches named by number alone.
const unsluggedTaskBranchTemplate = "grain/{deployment}/task-{id}";

// taskBranchSuggestion is the one-line offer of the current default
// branch template, or null when there is nothing to offer: a deployment
// still on grain/task-{id} is offered its deployment ID in each branch,
// and one on the deployment form that predates {slug} is offered a few
// words of each task's title after its number.
export function taskBranchSuggestion(settings) {
  const current = settings.taskBranchTemplate || "";
  const target = settings.taskBranchTemplateDefault;
  if (!target || current === target) return null;
  if (!current) {
    return `New tasks here go on grain/task-{id}, which any other grain on the same repository uses too. Switch to ${target} to put this deployment's ID in each new branch; existing tasks keep theirs.`;
  }
  if (current === unsluggedTaskBranchTemplate && target.includes("{slug}")) {
    return `New tasks here go on branches named by their number alone. Switch to ${target} to add a few words of each task's title after it, so a branch and its pull request say what they are about; existing tasks keep theirs.`;
  }
  return null;
}

// AgentsSection is Settings → Agents (docs/design/settings-panel.md
// §3.2). `s` is the page's useSettingsSection().
export default function AgentsSection({ s }) {
  const { settings, saveChoice, onTextBlur, onIntBlur, onEnterKey } = s;
  return (
    <>
      <SettingsCard title="Agent runs" anchor="agent-runs">
        <TextField
          key={`maxTurns-${settings.maxAgentTurns || 0}`}
          name="maxAgentTurns"
          data-setting-id="maxAgentTurns"
          label="Max agent turns"
          helperText="0 = uncapped; runs are bounded by wall-clock runtime instead"
          type="number"
          inputProps={{ min: 0, step: 1 }}
          defaultValue={String(settings.maxAgentTurns || 0)}
          onBlur={onIntBlur("maxAgentTurns", { emptyVal: 0 })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
      </SettingsCard>
      <SettingsCard
        title="Standing instructions"
        anchor="standing-instructions"
      >
        <MultilineSetting
          s={s}
          card="Standing instructions"
          name="promptExtension"
          data-setting-id="promptExtension"
          label="Standing instructions for every run"
          helperText="Added to the end of every run's prompt on this deployment -- house style, a test command, a convention grain has no way to know. A repo can add its own on the repository's page (Repos -> <repo> -> Agents), and a task can replace both (New task -> Advanced options). Leave empty to add nothing."
        />

        <Typography variant="body2" color="text.secondary">
          Every run is also given what grain has learned about working here --
          the memory agent&apos;s own notes, which follow the instructions above
          in the same prompt. That is written by an agent rather than by you, so
          it lives in Settings &rarr; Memory and not in this box.
        </Typography>
      </SettingsCard>
      <SettingsCard title="Task branches" anchor="task-branches">
        {taskBranchSuggestion(settings) && (
          <Alert
            severity="info"
            sx={{ mt: 1 }}
            data-testid="task-branch-suggestion"
            action={
              <Button
                color="inherit"
                size="small"
                onClick={() =>
                  saveChoice({
                    taskBranchTemplate: settings.taskBranchTemplateDefault,
                  })
                }
              >
                Use it
              </Button>
            }
          >
            {taskBranchSuggestion(settings)}
          </Alert>
        )}
        <TextField
          key={`branchTpl-${settings.taskBranchTemplate || ""}`}
          name="taskBranchTemplate"
          data-setting-id="taskBranchTemplate"
          label="Branch name template"
          helperText="Used for new tasks; every existing task keeps the branch it was given. Include {id}; {deployment} expands to the deployment ID below, which keeps two grains pointed at the same repo off each other's branches. {slug} expands to a few words of the task's title, so a branch says what it is about; it may only end the template, straight after {id} and a hyphen (with no {deployment} or {instance} in that last part of the name), so a title can never make another task's branch: grain/{deployment}/task-{id}-{slug} (for example grain/prod/task-56-fix-the-header-parser) is what a new deployment starts with. {instance} expands to this grain installation's durable ID. Literal text can include a username, for example alice/grain/task-{id}. Leave empty for grain/task-{id}, the name deployments created before that default keep."
          placeholder="grain/task-{id}"
          defaultValue={settings.taskBranchTemplate || ""}
          onBlur={onTextBlur("taskBranchTemplate")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
        />
        <TextField
          key={`deploymentId-${settings.deploymentId || ""}`}
          name="deploymentId"
          data-setting-id="deploymentId"
          label="Deployment ID"
          helperText={`What {deployment} in the template expands to, and the middle of the GCP service accounts the gcp server creates (grain-<id>-<set>-<n>). Lowercase letters, digits and hyphens. Changing it renames the branches of tasks created afterwards only. Existing service accounts keep working under their old names, and new ones get the new ID. Nothing is renamed or deleted. Leave empty for this deployment's random ID, ${settings.deploymentIdDefault || "drawn when it was installed"}.`}
          placeholder={settings.deploymentIdDefault || ""}
          defaultValue={settings.deploymentId || ""}
          onBlur={onTextBlur("deploymentId")}
          onKeyDown={onEnterKey}
          autoComplete="off"
          fullWidth
          margin="normal"
        />

        {/* There is no deployment-wide commit identity any more
            (f386abe49): each team names its own on its Git identity
            tab, and a team that names none commits as grain's own.
            Boxes here saved into a field PUT /api/settings ignores,
            so they answered 200 and came back empty on a reload. */}
      </SettingsCard>
      <SettingsCard title="Commit identity" anchor="commit-identity">
        <Typography
          variant="body2"
          color="text.secondary"
          data-testid="commit-identity-pointer"
        >
          Agents commit as the account the git credential they push with acts as
          -- a token&apos;s owner, a GitHub App&apos;s bot account -- which
          Settings &rarr; Git shows. A credential that names nobody commits as
          grain&apos;s own, {DEFAULT_GIT_IDENTITY_NAME} &lt;
          {DEFAULT_GIT_IDENTITY_EMAIL}&gt;.
        </Typography>
      </SettingsCard>
    </>
  );
}
