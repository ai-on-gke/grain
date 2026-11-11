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
  TextField,
  Typography,
  useMediaQuery,
} from "@mui/material";
import AppIcon from "./AppIcon.jsx";
import SearchSelect from "./SearchSelect.jsx";
import { DISCARD_PROMPT } from "./Overlay.jsx";
import { keepInView } from "../keepInView.js";
import UnbackedStateDot, { stateDot } from "./UnbackedStateDot.jsx";
import GeneralSection from "./settings/GeneralSection.jsx";
import AccessSection from "./settings/AccessSection.jsx";
import HostdsSection from "./settings/HostdsSection.jsx";
import AuditSection from "./settings/AuditSection.jsx";
import CLISection from "./settings/CLISection.jsx";
import ConfigRepoSection from "./settings/ConfigRepoSection.jsx";
import ModelsSection from "./settings/ModelsSection.jsx";
import AgentsSection from "./settings/AgentsSection.jsx";
import MemorySection from "./settings/MemorySection.jsx";
import TasksSection from "./settings/TasksSection.jsx";
import RepositoriesSection from "./settings/RepositoriesSection.jsx";
import GitSection from "./settings/GitSection.jsx";
import CapabilitiesSection from "./settings/CapabilitiesSection.jsx";
import EngineSection from "./settings/EngineSection.jsx";
import CISection from "./settings/CISection.jsx";
import SandboxesSection from "./settings/SandboxesSection.jsx";
import CachesSection from "./settings/CachesSection.jsx";
import PreferencesSection from "./settings/PreferencesSection.jsx";
import UpgradeSection from "./settings/UpgradeSection.jsx";
import HubSection from "./settings/HubSection.jsx";
import {
  SettingsReadOnlyContext,
  SettingsRoleRefusalContext,
} from "./settings/SettingsCard.jsx";
import { accessDeniedReason } from "../access.js";
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
import { SETTINGS_INDEX, matchSettings } from "../settingsIndex.js";
import { statusLine, useSettingsSection } from "../useSettingsSection.js";
import { leavingLosesEdits } from "../unsavedEdits.js";
import { settingLabel } from "../settingsPlan.js";
import { useIsPhone } from "../phone.js";

export { SETTINGS_GROUPS };

// Sections that hold no saved settings of their own -- this browser's
// preferences, Access's sign-in (design/design/config-repo-settings.design.md
// §1 invariant 7: it stays outside the config repo so a bad config PR can
// never lock the operator out), Upgrade, which is an action, and Audit,
// which only reads logs, and CLI, which only shows commands -- so they are
// never read-only under an external config repo. Upgrade, Audit and CLI
// have no save state either, so their headers leave the "All changes
// saved" line off.
const NOT_CONFIG = new Set([
  "config-repo",
  "preferences",
  "upgrade",
  "access",
  "hostds",
  "audit",
  "cli",
]);
const NO_SAVE_STATE = new Set(["upgrade", "audit", "cli"]);

// SECTION_WRITE_SCOPE is the scope a section's saves need
// (pkg/ui/route_scopes.go): PUT /api/settings for the settings rows,
// and the connection, repository, memory and capability routes for the
// sections that write those. A section whose scope the signed-in
// person's role lacks is drawn read-only, saying why (access.js).
// Preferences (this browser's own), Access (which gates its own
// controls), Upgrade and Audit are not listed.
const SECTION_WRITE_SCOPE = {
  general: "settings:write",
  tasks: "settings:write",
  agents: "settings:write",
  ci: "settings:write",
  engine: "settings:write",
  registry: "settings:write",
  sandboxes: "settings:write",
  caches: "settings:write",
  "config-repo": "settings:write",
  models: "connections:write",
  git: "connections:write",
  repositories: "repos:write",
  memory: "memory:write",
  capabilities: "capabilities:admin",
  hostds: "capabilities:admin",
};

// SECTION_ROLE_READ_ONLY says what a refused section's note covers,
// where that is not the whole section: Capabilities' Deployment
// credentials card follows settings:write and secrets:write instead
// (CredentialsSection), and says so itself where those are refused.
const SECTION_ROLE_READ_ONLY = {
  capabilities:
    "Capability servers, the capability policy and API scope sets are read-only for you.",
};

