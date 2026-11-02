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

import { useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  InputAdornment,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  ListSubheader,
  MenuItem,
  TextField,
  Typography,
  useMediaQuery,
} from "@mui/material";
import { DISCARD_PROMPT } from "./Overlay.jsx";
import UnbackedStateDot, { stateDot } from "./UnbackedStateDot.jsx";
import GeneralSection from "./settings/GeneralSection.jsx";
import AccessSection from "./settings/AccessSection.jsx";
import ConfigRepoSection from "./settings/ConfigRepoSection.jsx";
import ModelsSection from "./settings/ModelsSection.jsx";
import AgentsSection from "./settings/AgentsSection.jsx";
import MemorySection from "./settings/MemorySection.jsx";
import TasksSection from "./settings/TasksSection.jsx";
import RepositoriesSection from "./settings/RepositoriesSection.jsx";
import GitSection from "./settings/GitSection.jsx";
import CapabilitiesSection from "./settings/CapabilitiesSection.jsx";
import CredentialsSection from "./settings/CredentialsSection.jsx";
import EngineSection from "./settings/EngineSection.jsx";
import CISection from "./settings/CISection.jsx";
import SandboxesSection from "./settings/SandboxesSection.jsx";
import CachesSection from "./settings/CachesSection.jsx";
import PreferencesSection from "./settings/PreferencesSection.jsx";
import UpgradeSection from "./settings/UpgradeSection.jsx";
import { SettingsReadOnlyContext } from "./settings/SettingsCard.jsx";
import {
  EditInConfigRepo,
  configRepoRefusal,
  settingsSectionPath,
} from "./ConfigRefusal.jsx";
import { ConfigManagedNotice } from "./ConfigRepoLink.jsx";
import {
  CONFIG_REPO_SECTION_PATHS,
  DEFAULT_SETTINGS_SECTION,
  SETTINGS_GROUPS,
  SETTINGS_SECTIONS,
  isConfigRepoSection,
  settingsSectionLabel,
} from "../settingsSections.js";
import { matchSettings } from "../settingsIndex.js";
import { statusLine, useSettingsSection } from "../useSettingsSection.js";
import { leavingLosesEdits } from "../unsavedEdits.js";
import { settingLabel } from "../settingsPlan.js";
import { useIsPhone } from "../phone.js";

export { SETTINGS_SECTIONS, SETTINGS_GROUPS };

// Sections that hold no saved settings of their own -- this browser's
// preferences, and Upgrade, which is an action -- so they are never
// read-only under an external config repo. Upgrade has no save state
// either, so its header leaves the "All changes saved" line off.
const NOT_CONFIG = new Set(["config-repo", "preferences", "upgrade"]);
const NO_SAVE_STATE = new Set(["upgrade"]);

// Sections that read GET /api/settings, and so wait for it.
const READS_SETTINGS = new Set([
  "general",
  "access",
  "agents",
  "tasks",
  "capabilities",
  "credentials",
  "engine",
  "ci",
  "sandboxes",
  "caches",
]);

