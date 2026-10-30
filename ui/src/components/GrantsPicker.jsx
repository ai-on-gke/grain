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

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  Link,
  ListItemIcon,
  ListItemText,
  ListSubheader,
  MenuItem,
  MenuList,
  Popover,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import CheckIcon from "@mui/icons-material/Check";
import CloseIcon from "@mui/icons-material/Close";
import WarningAmberIcon from "@mui/icons-material/WarningAmber";
import CapabilityArgsForm from "./CapabilityArgsForm.jsx";
import InfoPopover from "./InfoPopover.jsx";
import { capabilityMethods, checkGrants } from "./capabilityMethods.js";
import {
  DANGEROUS_API_HINT,
  apiGrantId,
  apiShortName,
  groupApiRows,
  isApiGrant,
  splitApiGrant,
} from "../apiGrants.js";
import {
  describeGrant,
  hasArguments,
  initialValue,
  isCapabilityRef,
  methodMenuLabel,
  parseGrant,
  splitRef,
  unsupportedReason,
  validateValue,
} from "../capabilityArgs.js";

// GrantsPicker is the one "Grants" control of the new task overlay, the
// task pane, the template editor and the schedule editor
// (docs/design/unified-grants.md, slice 1): every grant a task holds --
// grain API scopes and presets (api:<name>[@owner/repo], docs/agent-api.md)
// and capability server methods with their with: arguments
// (docs/capabilities.md) -- is one section in one list, and one "Add
// grant…" search lists everything that can be added, grouped by kind.
//
// A grant with nothing to set -- an api grant, a method without with:
// arguments -- is a single line. A method with arguments is a section
// with its arguments' form inline, and it saves itself the way the rest
// of the task pane does: an edit that passes the schema's own check is
// written (after a short pause in typing, or at once when focus leaves
// the section), and one that does not shows why against its field. A
// method picked from "Add grant…" whose defaults already pass is granted
// at once; one with a required argument still to fill in is a draft
// section here until it passes, and is granted then.
//
// It is a UI over the storage each kind has today, not a new model: both
// kinds live in the task's capabilities list, an api grant as its name and
// a method grant as its reference string. `grants` is that list (anything
// else in it -- the legacy in-process ids -- is ignored here, and is the
// checklist's beside it). Where a change lands is the caller's:
// onApiChange(next) with the whole new list of api grants,
// onSubmit(spec, replacing) with spec {capability, with} and replacing the
// method grant being edited (or null), and onRemove(grant) for a method
// grant. onSubmit and onRemove may reject; an error carrying `fields` (the
// server's field errors) is shown against the arguments it names.
//
// A method grant carries the warning states it always has: a method no
// attached server offers any more, arguments the server's current schema
// no longer accepts, or now normalizes differently. An api grant is
// flagged when its scope is dangerous, or when it is no longer offered.

const asList = (m) => (Array.isArray(m) ? m : []);

// SAVE_DELAY_MS is how long an argument section waits after the last
// edit before saving it, so typing a word is one write rather than one
// per keystroke.
export const SAVE_DELAY_MS = 500;

// liveErrorsOf is the schema's own check of a with: value -- none for a
// schema the form edits as raw JSON, which only the server can judge.
const liveErrorsOf = (schema, value) =>
  !schema || unsupportedReason(schema)
    ? {}
    : validateValue(schema, schema, value || {});

const RUN_KIND_LABELS = {
  task: "agent tasks",
  process: "processes",
  ci: "CI jobs",
};

function policyVerdictText(verdict, runKind) {
  if (!verdict) return "";
  const forKind = RUN_KIND_LABELS[runKind]
    ? ` for ${RUN_KIND_LABELS[runKind]}`
    : "";
  if (verdict === "denied") {
    return `denied by the capability policy${forKind}`;
  }
  if (verdict === "may_be_refused") {
    return `may be refused by the capability policy${forKind}`;
  }
  return "";
}

let nextDraft = 0;

