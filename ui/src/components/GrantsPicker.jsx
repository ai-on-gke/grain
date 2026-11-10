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
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Link,
  ListItemIcon,
  ListItemText,
  ListSubheader,
  MenuItem,
  MenuList,
  Popover,
  TextField,
  Typography,
} from "@mui/material";
import CheckIcon from "@mui/icons-material/Check";
import WarningAmberIcon from "@mui/icons-material/WarningAmber";
import CapabilityArgsForm from "./CapabilityArgsForm.jsx";
import GrantLine, { RemoveButton, sectionSx } from "./GrantLine.jsx";
import RepoGrants, {
  REPO_READ_REF,
  RepoGrantMenuItems,
  repoGrantMenuRows,
} from "./RepoGrants.jsx";
import InfoPopover from "./InfoPopover.jsx";
import { capabilityMethods, checkGrants } from "./capabilityMethods.js";
import {
  API_ARGS_SCHEMA,
  API_CAPABILITY_SERVER,
  DANGEROUS_API_HINT,
  apiRef,
  apiShortName,
  groupApiRows,
  isApiCapabilityRef,
  isApiGrant,
  splitApiGrant,
} from "../apiGrants.js";
import {
  builtinServerName,
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
import { pickerAllows, pickerDisplay, sortByDisplay } from "../newTaskForm.js";

// GrantsPicker is the one "Grants" control of the new task overlay, the
// task pane, the template editor and the schedule editor
// (design/design/unified-grants.design.md, slice 1): every grant a task holds --
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
// It is a UI over the storage each kind has today, not a new model: all
// grants live in the task's capabilities list -- an api grant as its name,
// a method grant as its reference string, and any bare capability id an
// older store or declaration still holds as a read-only line that names
// what retired alias it became and lets the person remove it. Where a
// change lands is the caller's:
// onApiChange(next) with the whole new list of api grants,
// onSubmit(spec, replacing) with spec {capability, with} and replacing the
// method grant being edited (or null), and onRemove(grant) for a method or
// held bare capability grant. onSubmit and onRemove may reject; an error
// carrying `fields` (the server's field errors) is shown against the
// arguments it names.
//
// A method grant carries the warning states it always has: a method no
// attached server offers any more, arguments the server's current schema
// no longer accepts, or now normalizes differently. An api grant is
// flagged when its scope is dangerous, or when it is no longer offered.
//
// runKind is the kind of run the grants are for ("task", "process" or
// "ci"): each method carries the capability policy's verdict per kind
// (design/spec/capabilities/capabilities.spec.md §4.2.2), shown on it. policyRepo, where the
// form knows the repository too, has the listing judge that one run: a
// method the policy denies it is listed disabled with the policy's own
// reason rather than offered as a grant filing would refuse, and a held
// grant of one is flagged.

const asList = (m) => (Array.isArray(m) ? m : []);

// SAVE_DELAY_MS is how long an argument section waits after the last
// edit before saving it, so typing a word is one write rather than one
// per keystroke.
const SAVE_DELAY_MS = 500;

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

// Where a CI job's repository access is explained: it is declared in the
// job's workflow file, which is the one place that grants it.
const REPO_ACCESS_DOC =
  "https://github.com/ai-on-gke/grain/blob/main/docs/ci-capabilities.md#repository-access-declared-in-the-repository-enforced-by-the-teams-credential";
const REPO_ACCESS_DOC_LABEL = "Repository access docs";

function policyVerdictText(verdict, runKind) {
  if (!verdict) return "";
  const forKind = RUN_KIND_LABELS[runKind]
    ? ` for ${RUN_KIND_LABELS[runKind]}`
    : "";
  if (verdict === "ci_only") {
    return `CI only: not a grant${forKind}; a CI job declares it in its workflow's capabilities:`;
  }
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
  policyRepo = "",
  // formCapabilities is settings/new-task-form.yml's capability picker
  // configuration as resolved for the New Task form (newTaskForm.js):
  // offered and hidden narrow the "Add grant…" listing -- a grant
  // already held is still shown, and removable -- and display relabels,
  // describes and orders its rows. null everywhere else.
  formCapabilities = null,
  label = "Grants",
  helpText = "",
  // repos is the task's repository access, drawn as grants among the
  // rest (RepoGrants.jsx, unified-grants slice 5): its target, the repos
  // it reads and writes, and declared push: refs. null where the form
  // has no repository access to edit (Settings' default lists, CI).
  repos = null,
}) {
  const all = grants || [];
  const bareHeld = useMemo(
    () =>
      all.filter(
        (id) => Boolean(id) && !isCapabilityRef(id) && !isApiGrant(id),
      ),
    [all],
  );
  // Canonical https://grain.dev/capabilities/api#<scope> grants are
  // capability server methods like any other, edited with their {repo}
  // argument among the method sections; only legacy api:<scope>[@repo]
  // strings from unmigrated rows stay in the api group.
  const apiHeld = useMemo(
    () => all.filter((id) => isApiGrant(id) && !isApiCapabilityRef(id)),
    [all],
  );
  const refs = useMemo(() => all.filter(isCapabilityRef), [all]);
  const offered = config?.capabilities || [];
  const { presets, areas } = groupApiRows(offered);
  const apiRowOf = (id) => {
    const [target] = splitApiGrant(id);
    return offered.find((c) => splitApiGrant(c.id)[0] === target);
  };

  const [methods, setMethods] = useState(null);
  const [loadError, setLoadError] = useState("");
  const [checks, setChecks] = useState({});
  const [anchor, setAnchor] = useState(null);
  const [query, setQuery] = useState("");
  // drafts are argument sections not granted yet: {id, method, value,
  // server: {fields, message}}, each shown after its method's grants.
  const [drafts, setDrafts] = useState([]);
  // repoDrafts is the repository grants picked from "Add grant…" and not
  // given a repo yet: "read" or "write", each a repo picker in the list.
  const [repoDrafts, setRepoDrafts] = useState([]);
  const [error, setError] = useState("");
  const listRef = useRef(null);
  const addButtonRef = useRef(null);
  const restoreFocus = useRef(false);
  const popoverAction = useRef(null);
  // Reposition synchronously before paint when filtering or method loading
  // shrinks a viewport-clamped menu: MUI's own Popover repositions in a
  // post-paint useEffect, leaving one frame where the filtered row sits above
  // the repositioned paper so an immediate click lands on the backdrop.
  useLayoutEffect(() => {
    if (anchor) popoverAction.current?.updatePosition();
  }, [anchor, query, methods, offered.length, formCapabilities]);

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
  // A repo still being typed, or mistyped, is no run to ask about: the
  // listing names none rather than fail, and filing reports the repo.
  const typedRepo = (policyRepo || "").trim();
  const subjectRepo =
    runKind && /^[^/\s]+\/[^/\s]+$/.test(typedRepo) ? typedRepo : "";
  // With the repo known the listing has judged a task's or process's
  // one run, so a method it does not refuse is admitted: the kind's
  // "may be refused" (true of some other repo) would only mislead. A CI
  // job's trigger event is still unknown, so it keeps the kind's verdict.
  const kindVerdictShown =
    Boolean(runKind) && (!subjectRepo || runKind === "ci");
  // A CI-only method (repo#read) is refused this run by grain's own rule,
  // not by the capability policy, so it is worded as that and points at
  // how a CI job gets it -- declared in the job's own workflow file. The
  // repository's CI capabilities list does not grant it, and neither does
  // the policy, which has no rule to change.
  const ciOnly = (m) => Boolean(runKind) && m?.policy?.[runKind] === "ci_only";
  const load = useCallback(
    (fresh) => {
      const subject = subjectRepo ? { kind: runKind, repo: subjectRepo } : null;
      return capabilityMethods({ fresh, subject })
        .then((m) => {
          if (!live.current) return null;
          loaded.current = true;
          setLoadError("");
          // Only methods: a row without a ref is nothing to grant.
          const listed = asList(m).filter((x) => typeof x?.ref === "string");
          setMethods(listed);
          return listed;
        })
        .catch((err) => {
          if (live.current) setLoadError(err.message);
          return null;
        });
    },
    [runKind, subjectRepo],
  );
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
        .filter((ref) => !isApiCapabilityRef(ref))
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

  const toCheck = useMemo(() => [...bareHeld, ...refs], [bareHeld, refs]);
  const toCheckKey = toCheck.join("\n");
  useEffect(() => {
    if (toCheck.length === 0) return undefined;
    let current = true;
    checkGrants(toCheck.map((grant) => ({ grant })))
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
  }, [toCheckKey]);

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
    if (isApiCapabilityRef(method.ref)) {
      if (refs.includes(method.ref)) {
        addDraft(method, {});
        return;
      }
      Promise.resolve(
        onSubmit({ capability: method.ref, with: undefined }, null),
      ).catch((err) => setError(err.message));
      return;
    }
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
    const ref = apiRef(apiShortName(row.id));
    const heldRef = refs.includes(ref)
      ? ref
      : refs.find((g) => parseGrant(g).ref === ref);
    if (heldRef) {
      removeMethod(heldRef);
      return;
    }
    if (apiHeld.some((id) => splitApiGrant(id)[0] === row.id)) {
      onApiChange(apiHeld.filter((id) => splitApiGrant(id)[0] !== row.id));
      return;
    }
    setError("");
    Promise.resolve(onSubmit({ capability: ref, with: undefined }, null)).catch(
      (err) => setError(err.message),
    );
  };

  const closeMenu = useCallback((focusButton = false) => {
    if (focusButton) restoreFocus.current = true;
    setAnchor(null);
  }, []);

  useEffect(() => {
    if (!anchor && restoreFocus.current) {
      restoreFocus.current = false;
      addButtonRef.current?.focus();
    }
  }, [anchor]);

  // Keep Escape for the open popover even before MUI's ModalManager effect
  // has promoted it above the surrounding Overlay/SidePanel, or when focus
  // slipped off the menu's items onto body. Bubble phase so an open
  // SearchSelect listbox inside the popover still takes the first Escape.
  const menuOpen = Boolean(anchor);
  useLayoutEffect(() => {
    if (!menuOpen) return undefined;
    const onKeyDown = (e) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      e.preventDefault();
      closeMenu(true);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [menuOpen, closeMenu]);

  const openMenu = (evt) => {
    setQuery("");
    setAnchor(evt.currentTarget);
    load(true);
  };

  const shortAlias = (g) => {
    if (isApiGrant(g)) {
      const [, limit] = splitApiGrant(g);
      return `api:${apiShortName(g)}${limit ? ` on ${limit}` : ""}`;
    }
    if (isCapabilityRef(g)) {
      const { ref, with: w, raw } = parseGrant(g);
      if (raw === undefined) {
        const { capability, method } = splitRef(ref);
        const builtin = builtinServerName(capability);
        if (builtin && method) {
          const hasArgs = w && Object.keys(w).length > 0;
          return hasArgs
            ? `${builtin}#${method}=${JSON.stringify(w)}`
            : `${builtin}#${method}`;
        }
      }
    }
    return g;
  };

  const bareLine = (id) => {
    const check = checks[id];
    const aliasOf = Array.isArray(check?.aliasOf) ? check.aliasOf : [];
    const row = offered.find((c) => c.id === id);
    let ariaLabel = id;
    let caption = row?.summary || row?.description || "";
    let tip = row?.description ? `${id}\n${row.description}` : id;
    let badge = null;
    if (aliasOf.length > 0) {
      const short = aliasOf.map(shortAlias).join(" + ");
      ariaLabel = `${id} (retired alias)`;
      caption = `Retired alias of ${short}`;
      tip = `Retired alias of ${aliasOf.join(" and ")}. Remove it, or replace it with ${aliasOf.length > 1 ? "those grants" : "that grant"}.\n\n${id}`;
      badge = { label: "retired alias", color: "info" };
    } else if (check?.error) {
      ariaLabel = `${id} (no longer offered)`;
      caption = "No longer offered";
      tip = `${check.error}. Remove this grant.\n\n${id}`;
      badge = { label: "no longer offered", color: "warning" };
    }
    return (
      <GrantLine
        key={id}
        grant={id}
        kind="capability"
        label={id}
        ariaLabel={ariaLabel}
        tip={tip}
        caption={caption}
        badge={badge}
        onRemove={disabled ? undefined : () => removeMethod(id)}
      />
    );
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
    if (ciOnly(byRef[ref])) {
      const policyNote = policyVerdictText("ci_only", runKind);
      return {
        severity: "warning",
        badge: "CI only",
        suffix: " (CI only)",
        textNote: `This grant is ${policyNote}. Remove this grant: a CI job that needs it declares it in its own .grain/workflows file (${REPO_ACCESS_DOC_LABEL}).`,
        note: (
          <>
            This grant is {policyNote}. Remove this grant: a CI job that needs
            it declares it in its own .grain/workflows file (
            <Link href={REPO_ACCESS_DOC} target="_blank" rel="noreferrer">
              {REPO_ACCESS_DOC_LABEL}
            </Link>
            ).
          </>
        ),
      };
    }
    const refused = byRef[ref]?.policyRefusal;
    if (refused)
      return {
        severity: "warning",
        badge: "denied by policy",
        suffix: " (denied by the capability policy)",
        textNote: `${refused}. Remove this grant, or change the capability policy (Settings → Capabilities).`,
        note: (
          <>
            {refused}. Remove this grant, or change the capability policy (
            <Link href="/settings/capabilities#capability-policy">
              Settings → Capabilities
            </Link>
            ).
          </>
        ),
      };
    const verdict = kindVerdictShown
      ? byRef[ref]?.policy?.[runKind]
      : undefined;
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

  // methodOf is ref's method as GET /api/capability-methods lists it. An
  // api scope always takes its optional {repo} argument
  // (grainserver.buildAPIServer), so its section keeps the inline repo
  // field before that listing loads -- and if a listing leaves the
  // schema out -- rather than switching shape under someone's click.
  const methodOf = (ref) => {
    const listed = byRef[ref];
    if (isApiCapabilityRef(ref)) {
      if (listed && hasArguments(listed.args)) return listed;
      const row = apiRowOf(ref);
      return {
        ref,
        capability: API_CAPABILITY_SERVER,
        method: listed?.method || apiShortName(ref),
        description:
          listed?.description ||
          (row ? row.summary || row.description || "" : ""),
        args: API_ARGS_SCHEMA,
        requiresArgs: false,
      };
    }
    return listed || null;
  };

  // methodState is what is wrong with a held method grant, if anything.
  const methodState = (g) => {
    const { ref } = parseGrant(g);
    const check = checks[g];
    if (isApiCapabilityRef(ref)) {
      const row = apiRowOf(ref);
      if (
        !row &&
        offered.some((c) => isApiGrant(c.id)) &&
        !(methods && byRef[ref])
      ) {
        return {
          severity: "error",
          badge: "no longer offered",
          suffix: " (no longer offered)",
          textNote: "No longer offered -- remove it.",
          note: "No longer offered -- remove it.",
        };
      }
      if (check && check.error && !check.unchecked)
        return {
          severity: "warning",
          badge: "no longer validates",
          suffix: " (no longer validates)",
          note: `These arguments no longer validate under the server's current schema: ${check.error}`,
        };
      if (row?.dangerous) {
        return {
          severity: "warning",
          badge: "dangerous",
          suffix: " (dangerous)",
          textNote: DANGEROUS_API_HINT,
          note: DANGEROUS_API_HINT,
        };
      }
      return policyState(ref);
    }
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
    const method = methodOf(ref) || ds[0]?.method;
    const withArgs = method && hasArguments(method.args);
    const isApiRef = isApiCapabilityRef(ref);
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
      const sectionLabel = isApiRef
        ? `api:${method.method}`
        : methodMenuLabel(ref, method.method);
      const sectionAriaLabel = isApiRef
        ? `${d.args ? `${sectionLabel} · ${d.args}` : sectionLabel}${state?.suffix || ""}`
        : `${d.args ? `${d.label} · ${d.args}` : d.label}${state?.suffix || ""}`;
      return (
        <ArgsSection
          key={key}
          method={method}
          grant={g || null}
          draft={draft || null}
          label={sectionLabel}
          ariaLabel={sectionAriaLabel}
          state={state}
          idPrefix={`${idPrefix}-args-${argsIndex++}`}
          disabled={disabled}
          onSave={async (value, replacing) => {
            const normalizedWith =
              isApiRef &&
              value &&
              typeof value === "object" &&
              !Array.isArray(value) &&
              (!value.repo || Object.keys(value).length === 0)
                ? undefined
                : value;
            await onSubmit(
              { capability: ref, with: normalizedWith },
              replacing,
            );
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
  const formOffers = (id) => pickerAllows(formCapabilities, id);
  const displayed = (row) => {
    const d = pickerDisplay(formCapabilities, row.id);
    return d
      ? {
          ...row,
          displayLabel: d.label || "",
          summary: d.description || row.summary,
        }
      : row;
  };
  const arrange = (rows) =>
    sortByDisplay(
      formCapabilities,
      rows.filter((r) => formOffers(r.id)).map(displayed),
      (r) => r.id,
    );
  const shownPresets = arrange(presets).filter(apiMatches);
  if (shownPresets.length > 0)
    menuGroups.push({
      key: "api-presets",
      kind: "api",
      title: "Grain API presets",
      rows: shownPresets,
    });
  for (const { area, rows } of areas) {
    const shown = arrange(rows).filter(apiMatches);
    if (shown.length > 0)
      menuGroups.push({
        key: `api-${area}`,
        kind: "api",
        area,
        title: `Grain API scopes: ${area}`,
        rows: shown,
      });
  }
  const repoRows = repoGrantMenuRows(repos, matches);
  if (repoRows.length > 0)
    menuGroups.unshift({
      key: "repos",
      kind: "repo",
      title: "Repository access",
      rows: repoRows,
    });
  const shownMethods = sortByDisplay(
    formCapabilities,
    (methods || []).filter(
      (m) =>
        !isApiCapabilityRef(m.ref) &&
        // A task's read access is its reads list, offered above as
        // "Read a repository"; the listed method is a CI job's.
        !(repos && m.ref === REPO_READ_REF) &&
        formOffers(m.ref) &&
        matches(m.ref, m.method, m.description),
    ),
    (m) => m.ref,
  );
  if (shownMethods.length > 0)
    menuGroups.push({
      key: "methods",
      kind: "method",
      title: "Capability server methods",
      rows: shownMethods,
    });

  const apiItem = (row) => {
    const name = row.displayLabel || apiShortName(row.id);
    const ref = apiRef(apiShortName(row.id));
    const held =
      refs.some((g) => parseGrant(g).ref === ref) ||
      apiHeld.some((id) => splitApiGrant(id)[0] === row.id);
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

  // A method the policy denies the one run this picker is for (a repo
  // is known: policyRepo) is listed disabled, with the policy's own
  // reason; one it may deny some run of the kind carries the verdict.
  const methodItem = (m) => {
    const isCIOnly = ciOnly(m);
    const refused = isCIOnly
      ? m.policyRefusal || policyVerdictText("ci_only", runKind)
      : m.policyRefusal || "";
    const verdict = isCIOnly
      ? "ci_only"
      : refused
        ? "denied"
        : kindVerdictShown
          ? m.policy?.[runKind]
          : undefined;
    const policyNote = isCIOnly
      ? policyVerdictText("ci_only", runKind)
      : refused || policyVerdictText(verdict, runKind);
    const refSummary = `${splitRef(m.ref).capability.replace(/^https:\/\//, "")}${m.requiresArgs ? " · needs arguments" : ""}`;
    return (
      <MenuItem
        key={m.ref}
        className="method-grant-row"
        data-method={m.ref}
        data-policy-verdict={verdict || undefined}
        data-policy-denied={refused && !isCIOnly ? "true" : undefined}
        data-ci-only={isCIOnly ? "true" : undefined}
        title={
          refused || policyNote ? `${m.ref} — ${refused || policyNote}` : m.ref
        }
        disabled={Boolean(refused)}
        onClick={refused ? undefined : () => addMethod(m)}
        sx={{
          whiteSpace: "normal",
          alignItems: "flex-start",
          // Disabled, but its reason reads at full contrast and its
          // Settings link still follows.
          "&.Mui-disabled": { opacity: 1, pointerEvents: "auto" },
        }}
      >
        <ListItemIcon sx={{ pt: 0.25 }}>
          {verdict ? (
            <WarningAmberIcon fontSize="small" color="warning" />
          ) : null}
        </ListItemIcon>
        <ListItemText
          sx={{ minWidth: 0 }}
          primary={
            pickerDisplay(formCapabilities, m.ref)?.label ||
            methodMenuLabel(m.ref, m.method)
          }
          primaryTypographyProps={
            refused ? { color: "text.disabled" } : undefined
          }
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
                    href={
                      isCIOnly
                        ? REPO_ACCESS_DOC
                        : "/settings/capabilities#capability-policy"
                    }
                    {...(isCIOnly
                      ? { target: "_blank", rel: "noreferrer" }
                      : {})}
                    onClick={(e) => e.stopPropagation()}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ")
                        e.stopPropagation();
                    }}
                  >
                    {isCIOnly
                      ? REPO_ACCESS_DOC_LABEL
                      : "Settings → Capabilities"}
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
  const anyMethods = (methods || []).some((m) => !isApiGrant(m.ref));
  const reposHeld = repos
    ? (repos.target ? 1 : 0) +
      (repos.reads || []).length +
      (repos.writes || []).length +
      (repos.push || []).length
    : 0;
  const held =
    bareHeld.length +
    apiHeld.length +
    refs.length +
    drafts.length +
    reposHeld +
    repoDrafts.length;
  const stillLoading = count > 0 && methods === null;
  if (
    !repos &&
    !anyApi &&
    !anyMethods &&
    held === 0 &&
    !loadError &&
    !stillLoading
  )
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
        {label}
      </Typography>
      <Typography
        variant="caption"
        color="text.secondary"
        component="p"
        sx={{ mb: 0.75 }}
      >
        {helpText || (
          <>
            What the agent may use:{" "}
            {repos ? "the repositories it may read and write, " : ""}
            grain&apos;s own API (docs/agent-api.md) and capability server
            methods (docs/capabilities.md).
          </>
        )}
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
        {repos && (
          <RepoGrants
            repos={repos}
            drafts={repoDrafts}
            disabled={disabled}
            onDropDraft={(i) => {
              setRepoDrafts((ds) => ds.filter((_, j) => j !== i));
              // The picker that had focus is gone with its draft: focus
              // goes back to "Add grant…" rather than to the page, where
              // the overlay's own keys (Escape) would no longer reach.
              setTimeout(() => addButtonRef.current?.focus(), 0);
            }}
          />
        )}
        {bareHeld.map(bareLine)}
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
          ref={addButtonRef}
          size="small"
          variant="outlined"
          sx={{ mt: 0.75 }}
          onClick={openMenu}
          onKeyDown={(e) => {
            if (e.key === "Escape" && anchor) {
              e.preventDefault();
              e.stopPropagation();
              closeMenu(true);
            }
          }}
          aria-haspopup="menu"
          aria-controls={anchor ? `${regionId}-menu` : undefined}
        >
          Add grant…
        </Button>
      )}
      <Popover
        action={popoverAction}
        open={menuOpen}
        anchorEl={anchor}
        data-open-popover={menuOpen ? "" : undefined}
        onClose={(_event, reason) => closeMenu(reason === "escapeKeyDown")}
        transitionDuration={0}
        onKeyDown={(e) => {
          if (e.key === "Escape" && !e.defaultPrevented) {
            e.preventDefault();
            e.stopPropagation();
            closeMenu(true);
          }
        }}
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
              // A group's title sits among the menu's items (MenuList moves
              // focus over direct children only, so it cannot wrap them in
              // a role="group"). As a bare <li> it was a list item with no
              // list inside role="menu": axe's listitem and
              // aria-required-children (2026-11-08 walkthrough, F3). A
              // named separator is a child a menu may own.
              role="separator"
              aria-label={g.title}
              className={
                g.kind === "api" && g.area ? "api-scope-area" : undefined
              }
              data-grant-group={g.key}
              sx={{ lineHeight: "32px" }}
            >
              {g.title}
            </ListSubheader>,
            ...(g.kind === "repo"
              ? RepoGrantMenuItems({
                  rows: g.rows,
                  onPick: (kind) => {
                    setAnchor(null);
                    setRepoDrafts((ds) => [...ds, kind]);
                  },
                })
              : g.rows.map(g.kind === "api" ? apiItem : methodItem)),
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