// SettingsPage is the Settings view (docs/design/settings-panel.md §3):
// every deployment setting, this browser's preferences and Memory, in
// one view beside the sidebar, opened from the gear in the top bar. A
// section nav with a filter on the left, the open section's cards on
// the right; a Section select instead of the nav on a tablet, and a
// list then a section on a phone.
//
// `section` is the open section's id ("" for the bare /settings: the
// list on a phone, Models elsewhere -- App fills that in), and
// onSection picks another. One useSettingsSection() holds the save and
// dirty state for every section, so the nav can mark a dirty one and
// leaving one asks first.
export default function SettingsPage({
  config,
  section,
  anchor = "",
  item = "",
  onItem,
  onSection,
  showError,
  dismissError,
  onSaved,
  onLogout,
  onOpenSystem,
  onOpenRepo,
  tasks = [],
  sessions = [],
  boards = [],
  onShowStrandedValues,
}) {
  const isPhone = useIsPhone();
  const narrow = useMediaQuery("(max-width: 899.95px)") && !isPhone;
  const s = {
    ...useSettingsSection({ showError, dismissError, onSaved }),
    showError,
    onSaved,
  };
  const { settings, settingsError, reloadSettings } = s;
  const [filter, setFilter] = useState("");
  const filterRef = useRef(null);
  const matches = useMemo(() => matchSettings(filter), [filter]);
  const current = section || (isPhone ? "" : DEFAULT_SETTINGS_SECTION);
  const externalConfigRepo =
    (settings ? settings.externalConfigRepo : config?.externalConfigRepo) || "";
  const externalConfigBranch =
    (settings ? settings.externalConfigBranch : config?.externalConfigBranch) ||
    "main";
  // A config-repo-only section is read-only whichever repository holds
  // the configuration (docs/design/config-repo-settings.md §2.1): what it
  // draws is what the config repo says, and changing it is a pull
  // request.
  const external = Boolean(externalConfigRepo) && !NOT_CONFIG.has(current);
  // GET /api/config's configRepoOnly: the API refuses these sections'
  // writes in favour of a config pull request when settings are locked.
  // By default (settingsLocked: false) they stay editable in the UI, and
  // so they do under the directConfigWrites opt-out, which the server
  // folds into configRepoOnly: the loaded settings carry the same answer
  // for the row they last wrote, so toggling the lock redraws at once.
  const lockedInUI =
    settings && typeof settings.configRepoOnly === "boolean"
      ? settings.configRepoOnly
      : Boolean(config?.configRepoOnly);
  const configRepoOnly = lockedInUI || Boolean(externalConfigRepo);
  const configManaged = configRepoOnly && isConfigRepoSection(current);
  const readOnly = external || configManaged;
  s.readOnly = readOnly;
  // configLocked is for the sections that mix bootstrap settings with
  // config-managed ones (Engine, Sandboxes, Legacy capabilities): the
  // API refuses a change to the config-managed fields while this is on
  // (settings-mutability.spec.md D-1, D-2), so those fields are drawn
  // read-only one by one and the bootstrap ones beside them stay
  // editable.
  s.configLocked = configRepoOnly;

  useEffect(() => {
    if (READS_SETTINGS.has(current) && !settings) reloadSettings();
  }, [current, settings, reloadSettings]);

  // "/" focuses the filter while Settings is open, unless somebody is
  // typing somewhere already.
  useEffect(() => {
    const onKey = (evt) => {
      if (evt.key !== "/" || evt.ctrlKey || evt.metaKey || evt.altKey) return;
      const tag = document.activeElement?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
      if (document.activeElement?.isContentEditable) return;
      evt.preventDefault();
      filterRef.current?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // A hash names a card or a field (/settings/ci#ci-queue): scroll it
  // into view once the section has drawn it.
  const [focusTarget, setFocusTarget] = useState(() =>
    window.location.hash
      ? decodeURIComponent(window.location.hash.slice(1))
      : "",
  );
  useEffect(() => {
    if (anchor) setFocusTarget(anchor);
  }, [anchor]);
  useEffect(() => {
    if (!focusTarget) return;
    const reveal = () => {
      const el =
        document.querySelector(`[data-setting-id="${focusTarget}"]`) ||
        document.getElementById(focusTarget);
      if (!el) return false;
      // Centre a field or a short card; a card taller than the screen
      // (Sandboxes → Kubernetes) is aligned to its top instead, since
      // centring it scrolls past its heading and the first field, which is
      // the one focused below without scrolling.
      const tall = el.getBoundingClientRect?.().height > window.innerHeight;
      el.scrollIntoView?.({ block: tall ? "start" : "center" });
      const input = el.matches?.("input,textarea,select,button")
        ? el
        : el.querySelector?.("input:not([type=hidden]),textarea,select,button");
      input?.focus?.({ preventScroll: true });
      el.classList.add("setting-highlight");
      setTimeout(() => el.classList.remove("setting-highlight"), 2000);
      setFocusTarget("");
      return true;
    };
    if (reveal()) return;
    const observer = new MutationObserver(() => {
      if (reveal()) observer.disconnect();
    });
    observer.observe(document.body, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [focusTarget, current, settings]);

  const go = (next) => {
    if (next === current) return true;
    if (
      (s.dirtyCards.length > 0 || leavingLosesEdits()) &&
      !window.confirm(DISCARD_PROMPT)
    )
      return false;
    onSection(next);
    return true;
  };

  const pick = (entry) => {
    if (!go(entry.section)) return;
    setFilter("");
    setFocusTarget(entry.card ? entry.anchor : entry.id);
    window.history.replaceState(
      window.history.state,
      "",
      `/settings/${entry.section}#${entry.card ? entry.anchor : entry.id}`,
    );
  };

  const filterBox = (
    <TextField
      size="small"
      fullWidth
      placeholder="Filter settings"
      value={filter}
      inputRef={filterRef}
      onChange={(evt) => setFilter(evt.target.value)}
      onKeyDown={(evt) => {
        if (evt.key === "Escape") setFilter("");
        if (evt.key === "Enter" && matches[0]) {
          evt.preventDefault();
          pick(matches[0]);
        }
      }}
      inputProps={{ "aria-label": "Filter settings" }}
      data-testid="settings-filter"
      InputProps={
        filter
          ? {
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton
                    size="small"
                    aria-label="Clear filter"
                    onClick={() => setFilter("")}
                  >
                    ×
                  </IconButton>
                </InputAdornment>
              ),
            }
          : undefined
      }
    />
  );

  const results = filter ? (
    matches.length === 0 ? (
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ p: 1.5 }}
        data-testid="settings-no-match"
      >
        No setting matches &apos;{filter}&apos;.
      </Typography>
    ) : (
      <List dense aria-label="Matching settings" data-testid="settings-results">
        {SETTINGS_SECTIONS.filter((sec) =>
          matches.some((m) => m.section === sec.id),
        ).map((sec) => (
          <li key={sec.id}>
            <ul style={{ padding: 0 }}>
              <ListSubheader disableSticky sx={{ lineHeight: "28px" }}>
                {sec.label}
              </ListSubheader>
              {matches
                .filter((m) => m.section === sec.id)
                .map((m) => (
                  <ListItem key={m.id} disablePadding>
                    <ListItemButton
                      onClick={() => pick(m)}
                      data-testid="settings-result"
                    >
                      <ListItemText
                        primary={`${m.label}, ${sec.label}`}
                        secondary={
                          m.configRepo && configRepoOnly
                            ? "Read-only: in the config repo"
                            : null
                        }
                      />
                    </ListItemButton>
                  </ListItem>
                ))}
            </ul>
          </li>
        ))}
      </List>
    )
  ) : null;

  const navLabel = (sec) => (
    <>
      {sec.label}
      {sec.id === "config-repo" && stateDot(config) && (
        <>
          {" "}
          <UnbackedStateDot config={config} />
        </>
      )}
      {sec.id === current && s.dirtyCards.length > 0 && (
        <Box
          component="span"
          aria-label="unsaved changes"
          data-testid="settings-dirty-dot"
          sx={{
            display: "inline-block",
            width: 8,
            height: 8,
            ml: 1,
            borderRadius: "50%",
            bgcolor: "warning.main",
          }}
        />
      )}
    </>
  );

  const nav = (
    <Box
      component="nav"
      aria-label="Settings sections"
      data-testid="settings-nav"
      className="settings-nav"
      sx={
        isPhone
          ? { p: 1.5 }
          : {
              width: 232,
              flexShrink: 0,
              borderRight: 1,
              borderColor: "divider",
              p: 1.5,
              overflowY: "auto",
            }
      }
    >
      <Typography variant="h6" component="h2" sx={{ mb: 1 }}>
        Settings
      </Typography>
      {filterBox}
      {results ||
        SETTINGS_GROUPS.map((group) => (
          <List
            key={group.id}
            dense
            subheader={
              <ListSubheader disableSticky sx={{ lineHeight: "28px", mt: 1 }}>
                {group.label}
              </ListSubheader>
            }
          >
            {/* Each button in a list item of its own: a <ul> holding
                role="button" children directly is not a list to a
                screen reader (axe, 2026-10-26). */}
            {group.sections.map((sec) => (
              <ListItem key={sec.id} disablePadding>
                <ListItemButton
                  selected={sec.id === current}
                  aria-current={sec.id === current ? "page" : undefined}
                  onClick={() => go(sec.id)}
                  data-testid={`settings-nav-${sec.id}`}
                  sx={isPhone ? { minHeight: 40 } : undefined}
                >
                  <ListItemText primary={navLabel(sec)} />
                  {isPhone && <span aria-hidden="true">›</span>}
                </ListItemButton>
              </ListItem>
            ))}
          </List>
        ))}
    </Box>
  );

  const pending = (settings?.pendingRestart || []).map(settingLabel);
  const restartChip =
    READS_SETTINGS.has(current) && pending.length > 0 ? (
      <Chip
        size="small"
        color="warning"
        label="Restart needed"
        onClick={() => onOpenSystem?.("power")}
        data-testid="settings-restart-chip"
      />
    ) : null;

  const header = (
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        gap: 1.5,
        flexWrap: "wrap",
        mb: 2,
      }}
    >
      {isPhone && (
        <IconButton
          aria-label="All settings"
          onClick={() => go("")}
          data-testid="settings-back"
          edge="start"
        >
          ←
        </IconButton>
      )}
      <Typography variant="h5" component="h1">
        {settingsSectionLabel(current)}
      </Typography>
      {!NO_SAVE_STATE.has(current) && (
        <Typography
          variant="body2"
          color={s.status === "error" ? "error" : "text.secondary"}
          data-testid="settings-status"
          role="status"
        >
          {statusLine(s)}
        </Typography>
      )}
      {!NO_SAVE_STATE.has(current) &&
        s.status === "error" &&
        s.error?.retry && (
          <Button size="small" onClick={s.error.retry}>
            Retry
          </Button>
        )}
      <Box sx={{ flex: 1 }} />
      {restartChip}
    </Box>
  );

  const sectionSelect = narrow && (
    <>
      <Typography variant="h6" component="h2" sx={{ mb: 1 }}>
        Settings
      </Typography>
      <Box sx={{ display: "flex", gap: 1, mb: 2, alignItems: "flex-start" }}>
        <TextField
          select
          size="small"
          label="Section"
          value={current}
          onChange={(evt) => go(evt.target.value)}
          sx={{ minWidth: 200 }}
          SelectProps={{ "data-testid": "settings-section-select" }}
        >
          {SETTINGS_GROUPS.flatMap((group) => [
            <ListSubheader key={`g-${group.id}`}>{group.label}</ListSubheader>,
            ...group.sections.map((sec) => (
              <MenuItem key={sec.id} value={sec.id}>
                {sec.label}
              </MenuItem>
            )),
          ])}
        </TextField>
        <Box sx={{ flex: 1, position: "relative" }}>
          {filterBox}
          {results && (
            <Box
              sx={{
                position: "absolute",
                zIndex: 5,
                left: 0,
                right: 0,
                bgcolor: "background.paper",
                boxShadow: 3,
                borderRadius: 1,
              }}
            >
              {results}
            </Box>
          )}
        </Box>
      </Box>
    </>
  );

  const needsSettings = READS_SETTINGS.has(current);
  let body = null;
  if (needsSettings && !settings) {
    body = settingsError ? (
      <Alert
        severity="error"
        action={
          <Button color="inherit" size="small" onClick={() => reloadSettings()}>
            Try again
          </Button>
        }
      >
        Deployment settings could not be read ({settingsError}).
      </Alert>
    ) : null;
  } else {
    const common = { config, showError };
    switch (current) {
      case "general":
        body = <GeneralSection s={s} config={config} />;
        break;
      case "access":
        body = <AccessSection s={s} {...common} onLogout={onLogout} />;
        break;
      case "config-repo":
        body = (
          <ConfigRepoSection
            {...common}
            item={item}
            onItem={onItem}
            onStatusChange={() => {
              reloadSettings();
              onSaved?.();
            }}
          />
        );
        break;
      case "models":
        body = <ModelsSection {...common} onSaved={onSaved} />;
        break;
      case "git":
        body = (
          <GitSection showError={showError} onSaved={onSaved} onSection={go} />
        );
        break;
      case "tasks":
        body = (
          <TasksSection
            s={s}
            config={config}
            tasks={tasks}
            onShowStrandedValues={onShowStrandedValues}
          />
        );
        break;
      case "agents":
        body = <AgentsSection s={s} />;
        break;
      case "memory":
        body = <MemorySection {...common} s={s} tasks={tasks} />;
        break;
      case "repositories":
        body = (
          <RepositoriesSection
            config={config}
            tasks={tasks}
            onOpenRepo={onOpenRepo}
            onSection={go}
          />
        );
        break;
      case "capabilities":
        body = <CapabilitiesSection {...common} item={item} onItem={onItem} />;
        break;
      case "credentials":
        body = (
          <CredentialsSection
            s={s}
            claimedSecrets={[
              ...(settings?.agentBackends || []).map(
                (backend) => `agent-backend-${backend.id}`,
              ),
              ...(settings?.capabilities || []).flatMap((c) =>
                (c.secrets || []).map((sec) => sec.secret),
              ),
            ]}
          />
        );
        break;
      case "engine":
        body = <EngineSection s={s} />;
        break;
      case "ci":
        body = <CISection s={s} />;
        break;
      case "sandboxes":
        body = <SandboxesSection s={s} />;
        break;
      case "caches":
        body = <CachesSection s={s} />;
        break;
      case "upgrade":
        body = (
          <UpgradeSection
            {...common}
            onOpenUpdates={() => {
              if (!go("general")) return;
              setFocusTarget("updates");
            }}
          />
        );
        break;
      case "preferences":
        body = (
          <PreferencesSection
            sessions={sessions}
            boards={boards}
            deploymentTimeZone={config?.timeZone || ""}
          />
        );
        break;
      default:
        body = null;
    }
  }

  const content = (
    <Box
      className="settings-content"
      data-testid="settings-section"
      data-section={current}
      sx={{ flex: 1, minWidth: 0, p: { xs: 1.5, sm: 3 }, overflowY: "auto" }}
    >
      <Box sx={{ maxWidth: 760 }}>
        {sectionSelect}
        {header}
        {needsSettings && settings && settingsError && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            Deployment settings could not be read again ({settingsError}), so
            the values below are from the last read that succeeded and may be
            stale.
          </Alert>
        )}
        {needsSettings && settings && !settings.configured && (
          <Alert severity="info" sx={{ mb: 2 }}>
            Not configured yet -- nothing has been saved for this deployment.
          </Alert>
        )}
        {configManaged && !external && (
          <ConfigManagedNotice paths={CONFIG_REPO_SECTION_PATHS[current]} />
        )}
        {external && (
          <Alert
            severity="info"
            sx={{ mb: 2 }}
            data-testid="external-config-repo-banner"
          >
            {`Configuration is read-only and managed in ${externalConfigRepo} (${externalConfigBranch}). Switch to the internal config repository in Config Repo settings to edit settings in the UI.`}
            <Box sx={{ mt: 1 }}>
              <EditInConfigRepo
                refusal={configRepoRefusal(
                  externalConfigRepo,
                  externalConfigBranch,
                )}
                paths={[settingsSectionPath(current)]}
              />
            </Box>
          </Alert>
        )}
        <SettingsReadOnlyContext.Provider value={readOnly}>
          {body}
        </SettingsReadOnlyContext.Provider>
      </Box>
    </Box>
  );

  return (
    // role="main": this view is the shell's content, as every other
    // view's is.
    <div className="main-column" role="main" data-testid="settings-page">
      {/* content keeps the same slot in every layout, so crossing the
          phone or narrow breakpoint re-lays the open section out rather
          than remounting it and dropping its state (a selected tab, an
          open form). */}
      <Box sx={{ display: "flex", height: "100%", minHeight: 0 }}>
        {!isPhone && !narrow && nav}
        {isPhone && !current ? nav : content}
      </Box>
    </div>
  );
}