export default function GrantsPicker({
  grants,
  config,
  onApiChange,
  onSubmit,
  onRemove,
  disabled = false,
  idPrefix = "grant",
  runKind = "task",
}) {
  const all = grants || [];
  const apiHeld = useMemo(() => all.filter(isApiGrant), [all]);
  const refs = useMemo(() => all.filter(isCapabilityRef), [all]);
  const offered = config?.capabilities || [];
  const { presets, areas } = groupApiRows(offered);
  const apiRowOf = (id) => offered.find((c) => c.id === splitApiGrant(id)[0]);
  const repos = config?.targetRepos || [];

  const [methods, setMethods] = useState(null);
  const [loadError, setLoadError] = useState("");
  const [checks, setChecks] = useState({});
  const [anchor, setAnchor] = useState(null);
  const [query, setQuery] = useState("");
  const [apiRepo, setApiRepo] = useState("");
  // drafts are argument sections not granted yet: {id, method, value,
  // server: {fields, message}}, each shown after its method's grants.
  const [drafts, setDrafts] = useState([]);
  const [error, setError] = useState("");
  const listRef = useRef(null);

  // The method listing is fetched only where there is something in it --
  // /api/config's count says so -- or a grant to show. A change in that
  // count (config is re-read as the page polls) means a server came or
  // went, so the listing is read again past its cache; so does opening
  // "Add grant…" (grain/task-1056).
  const count = config?.capabilityMethods || 0;
  const wanted = count > 0 || refs.length > 0;
  const loaded = useRef(false);
  const live = useRef(true);
  useEffect(() => {
    live.current = true;
    return () => {
      live.current = false;
    };
  }, []);
  const load = useCallback((fresh) => {
    return capabilityMethods({ fresh })
      .then((m) => {
        if (!live.current) return null;
        loaded.current = true;
        setLoadError("");
        setMethods(asList(m));
        return asList(m);
      })
      .catch((err) => {
        if (live.current) setLoadError(err.message);
        return null;
      });
  }, []);
  useEffect(() => {
    if (wanted) load(loaded.current);
  }, [wanted, count, load]);

  // A grant naming a method the listing lacks is only flagged once a
  // fresh read still lacks it: the listing may simply predate the
  // server that offers it. confirmedMissing holds the refs that did.
  const [confirmedMissing, setConfirmedMissing] = useState(() => new Set());
  const absentKey = methods
    ? refs
        .map((g) => parseGrant(g).ref)
        .filter((ref, i, list) => list.indexOf(ref) === i)
        .filter((ref) => !methods.some((m) => m.ref === ref))
        .join("\n")
    : "";
  useEffect(() => {
    if (!absentKey) return;
    load(true).then((m) => {
      if (!m || !live.current) return;
      const have = new Set(m.map((x) => x.ref));
      setConfirmedMissing(
        new Set(absentKey.split("\n").filter((ref) => !have.has(ref))),
      );
    });
  }, [absentKey, load]);

  const refsKey = refs.join("\n");
  useEffect(() => {
    if (refs.length === 0) return undefined;
    let current = true;
    checkGrants(refs.map((grant) => ({ grant })))
      .then((res) => {
        if (!current || !Array.isArray(res)) return;
        const out = {};
        res.forEach((r) => (out[r.grant] = r));
        setChecks(out);
      })
      .catch(() => {});
    return () => {
      current = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refsKey]);

  const byRef = useMemo(() => {
    const m = {};
    (methods || []).forEach((x) => (m[x.ref] = x));
    return m;
  }, [methods]);

  // Method grants grouped by method, in the order they are held, each
  // method's drafts after its grants, so "Add another" sits after the
  // last section of its method.
  const groups = useMemo(() => {
    const out = [];
    const index = {};
    const group = (ref) => {
      if (!(ref in index)) {
        index[ref] = out.length;
        out.push({ ref, grants: [], drafts: [] });
      }
      return out[index[ref]];
    };
    for (const g of refs) group(parseGrant(g).ref).grants.push(g);
    for (const d of drafts) group(d.method.ref).drafts.push(d);
    return out;
  }, [refs, drafts]);

  const dropDraft = (id) => setDrafts((ds) => ds.filter((d) => d.id !== id));
  const addDraft = (method, value, server) =>
    setDrafts((ds) => [
      ...ds,
      { id: ++nextDraft, method, value, server: server || null },
    ]);

  // addMethod is a method picked from "Add grant…" or "Add another":
  // granted at once when there is nothing to fill in, or nothing its
  // defaults leave failing the schema; a draft section otherwise.
  const addMethod = (method) => {
    setAnchor(null);
    setError("");
    if (!hasArguments(method.args)) {
      Promise.resolve(
        onSubmit({ capability: method.ref, with: undefined }, null),
      ).catch((err) => setError(err.message));
      return;
    }
    const value = initialValue(method.args, method.args) ?? {};
    if (Object.keys(liveErrorsOf(method.args, value)).length > 0) {
      addDraft(method, value);
      return;
    }
    Promise.resolve(
      onSubmit({ capability: method.ref, with: value }, null),
    ).catch((err) =>
      addDraft(method, value, {
        fields: err.fields || {},
        message: err.message,
      }),
    );
  };

  const toggleApi = (row) => {
    const id = apiGrantId(row.id, apiRepo);
    onApiChange(
      apiHeld.includes(id) ? apiHeld.filter((s) => s !== id) : [...apiHeld, id],
    );
  };

  const openMenu = (evt) => {
    setQuery("");
    setAnchor(evt.currentTarget);
    load(true);
  };

  const apiLine = (id) => {
    const [, limit] = splitApiGrant(id);
    const row = apiRowOf(id);
    const label = `api:${apiShortName(id)}${limit ? ` on ${limit}` : ""}`;
    return (
      <GrantLine
        key={id}
        grant={id}
        kind="api"
        label={label}
        ariaLabel={`${label}${row ? (row.dangerous ? " (dangerous)" : "") : " (no longer offered)"}`}
        tip={
          row
            ? row.dangerous
              ? `${DANGEROUS_API_HINT} ${row.description}`
              : row.description
            : "No longer offered -- remove it"
        }
        caption={row ? row.summary || row.description : ""}
        badge={
          !row
            ? { label: "no longer offered", color: "error" }
            : row.dangerous
              ? { label: "dangerous", color: "warning" }
              : null
        }
        onRemove={
          disabled
            ? undefined
            : () => onApiChange(apiHeld.filter((s) => s !== id))
        }
      />
    );
  };

  const policyState = (ref) => {
    const verdict = runKind ? byRef[ref]?.policy?.[runKind] : undefined;
    if (!verdict) return null;
    const policyNote = policyVerdictText(verdict, runKind);
    return {
      severity: "warning",
      badge: verdict === "denied" ? "denied by policy" : "may be refused",
      suffix: ` (${policyNote})`,
      textNote: `This grant is ${policyNote} (Settings → Capabilities).`,
      note: (
        <>
          This grant is {policyNote} (
          <Link href="/settings/capabilities#capability-policy">
            Settings → Capabilities
          </Link>
          ).
        </>
      ),
    };
  };

  // methodState is what is wrong with a held method grant, if anything.
  const methodState = (g) => {
    const { ref } = parseGrant(g);
    const check = checks[g];
    if (methods && !byRef[ref] && confirmedMissing.has(ref))
      return {
        severity: "warning",
        badge: "server not attached",
        suffix: " (server not attached)",
        note: `No attached capability server offers ${ref} any more. Remove this grant, or attach the server again.`,
      };
    if (check && check.error && !check.unchecked)
      return {
        severity: "warning",
        badge: "no longer validates",
        suffix: " (no longer validates)",
        note: `These arguments no longer validate under the server's current schema: ${check.error}`,
      };
    if (check && check.changed)
      return {
        severity: "info",
        badge: "normalizes differently",
        suffix: " (normalizes differently now)",
        note: `The server's current schema normalizes this grant differently (now ${check.recorded}). It keeps what it was granted until you edit it.`,
      };
    return policyState(ref);
  };

  const removeMethod = (g) =>
    Promise.resolve(onRemove(g)).catch((err) => setError(err.message));

  // methodSections is one method's grants and drafts. Each is keyed by
  // its place among its method's sections, so a draft that is granted
  // -- it becomes the method's last grant -- keeps its section, and the
  // edit in it, rather than being drawn anew.
  let argsIndex = 0;
  const methodSections = ({ ref, grants: gs, drafts: ds }) => {
    const method = byRef[ref] || ds[0]?.method;
    const withArgs = method && hasArguments(method.args);
    const total = gs.length + ds.length;
    const another = withArgs && !disabled ? () => addMethod(method) : undefined;
    const items = [
      ...gs.map((g) => ({ grant: g })),
      ...ds.map((d) => ({ draft: d })),
    ];
    return items.map(({ grant: g, draft }, i) => {
      const key = `${ref}#${i}`;
      const last = i === total - 1;
      if (g && !withArgs) {
        const d = describeGrant(g, { method: method || null, config });
        const label = d.args ? `${d.label} · ${d.args}` : d.label;
        const state = methodState(g);
        return (
          <GrantLine
            key={key}
            grant={g}
            kind="method"
            label={label}
            ariaLabel={`${label}${state?.suffix || ""}`}
            tip={`${state ? `${state.textNote || state.note}\n\n` : ""}${d.lines.join("\n")}`}
            info={method?.description || ""}
            badge={
              state
                ? {
                    label: state.badge,
                    color: state.severity === "info" ? "info" : "warning",
                  }
                : null
            }
            onRemove={disabled ? undefined : () => removeMethod(g)}
          />
        );
      }
      const state = g ? methodState(g) : policyState(ref);
      const d = describeGrant(g || ref, { method, config });
      return (
        <ArgsSection
          key={key}
          method={method}
          grant={g || null}
          draft={draft || null}
          label={methodMenuLabel(ref, method.method)}
          ariaLabel={`${d.args ? `${d.label} · ${d.args}` : d.label}${state?.suffix || ""}`}
          state={state}
          idPrefix={`${idPrefix}-args-${argsIndex++}`}
          disabled={disabled}
          onSave={async (value, replacing) => {
            await onSubmit({ capability: ref, with: value }, replacing);
            if (!replacing && draft) dropDraft(draft.id);
          }}
          onRemove={
            disabled
              ? undefined
              : g
                ? () => removeMethod(g)
                : () => dropDraft(draft.id)
          }
          onAddAnother={last ? another : undefined}
        />
      );
    });
  };

  // The "Add grant…" listing, narrowed by the search box: api presets,
  // then api scopes by area, then server methods.
  const q = query.trim().toLowerCase();
  const matches = (...texts) =>
    !q || texts.some((t) => (t || "").toLowerCase().includes(q));
  const apiMatches = (row) =>
    matches(row.id, row.summary, row.description, ...(row.scopes || []));
  const menuGroups = [];
  const shownPresets = presets.filter(apiMatches);
  if (shownPresets.length > 0)
    menuGroups.push({
      key: "api-presets",
      kind: "api",
      title: "Grain API presets",
      rows: shownPresets,
    });
  for (const { area, rows } of areas) {
    const shown = rows.filter(apiMatches);
    if (shown.length > 0)
      menuGroups.push({
        key: `api-${area}`,
        kind: "api",
        area,
        title: `Grain API scopes: ${area}`,
        rows: shown,
      });
  }
  const shownMethods = (methods || []).filter((m) =>
    matches(m.ref, m.method, m.description),
  );
  if (shownMethods.length > 0)
    menuGroups.push({
      key: "methods",
      kind: "method",
      title: "Capability server methods",
      rows: shownMethods,
    });

  const apiItem = (row) => {
    const name = apiShortName(row.id);
    const held = apiHeld.includes(apiGrantId(row.id, apiRepo));
    return (
      <MenuItem
        key={row.id}
        role="menuitemcheckbox"
        aria-checked={held}
        aria-label={name}
        className="api-grant-row"
        data-api-grant={row.id}
        data-api-kind={row.preset ? "preset" : "scope"}
        title={row.description}
        onClick={() => toggleApi(row)}
        sx={{ whiteSpace: "normal", alignItems: "flex-start" }}
      >
        <ListItemIcon sx={{ pt: 0.25 }}>
          {held ? <CheckIcon fontSize="small" /> : null}
        </ListItemIcon>
        <ListItemText
          disableTypography
          primary={
            <Box
              sx={{
                display: "flex",
                flexWrap: "wrap",
                alignItems: "center",
                gap: 0.5,
              }}
            >
              <Box component="code" sx={{ fontSize: "0.85rem" }}>
                {name}
              </Box>
              {row.dangerous && (
                <Chip
                  size="small"
                  color="warning"
                  variant="outlined"
                  icon={<WarningAmberIcon />}
                  label="dangerous"
                  className="api-dangerous"
                  title={DANGEROUS_API_HINT}
                  sx={{ height: 20, fontSize: "0.7rem" }}
                />
              )}
            </Box>
          }
          secondary={
            <Typography
              variant="caption"
              color="text.secondary"
              component="span"
              sx={{ display: "block" }}
            >
              {row.summary || row.description}
              {row.preset && (row.scopes || []).length > 0 && (
                <Box
                  component="span"
                  className="api-preset-scopes"
                  sx={{ display: "block" }}
                >
                  grants {row.scopes.join(", ")}
                </Box>
              )}
            </Typography>
          }
        />
      </MenuItem>
    );
  };

  const methodItem = (m) => {
    const verdict = runKind ? m.policy?.[runKind] : undefined;
    const policyNote = policyVerdictText(verdict, runKind);
    const refSummary = `${splitRef(m.ref).capability.replace(/^https:\/\//, "")}${m.requiresArgs ? " · needs arguments" : ""}`;
    return (
      <MenuItem
        key={m.ref}
        className="method-grant-row"
        data-method={m.ref}
        data-policy-verdict={verdict || undefined}
        title={policyNote ? `${m.ref} — ${policyNote}` : m.ref}
        onClick={() => addMethod(m)}
        sx={{ whiteSpace: "normal", alignItems: "flex-start" }}
      >
        <ListItemIcon sx={{ pt: 0.25 }}>
          {verdict ? (
            <WarningAmberIcon fontSize="small" color="warning" />
          ) : null}
        </ListItemIcon>
        <ListItemText
          sx={{ minWidth: 0 }}
          primary={methodMenuLabel(m.ref, m.method)}
          secondary={
            policyNote ? (
              <>
                <Box
                  component="span"
                  sx={{ display: "block", overflowWrap: "anywhere" }}
                >
                  {refSummary}
                </Box>
                <Typography
                  variant="caption"
                  color="warning.main"
                  component="span"
                  className="method-policy-verdict"
                  sx={{ display: "block", overflowWrap: "anywhere" }}
                >
                  {policyNote} (
                  <Link
                    href="/settings/capabilities#capability-policy"
                    onClick={(e) => e.stopPropagation()}
                    onKeyDown={(e) => e.stopPropagation()}
                  >
                    Settings → Capabilities
                  </Link>
                  )
                </Typography>
              </>
            ) : (
              refSummary
            )
          }
        />
      </MenuItem>
    );
  };

  const anyApi = presets.length > 0 || areas.length > 0;
  const anyMethods = (methods || []).length > 0;
  const held = apiHeld.length + refs.length + drafts.length;
  if (!anyApi && !anyMethods && held === 0 && !loadError && count === 0)
    return null;

  const regionId = `${idPrefix}-grants`;
  return (
    <Box
      data-testid={regionId}
      role="group"
      aria-labelledby={`${regionId}-label`}
      sx={{ mt: 1.5 }}
    >
      <Typography id={`${regionId}-label`} variant="subtitle2" component="div">
        Grants
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="p"
        sx={{ mb: 0.75 }}
      >
        What the agent may use: grain&apos;s own API (docs/agent-api.md) and
        capability server methods (docs/capabilities.md).
      </Typography>
      <Box
        className="grants-held"
        sx={{
          display: "flex",
          flexDirection: "column",
          gap: 1,
          minWidth: 0,
          // A line's label and caption do not wrap, they are cut; without
          // this their full width is the list's min-content width, which
          // a <fieldset> around it (the task pane's) grows to fit.
          contain: "inline-size",
        }}
      >
        {held === 0 && (
          <Typography variant="body2" color="text.secondary">
            None granted
          </Typography>
        )}
        {apiHeld.map(apiLine)}
        {groups.map((group) => (
          <Box
            key={group.ref}
            data-method={group.ref}
            sx={{ display: "contents" }}
          >
            {methodSections(group)}
          </Box>
        ))}
      </Box>
      {!disabled && (
        <Button
          size="small"
          variant="outlined"
          sx={{ mt: 0.75 }}
          onClick={openMenu}
          aria-haspopup="menu"
          aria-controls={anchor ? `${regionId}-menu` : undefined}
        >
          Add grant…
        </Button>
      )}
      <Popover
        open={Boolean(anchor)}
        anchorEl={anchor}
        onClose={() => setAnchor(null)}
        anchorOrigin={{ vertical: "bottom", horizontal: "left" }}
        slotProps={{
          paper: {
            sx: {
              width: "min(440px, calc(100vw - 32px))",
              maxHeight: "min(520px, calc(100vh - 32px))",
              display: "flex",
              flexDirection: "column",
            },
          },
        }}
      >
        <Box sx={{ p: 1, pb: 0.5 }}>
          <TextField
            autoFocus
            fullWidth
            size="small"
            placeholder="Search grants"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            inputProps={{ "aria-label": "Search grants" }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                listRef.current
                  ?.querySelector('[role^="menuitem"]:not([aria-disabled])')
                  ?.focus();
              }
            }}
          />
          {anyApi && repos.length > 0 && (
            <TextField
              select
              size="small"
              margin="dense"
              fullWidth
              id={`${regionId}-repo`}
              label="Grain API grants apply to"
              value={apiRepo}
              onChange={(e) => setApiRepo(e.target.value)}
              helperText={
                apiRepo
                  ? `An API grant picked here is limited to ${apiRepo} (api:<scope>@${apiRepo}).`
                  : undefined
              }
            >
              <MenuItem value="">All repositories</MenuItem>
              {repos.map((r) => (
                <MenuItem key={r} value={r}>
                  {r}
                </MenuItem>
              ))}
            </TextField>
          )}
        </Box>
        <MenuList
          ref={listRef}
          id={`${regionId}-menu`}
          dense
          aria-label="Grants to add"
          autoFocusItem={false}
          sx={{ overflowY: "auto", pt: 0 }}
        >
          {menuGroups.flatMap((g) => [
            <ListSubheader
              key={g.key}
              className={
                g.kind === "api" && g.area ? "api-scope-area" : undefined
              }
              data-grant-group={g.key}
              sx={{ lineHeight: "32px" }}
            >
              {g.title}
            </ListSubheader>,
            ...g.rows.map(g.kind === "api" ? apiItem : methodItem),
          ])}
          {menuGroups.length === 0 && (
            <MenuItem disabled>
              {q
                ? "Nothing matches"
                : loadError
                  ? "Capability server methods could not be read"
                  : "Nothing to grant"}
            </MenuItem>
          )}
        </MenuList>
      </Popover>
      {(error || loadError) && (
        <Alert
          severity="error"
          sx={{ mt: 1, py: 0 }}
          onClose={() => setError("")}
        >
          {error || `Capability server methods could not be read: ${loadError}`}
        </Alert>
      )}
    </Box>
  );
}