// Sections that read GET /api/settings, and so wait for it.
const READS_SETTINGS = new Set([
  "general",
  "cli",
  "agents",
  "tasks",
  "capabilities",
  "engine",
  "registry",
  "ci",
  "sandboxes",
  "caches",
]);

// SettingsPage is the Settings view (design/design/settings-panel.design.md §3):
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
  // GET /api/whoami's answer, for Settings -> Access (App.jsx).
  whoami = null,
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
  const matches = useMemo(() => {
    const raw = matchSettings(filter);
    if (!config?.selfUpgradeDisabled) return raw;
    return raw.filter(
      (entry) =>
        !(
          !entry.card &&
          entry.section === "general" &&
          entry.anchor === "updates"
        ),
    );
  }, [filter, config?.selfUpgradeDisabled]);
  const current = section || (isPhone ? "" : DEFAULT_SETTINGS_SECTION);
  const externalConfigRepo =
    (settings ? settings.externalConfigRepo : config?.externalConfigRepo) || "";
  const externalConfigBranch =
    (settings ? settings.externalConfigBranch : config?.externalConfigBranch) ||
    "main";
  // A config-repo-only section is read-only whichever repository holds
  // the configuration (design/design/config-repo-settings.design.md §2.1): what it
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
  const roleRefusal = SECTION_WRITE_SCOPE[current]
    ? accessDeniedReason(config, SECTION_WRITE_SCOPE[current])
    : "";
  const readOnly = external || configManaged || Boolean(roleRefusal);
  s.readOnly = readOnly;
  // What is read-only for everybody, whatever their role: the
  // Deployment credentials card follows its own write scopes rather than
  // Capabilities' capabilities:admin (below), so it reads this instead.
  s.readOnlyForAnyRole = external || configManaged;
  // configLocked is for the sections that mix bootstrap settings with
  // config-managed ones (Engine, Sandboxes, Capabilities' Deployment credentials): the
  // API refuses a change to the config-managed fields while this is on
  // (settings-mutability.spec.md D-1, D-2), so those fields are drawn
  // read-only one by one and the bootstrap ones beside them stay
  // editable.
  s.configLocked = configRepoOnly;
  // Capabilities' Deployment credentials card writes through routes of
  // its own instead of the section's capabilities:admin -- PUT
  // /api/settings and /api/secrets (pkg/ui/route_scopes.go) -- so it is
  // gated on what the role refuses there (CredentialsSection), and is
  // writable to a role holding those scopes without capabilities:admin.
  s.settingsWriteRefusal = accessDeniedReason(config, "settings:write");
  s.secretsWriteRefusal = accessDeniedReason(config, "secrets:write");

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
  // What still holds the last revealed card in view while the section
  // around it draws (keepInView); revealing another, or leaving, lets go.
  const settling = useRef(null);
  useEffect(() => () => settling.current?.(), []);
  useEffect(() => {
    if (!focusTarget) return;
    const reveal = () => {
      // A setting hidden while its parent mode is off (Automatic upgrade
      // time of day when Automatic upgrades is off; the fixed or auto
      // overcommit bounds of the inactive Overcommit mode) has no input in
      // the DOM once its card has drawn: reveal that card instead.
      const cardAnchor = SETTINGS_INDEX.find(
        (e) => e.id === focusTarget && e.section === current && !e.card,
      )?.anchor;
      const el =
        document.querySelector(`[data-setting-id="${focusTarget}"]`) ||
        document.getElementById(focusTarget) ||
        (cardAnchor ? document.getElementById(cardAnchor) : null);
      if (!el) return false;
      // Centre a field or a short card; a card taller than the screen
      // (Sandboxes → Kubernetes) is aligned to its top instead, since
      // centring it scrolls past its heading and the first field, which is
      // the one focused below without scrolling.
      const tall = el.getBoundingClientRect?.().height > window.innerHeight;
      const scrollOptions = { block: tall ? "start" : "center" };
      el.scrollIntoView?.(scrollOptions);
      settling.current?.();
      settling.current = keepInView(el, scrollOptions);
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

  // go opens section next, asking first when an edit would be lost. A
  // nextAnchor (a card or field id) rides along as the address's #hash
  // (GitSection's note opens Capabilities at its Deployment credentials
  // card; CLI's Sign in opens Agents at External agents) and is scrolled
  // to once it is drawn.
  const go = (next, nextAnchor = "") => {
    if (next !== current || Boolean(item)) {
      if (
        (s.dirtyCards.length > 0 || leavingLosesEdits()) &&
        !window.confirm(DISCARD_PROMPT)
      )
        return false;
      if (nextAnchor) onSection(next, nextAnchor);
      else onSection(next);
    }
    if (nextAnchor) setFocusTarget(nextAnchor);
    return true;
  };

  const pick = (entry) => {
    const target = entry.card ? entry.anchor : entry.id;
    const sameSection = entry.section === current && !item;
    if (!go(entry.section, target)) return;
    setFilter("");
    if (sameSection) {
      window.history.replaceState(
        window.history.state,
        "",
        `/settings/${entry.section}#${target}`,
      );
    }
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
      InputProps={{
        startAdornment: (
          <InputAdornment position="start" sx={{ color: "text.secondary" }}>
            <AppIcon name="search" size={16} />
          </InputAdornment>
        ),
        ...(filter
          ? {
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton
                    size="small"
                    aria-label="Clear filter"
                    onClick={() => setFilter("")}
                  >
                    <AppIcon name="close" size={16} />
                  </IconButton>
                </InputAdornment>
              ),
            }
          : {}),
      }}
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
                  {isPhone && <AppIcon name="chevronRight" size={18} />}
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
          <AppIcon name="back" size={18} />
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
        <SearchSelect
          label="Section"
          fullWidth={false}
          value={current}
          onChange={(next) => {
            if (next) go(next);
          }}
          sx={{ minWidth: 200 }}
          data-testid="settings-section-select"
          groupBy={(o) => o.group}
          options={SETTINGS_GROUPS.flatMap((group) =>
            group.sections.map((sec) => ({
              value: sec.id,
              label: sec.label,
              group: group.label,
              match: `${sec.label} ${group.label}`,
            })),
          )}
        />
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
        body = (
          <AccessSection
            s={s}
            {...common}
            whoami={whoami}
            onSaved={onSaved}
            onLogout={onLogout}
          />
        );
        break;
      case "hostds":
        body = <HostdsSection {...common} dismissError={dismissError} />;
        break;
      case "audit":
        body = <AuditSection />;
        break;
      case "cli":
        body = <CLISection s={s} config={config} onSection={go} />;
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
          <GitSection
            showError={showError}
            dismissError={dismissError}
            onSaved={onSaved}
            onSection={go}
          />
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
        body = <AgentsSection s={s} onSection={go} />;
        break;
      case "memory":
        body = <MemorySection {...common} s={s} tasks={tasks} />;
        break;
      case "repositories":
        body = (
          <RepositoriesSection
            {...common}
            tasks={tasks}
            onOpenRepo={onOpenRepo}
            onSection={go}
          />
        );
        break;
      case "capabilities":
        body = (
          <CapabilitiesSection
            {...common}
            s={s}
            item={item}
            onItem={onItem}
            // Only secrets a live control elsewhere in Settings owns:
            // CapabilitiesPanel is retired, so secrets reported on
            // settings.capabilities (github-app, registry-push, cosign-*)
            // have no per-capability field and stay visible in SecretsPanel.
            claimedSecrets={[
              "registry-registration-token",
              ...(settings?.agentBackends || []).map(
                (backend) => `agent-backend-${backend.id}`,
              ),
            ]}
          />
        );
        break;
      case "engine":
        body = <EngineSection s={s} />;
        break;
      case "registry":
        body = <HubSection s={s} />;
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
              go("general", "updates");
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
        {roleRefusal && (
          <Alert
            severity="info"
            sx={{ mb: 2 }}
            data-testid="settings-role-read-only"
          >
            {`${SECTION_ROLE_READ_ONLY[current] || "Read-only for you."} ${roleRefusal} An operator can change your role in Settings → Access.`}
          </Alert>
        )}
        <SettingsRoleRefusalContext.Provider value={roleRefusal}>
          <SettingsReadOnlyContext.Provider value={readOnly}>
            {body}
          </SettingsReadOnlyContext.Provider>
        </SettingsRoleRefusalContext.Provider>
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
