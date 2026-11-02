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
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControl,
  FormControlLabel,
  FormHelperText,
  InputLabel,
  Link,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { isApiGrant, withoutApiRows } from "../../apiGrants.js";
import { isCapabilityRef } from "../../capabilityArgs.js";
import GrantsPicker from "../GrantsPicker.jsx";
import ReadOnlyGrants from "../ReadOnlyGrants.jsx";
import { localGrantHandlers } from "../capabilityMethods.js";
import { LEGACY_CAPABILITIES_HINT, capabilityRows } from "../../state.js";
import { legacyValues } from "../../taskAttributes.js";
import SettingsCard from "./SettingsCard.jsx";

function strandedBy(field, tasks) {
  let n = 0;
  for (const t of tasks || []) {
    if (legacyValues(field, t.customFields?.[field.name]).length > 0) n += 1;
  }
  return n;
}

function StrandedFieldNote({ field, tasks, onShow }) {
  const n = strandedBy(field, tasks);
  if (n === 0) return null;
  const said =
    n === 1
      ? "1 task still carries a value this field no longer offers"
      : `${n} tasks still carry a value this field no longer offers`;
  return (
    <Typography variant="caption" color="text.secondary" component="div">
      {onShow ? (
        <Link
          component="button"
          type="button"
          variant="caption"
          underline="hover"
          onClick={() => onShow(field.name)}
        >
          {said}
        </Link>
      ) : (
        said
      )}
    </Typography>
  );
}

function TaskFieldsCard({ config, tasks, onShowStrandedValues }) {
  const fields = (config && config.customFields) || [];
  return (
    // Nothing here is editable -- the fields are declared in the config
    // repo -- and the stranded-value counts are links into the task list,
    // so the card is never drawn inside a read-only fieldset.
    <SettingsCard title="Task fields" anchor="task-fields" allowInExternalMode>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Fields this deployment adds to its own tasks -- filled in on the
        new-task form, shown on each task, and handed to every run in its
        prompt. One marked “on rows” is shown beside the title in every list and
        on every card as well. They are declared in the configuration
        repository, in <code>tables/custom_field.json</code>: file a task asking
        for the field you want and the change arrives as a pull request to
        review.
      </Typography>
      {fields.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          No task fields are declared here.
        </Typography>
      ) : (
        <Stack spacing={0.5}>
          {fields.map((f) => (
            <Box key={f.name}>
              <Typography variant="body2">
                <strong>{f.label || f.name}</strong>{" "}
                <span className="hint">
                  {f.name} · {f.type}
                  {f.multiple ? " (several)" : ""}
                  {f.type === "enum" && (f.options || []).length > 0
                    ? `: ${f.options.join(", ")}`
                    : ""}
                  {f.default ? ` · default ${f.default}` : ""}
                  {f.showOnRow ? " · on rows" : ""}
                </span>
              </Typography>
              {f.description && (
                <Typography variant="caption" color="text.secondary">
                  {f.description}
                </Typography>
              )}
              <StrandedFieldNote
                field={f}
                tasks={tasks}
                onShow={onShowStrandedValues}
              />
            </Box>
          ))}
        </Stack>
      )}
    </SettingsCard>
  );
}