const sectionSx = {
  border: 1,
  borderColor: "divider",
  borderRadius: 1,
  minWidth: 0,
};

// GrantLine is a grant with nothing to set, on one line: its label (the
// whole grant in its tooltip), a caption, a warning badge, and remove.
function GrantLine({
  grant,
  kind,
  label,
  ariaLabel,
  tip,
  caption,
  info,
  badge,
  onRemove,
}) {
  return (
    <Box
      role="group"
      data-grant={grant}
      data-grant-kind={kind}
      aria-label={ariaLabel}
      className="grant-line"
      sx={{
        ...sectionSx,
        display: "flex",
        alignItems: "center",
        gap: 1,
        pl: 1,
        pr: onRemove ? 0.25 : 1,
        py: 0.25,
        minHeight: 32,
      }}
    >
      <Tooltip
        title={
          <Box
            component="span"
            sx={{
              display: "block",
              whiteSpace: "pre-line",
              wordBreak: "break-word",
            }}
            data-grant-tooltip
          >
            {tip}
          </Box>
        }
        describeChild
      >
        <Typography
          variant="body2"
          component="span"
          tabIndex={0}
          className="grant-label"
          noWrap
          sx={{ flexShrink: 1, minWidth: 0, fontWeight: 500 }}
        >
          {label}
        </Typography>
      </Tooltip>
      <InfoPopover title={info} label={label} />
      {caption && (
        <Typography
          variant="caption"
          color="text.secondary"
          noWrap
          sx={{ flex: "1 1 0", minWidth: 0 }}
        >
          {caption}
        </Typography>
      )}
      {!caption && <Box sx={{ flex: "1 1 0" }} />}
      {badge && (
        <Chip
          size="small"
          variant="outlined"
          color={badge.color}
          icon={<WarningAmberIcon />}
          label={badge.label}
          className="grant-badge"
          sx={{ height: 20, fontSize: "0.7rem", flexShrink: 0 }}
        />
      )}
      {onRemove && <RemoveButton label={ariaLabel} onClick={onRemove} />}
    </Box>
  );
}

function RemoveButton({ label, onClick }) {
  return (
    <Tooltip title="Remove">
      <IconButton
        size="small"
        aria-label={`Remove ${label}`}
        className="grant-remove"
        onClick={onClick}
        sx={{ flexShrink: 0 }}
      >
        <CloseIcon fontSize="small" />
      </IconButton>
    </Tooltip>
  );
}

// ArgsSection is a method grant with arguments -- held (grant) or a
// draft not granted yet -- with its arguments' form inline. It saves
// itself: an edit that passes the schema's check is saved SAVE_DELAY_MS
// after the last one, or at once when focus leaves the section, one save
// at a time, each replacing the grant the last one recorded.
function ArgsSection({
  method,
  grant,
  draft,
  label,
  ariaLabel,
  state,
  idPrefix,
  disabled,
  onSave,
  onRemove,
  onAddAnother,
}) {
  const schema = method.args;
  const [value, setValue] = useState(
    () => (grant ? parseGrant(grant).with : draft?.value) ?? {},
  );
  const [server, setServer] = useState(
    () => draft?.server || { fields: {}, message: "" },
  );
  const [saving, setSaving] = useState(false);

  // The refs are what a save in flight, or a timer, reads when it runs:
  // the latest value, the grant it now replaces and the caller's onSave.
  const latest = useRef({ value, grant, onSave });
  latest.current = { value, grant, onSave };
  const pending = useRef(false);
  const inFlight = useRef(false);
  const timer = useRef(null);
  const mounted = useRef(true);

  const save = async () => {
    clearTimeout(timer.current);
    timer.current = null;
    if (!pending.current || inFlight.current) return;
    const { value: v, grant: replacing, onSave: write } = latest.current;
    if (Object.keys(liveErrorsOf(schema, v)).length > 0) return;
    pending.current = false;
    inFlight.current = true;
    setSaving(true);
    try {
      await write(v, replacing);
      if (mounted.current) setServer({ fields: {}, message: "" });
    } catch (err) {
      if (mounted.current)
        setServer({ fields: err.fields || {}, message: err.message || "" });
    } finally {
      inFlight.current = false;
      if (mounted.current) setSaving(false);
    }
  };
  const saveRef = useRef(save);
  saveRef.current = save;
  const schedule = () => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => saveRef.current(), SAVE_DELAY_MS);
  };

  // An edit made while a save was in flight is saved once it is done,
  // and once the grant it recorded is the one this section holds.
  useEffect(() => {
    if (!saving && pending.current && !timer.current) schedule();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [saving]);

  // A grant changed from outside -- the server's normalized form of a
  // save, another tab's edit -- is shown unless an edit here is newer.
  useEffect(() => {
    if (!grant || pending.current || inFlight.current) return;
    setValue(parseGrant(grant).with ?? {});
  }, [grant]);

  // Leaving the page (or the form) does not lose an edit not yet saved.
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      if (timer.current) saveRef.current();
    };
  }, []);

  const live = liveErrorsOf(schema, value);
  const errors = { ...live, ...server.fields };
  const status = saving
    ? "Saving…"
    : Object.keys(live).length > 0
      ? grant
        ? "Not saved: fix the arguments marked"
        : "Fill in the arguments marked to grant it"
      : server.message
        ? "Not saved"
        : "";

  return (
    <Box
      role="group"
      data-grant={grant || undefined}
      data-grant-kind="method"
      data-grant-draft={grant ? undefined : ""}
      aria-label={ariaLabel}
      className="grant-section"
      onBlur={(e) => {
        if (timer.current && !e.currentTarget.contains(e.relatedTarget))
          saveRef.current();
      }}
      sx={{ ...sectionSx, px: 1.5, pt: 0.75, pb: 1.5 }}
    >
      <Box
        sx={{
          display: "flex",
          alignItems: "flex-start",
          gap: 1,
          mb: 1,
          mr: onRemove ? -1 : 0,
        }}
      >
        <Box sx={{ flex: "1 1 0", minWidth: 0, pt: 0.5 }}>
          <Box sx={{ display: "flex", alignItems: "center", gap: 0.5 }}>
            <Typography
              variant="body2"
              component="div"
              className="grant-label"
              sx={{ fontWeight: 500 }}
            >
              {label}
            </Typography>
            <InfoPopover title={method.description} label={label} />
          </Box>
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ wordBreak: "break-all" }}
          >
            {method.ref}
          </Typography>
        </Box>
        {(status || !grant) && (
          <Typography
            variant="caption"
            color={
              saving
                ? "text.secondary"
                : status
                  ? "warning.main"
                  : "text.secondary"
            }
            className="grant-status"
            aria-live="polite"
            sx={{
              pt: 0.75,
              flexShrink: 0,
              maxWidth: "45%",
              textAlign: "right",
            }}
          >
            {status || "Not granted yet"}
          </Typography>
        )}
        {onAddAnother && (
          <Button
            size="small"
            onClick={onAddAnother}
            sx={{ flexShrink: 0, minWidth: 0 }}
            aria-label={`Add another ${method.method}`}
          >
            Add another
          </Button>
        )}
        {onRemove && (
          <RemoveButton
            label={grant ? ariaLabel : `${method.method} draft`}
            onClick={onRemove}
          />
        )}
      </Box>
      {state && (
        <Alert
          severity={state.severity}
          className="grant-badge"
          sx={{ mb: 1, py: 0 }}
        >
          {state.note}
        </Alert>
      )}
      <Box
        component="fieldset"
        disabled={disabled}
        sx={{ border: 0, m: 0, p: 0, minWidth: 0 }}
      >
        <CapabilityArgsForm
          schema={schema}
          value={value}
          errors={errors}
          idPrefix={idPrefix}
          onChange={(next) => {
            setValue(next);
            setServer({ fields: {}, message: "" });
            pending.current = true;
            latest.current = { ...latest.current, value: next };
            schedule();
          }}
        />
      </Box>
      {server.message && Object.keys(server.fields).length === 0 && (
        <Alert severity="error" sx={{ mt: 1.5, py: 0 }}>
          {server.message}
        </Alert>
      )}
    </Box>
  );
}