// TasksSection is Settings → Tasks (docs/design/settings-panel.md §3.2):
// the defaults a new task is filed with, the backlog order and follow-up
// limits, and the task fields the deployment declares. `s` is the page's
// useSettingsSection(); every field here is an /api/settings key.
export default function TasksSection({
  s,
  config,
  tasks = [],
  onShowStrandedValues,
}) {
  const { settings, saveChoice, onIntBlur, onEnterKey } = s;
  // Read-only when the config repo holds these settings: the default
  // grants are then settings/capabilities.yml's, listed rather than
  // edited (docs/design/config-repo-settings.md §7.4).
  const readOnly = Boolean(s.readOnly);
  const defaultCapabilities = settings.defaultCapabilities || [];
  // The defaults are one list holding all three kinds a task's own
  // capabilities do: the legacy in-process ids, edited with the
  // checklist, and the agent API's grants and capability server methods
  // (with their with: arguments), edited with GrantsPicker -- the same
  // split the new-task form, the template and the schedule editors make.
  const legacyDefaults = defaultCapabilities.filter(
    (id) => !isCapabilityRef(id) && !isApiGrant(id),
  );
  const apiDefaults = defaultCapabilities.filter(isApiGrant);
  const refDefaults = defaultCapabilities.filter(isCapabilityRef);
  const capabilityChoices = withoutApiRows(
    capabilityRows(
      (settings.capabilities || []).filter((c) => c.grantable !== false),
      legacyDefaults,
    ),
  );
  const methodDefaults = localGrantHandlers(refDefaults, (refs) =>
    saveChoice({
      defaultCapabilities: [...legacyDefaults, ...apiDefaults, ...refs],
    }),
  );

  return (
    <>
      <SettingsCard title="Task defaults" anchor="task-defaults">
        <FormControlLabel
          control={
            <Checkbox
              name="approvedByDefault"
              data-setting-id="approvedByDefault"
              checked={!!settings.approvedByDefault}
              onChange={(evt) =>
                saveChoice({ approvedByDefault: evt.target.checked })
              }
            />
          }
          label={
            <>
              Queue new tasks immediately by default
              <span className="hint">
                on (default): a new task&apos;s own &quot;Queue
                immediately&quot; checkbox starts checked, filing a task ready
                to dispatch at once. off: it starts unchecked instead, filing it
                as a proposal needing approval.
              </span>
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />

        <FormControlLabel
          control={
            <Checkbox
              name="autoMergeByDefault"
              data-setting-id="autoMergeByDefault"
              checked={!!settings.autoMergeByDefault}
              onChange={(evt) =>
                saveChoice({ autoMergeByDefault: evt.target.checked })
              }
            />
          }
          label={
            <>
              Auto-merge new tasks by default
              <span className="hint">
                on (default): a new task&apos;s own &quot;Auto-merge once checks
                pass&quot; checkbox starts checked. off: it starts unchecked
                instead.
              </span>
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />

        <FormControl
          fullWidth
          margin="normal"
          size="small"
          data-setting-id="defaultCapabilities"
        >
          <InputLabel id="settings-default-capabilities-label">
            Default legacy capabilities
          </InputLabel>
          <Select
            labelId="settings-default-capabilities-label"
            label="Default legacy capabilities"
            multiple
            value={legacyDefaults}
            onChange={(e) => {
              const next = e.target.value;
              saveChoice({
                defaultCapabilities: [...next, ...apiDefaults, ...refDefaults],
              });
            }}
            renderValue={(selected) => (
              <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5 }}>
                {selected.map((id) => {
                  const cap = capabilityChoices.find((c) => c.id === id);
                  return (
                    <Chip
                      key={id}
                      size="small"
                      label={cap ? cap.name || cap.id : id}
                    />
                  );
                })}
              </Box>
            )}
          >
            {capabilityChoices.length === 0 && (
              <MenuItem disabled>
                None configured &mdash; the rows under Settings → Legacy
                capabilities are where a capability is set up
              </MenuItem>
            )}
            {capabilityChoices.map((c) => (
              <MenuItem key={c.id} value={c.id} title={c.description}>
                <Checkbox
                  checked={defaultCapabilities.includes(c.id)}
                  size="small"
                />
                <ListItemText
                  primary={c.name || c.id}
                  secondary={
                    c.retired
                      ? c.description
                      : c.buildDefault
                        ? "on by default for this build"
                        : null
                  }
                />
              </MenuItem>
            ))}
          </Select>
          <FormHelperText>
            attached to every new task as it is filed, whichever repo it targets
            -- whoever files one can untick any of these on the new-task form,
            and any of them can be detached from a task afterwards. Tasks
            already filed keep what they were filed with. An individual repo can
            add more of its own, on the repos page. {LEGACY_CAPABILITIES_HINT}
          </FormHelperText>
        </FormControl>
        <Box data-testid="default-grants" sx={{ mt: 1 }}>
          <Typography variant="subtitle2" component="h3" sx={{ mb: 0.5 }}>
            Default grants
          </Typography>
          {readOnly ? (
            <ReadOnlyGrants
              config={config}
              grants={[...apiDefaults, ...refDefaults]}
              empty="None: a new task starts with only the grants whoever files it adds."
              testId="default-grants-list"
              showLink={false}
            />
          ) : (
            <GrantsPicker
              config={config}
              grants={defaultCapabilities}
              onApiChange={(next) =>
                saveChoice({
                  defaultCapabilities: [
                    ...legacyDefaults,
                    ...next,
                    ...refDefaults,
                  ],
                })
              }
              onSubmit={methodDefaults.onSubmit}
              onRemove={methodDefaults.onRemove}
              idPrefix="settings-default"
            />
          )}
          <FormHelperText>
            Agent API grants and capability server methods, with their
            arguments, every new task is filed with -- shown on the new-task
            form as sections of its own, to be edited or removed there.
          </FormHelperText>
        </Box>
        {/* A capability this build turns on by default
              (ui.CapabilityStatus.BuildDefault) is seeded only into a
              fresh install's defaults: grain never adds one to a stored
              configuration, so a deployment that predates the build,
              or whose operator took it out, is told here and offered
              the one click instead. */}
        {capabilityChoices
          .filter((c) => c.buildDefault && !defaultCapabilities.includes(c.id))
          .map((c) => (
            <Alert
              key={`build-default-${c.id}`}
              severity="info"
              sx={{ mt: 1 }}
              action={
                readOnly ? null : (
                  <Button
                    color="inherit"
                    size="small"
                    onClick={() => {
                      const next = [...defaultCapabilities, c.id];
                      saveChoice({ defaultCapabilities: next });
                    }}
                  >
                    Add to defaults
                  </Button>
                )
              }
            >
              {c.name || c.id} is on by default for this build, and is not in
              this deployment&apos;s defaults -- either it was installed before
              this build, or somebody took it out.
            </Alert>
          ))}
      </SettingsCard>
      <SettingsCard title="Backlog & follow-up limits" anchor="backlog">
        <FormControlLabel
          control={
            <Checkbox
              name="newestFirst"
              data-setting-id="newestFirst"
              checked={!!settings.newestFirst}
              onChange={(evt) =>
                saveChoice({ newestFirst: evt.target.checked })
              }
            />
          }
          label={
            <>
              Work through the backlog newest-first
              <span className="hint">
                the list runs top-to-bottom in the order tasks will be
                dispatched, whatever this says. off (default): a new task joins
                the end of it, dispatched last, behind everything already
                queued. on: it joins the top instead and is dispatched next,
                ahead of everything queued.
              </span>
            </>
          }
          sx={{ display: "flex", mt: 1 }}
        />

        {/* A deployment-wide "grain CI is the gate everywhere" box sat
          here until the 2026-10-23 settings walkthrough found it saved
          nothing: the server dropped Config.IgnoreGitHubCI in favour of
          each repository's own CI mode (a5c329900), and PUT
          /api/settings ignores a field it does not know, so the box
          reported "All changes saved" and read back unchecked. */}
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ mt: 1 }}
          data-testid="ci-gate-per-repo"
        >
          Whether GitHub&rsquo;s check runs count towards the merge gate is each
          repository&rsquo;s own CI mode, set under Settings &rarr; Repositories
          or on the repository&rsquo;s page.
        </Typography>

        <TextField
          key={`maxDepth-${settings.maxAutoSubmitDepth || 2}`}
          name="maxAutoSubmitDepth"
          data-setting-id="maxAutoSubmitDepth"
          label="Max unreviewed follow-up depth"
          helperText="how many levels of self-approved follow-up work an auto-merge task may queue below the last person who approved anything -- past it, a proposal is still filed, waiting for a human and saying so"
          type="number"
          inputProps={{ min: 1, step: 1 }}
          defaultValue={String(settings.maxAutoSubmitDepth || 2)}
          onBlur={onIntBlur("maxAutoSubmitDepth", {
            defaultVal: 2,
            emptyVal: 2,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />
        <TextField
          key={`maxFanOut-${settings.maxAutoSubmitFanOut || 5}`}
          name="maxAutoSubmitFanOut"
          data-setting-id="maxAutoSubmitFanOut"
          label="Max unreviewed follow-up per task"
          helperText="how many proposals one task may have queued that way in total; the next waits for a human"
          type="number"
          inputProps={{ min: 1, step: 1 }}
          defaultValue={String(settings.maxAutoSubmitFanOut || 5)}
          onBlur={onIntBlur("maxAutoSubmitFanOut", {
            defaultVal: 5,
            emptyVal: 5,
          })}
          onKeyDown={onEnterKey}
          fullWidth
          margin="normal"
        />

        <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
          Target repos are managed from the Repos pane now, not here.
        </Typography>
      </SettingsCard>
      <TaskFieldsCard
        config={config}
        tasks={tasks}
        onShowStrandedValues={onShowStrandedValues}
      />
    </>
  );
}
