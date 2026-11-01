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

// The New Task form's configuration (settings/new-task-form.yml,
// docs/design/config-repo-settings.md §6), as GET /api/config carries it
// (config.newTaskForm, model.NewTaskForm): resolveNewTaskForm merges the
// built-in form with the deployment's layer, then the board's, the
// repo's and the template's, into one map of fields NewTaskOverlay.jsx
// draws from. With no configuration it is the form exactly as built.

// The form's own placement of every field, in the order it draws them.
// Orders leave room between fields for a configuration to slot others
// in. "Advanced options" sits at ADVANCED_ORDER among the shown fields.
export const ADVANCED_ORDER = 150;

const BUILTIN = {
  title: { visibility: "shown", order: 10 },
  description: { visibility: "shown", order: 20 },
  attachments: { visibility: "shown", order: 25 },
  customFields: { visibility: "shown", order: 30 },
  repo: { visibility: "shown", order: 40 },
  newBranch: { visibility: "shown", order: 45 },
  base: { visibility: "shown", order: 50 },
  writes: { visibility: "shown", order: 55 },
  reads: { visibility: "shown", order: 60 },
  autoMerge: { visibility: "shown", order: 70 },
  noPort: { visibility: "shown", order: 75 },
  fixesBaseBranch: { visibility: "shown", order: 80 },
  grants: { visibility: "shown", order: 90 },
  model: { visibility: "shown", order: 100 },
  dependsOn: { visibility: "shown", order: 110 },
  agentBackendId: { visibility: "more", order: 200 },
  networkAccess: { visibility: "more", order: 210 },
  nothingNeededAction: { visibility: "more", order: 220 },
  reviewTemplateId: { visibility: "more", order: 230 },
  promptExtension: { visibility: "more", order: 235 },
  sandboxShape: { visibility: "more", order: 240 },
  approved: { visibility: "shown", order: 300 },
  atFront: { visibility: "shown", order: 310 },
};

export const NEW_TASK_FORM_FIELDS = Object.keys(BUILTIN);

// builtinDefault is each field's starting value on a form nobody has
// configured: what NewTaskOverlay.jsx has always started on.
function builtinDefault(id, config) {
  switch (id) {
    case "autoMerge":
      return !!config?.autoMergeByDefault;
    case "approved":
      return !!config?.approvedByDefault;
    case "atFront":
      return !!config?.newestFirst;
    case "nothingNeededAction":
      return "close";
    case "newBranch":
    case "noPort":
    case "fixesBaseBranch":
      return false;
    case "writes":
    case "reads":
    case "dependsOn":
    case "attachments":
      return [];
    case "repo":
    case "grants":
    case "customFields":
      // The page's repo, the capability defaults and the declared
      // fields' own defaults: NewTaskOverlay.jsx works those out.
      return undefined;
    default:
      return "";
  }
}

function sameRepo(a, b) {
  return (a || "").toLowerCase() === (b || "").toLowerCase();
}

function repoLayer(repos, repo) {
  if (!repos || !repo) return null;
  if (repos[repo]) return repos[repo];
  const key = Object.keys(repos).find((k) => sameRepo(k, repo));
  return key ? repos[key] : null;
}

// newTaskFormLayers is the layers that apply, lowest precedence first.
export function newTaskFormLayers(form, { repo, templateId, boardId } = {}) {
  if (!form) return [];
  return [
    form.default,
    boardId ? form.boards?.[boardId] : null,
    repoLayer(form.repos, repo),
    templateId ? form.templates?.[templateId] : null,
  ].filter(Boolean);
}

// resolveNewTaskForm is the form for one repo, template and board:
// {configured, fields: {<id>: {visibility, order, label, helpText,
// default, defaultSet, offered, allowNoRepo, allowChangeTargets, presets,
// defaultPreset, allowCustomNumbers}}, capabilities: {defaults, offered,
// hidden, display}}. defaultSet says a layer chose the default, which
// is what NewTaskOverlay.jsx re-applies when the repo or template
// changes; capabilities.defaults and .offered are null where no layer
// says (the catalogue's defaults, and everything, then).
export function resolveNewTaskForm(config, ctx = {}) {
  const form = config?.newTaskForm || null;
  const fields = {};
  for (const id of NEW_TASK_FORM_FIELDS) {
    fields[id] = {
      ...BUILTIN[id],
      label: "",
      helpText: "",
      default: builtinDefault(id, config),
      defaultSet: false,
    };
  }
  const capabilities = {
    defaults: null,
    offered: null,
    hidden: [],
    display: {},
  };
  for (const layer of newTaskFormLayers(form, ctx)) {
    for (const [rawId, fc] of Object.entries(layer.fields || {})) {
      const id = rawId === "legacyCapabilities" ? "grants" : rawId;
      const f = fields[id];
      if (!f || !fc) continue;
      if (fc.visibility) f.visibility = fc.visibility;
      if (typeof fc.order === "number") f.order = fc.order;
      if (fc.label) f.label = fc.label;
      if (fc.help_text) f.helpText = fc.help_text;
      if (fc.default !== undefined && fc.default !== null) {
        f.default = fc.default;
        f.defaultSet = true;
      }
      if (Array.isArray(fc.offered)) f.offered = fc.offered;
      if (typeof fc.allow_no_repo === "boolean")
        f.allowNoRepo = fc.allow_no_repo;
      if (typeof fc.allow_change_targets === "boolean")
        f.allowChangeTargets = fc.allow_change_targets;
      if (Array.isArray(fc.presets)) f.presets = fc.presets;
      if (typeof fc.default_preset === "string") {
        f.defaultPreset = fc.default_preset;
        f.defaultSet = true;
      }
      if (typeof fc.allow_custom_numbers === "boolean")
        f.allowCustomNumbers = fc.allow_custom_numbers;
    }
    const c = layer.capabilities;
    if (c) {
      if (Array.isArray(c.defaults)) capabilities.defaults = c.defaults;
      if (Array.isArray(c.offered)) capabilities.offered = c.offered;
      if (Array.isArray(c.hidden)) capabilities.hidden = c.hidden;
      for (const [k, d] of Object.entries(c.display || {})) {
        const prev = capabilities.display[k] || {};
        capabilities.display[k] = {
          ...prev,
          ...(d.label ? { label: d.label } : {}),
          ...(d.description ? { description: d.description } : {}),
          ...(typeof d.order === "number" ? { order: d.order } : {}),
        };
      }
    }
  }
  return { configured: !!form, fields, capabilities };
}

// formFieldsIn is the ids placed at visibility, in drawing order.
export function formFieldsIn(resolved, visibility) {
  return NEW_TASK_FORM_FIELDS.filter(
    (id) => resolved.fields[id].visibility === visibility,
  ).sort((a, b) => resolved.fields[a].order - resolved.fields[b].order);
}

// grantRefOf is what the picker's offered and hidden lists name a grant
// by: an api grant without its @repo, and a capability server method by
// its server as well as by itself.
function grantNames(id) {
  const g = String(id || "").trim();
  const bare = g.split("@")[0];
  const out = [g, bare];
  const hash = bare.indexOf("#");
  if (hash > 0) out.push(bare.slice(0, hash));
  return out;
}

// pickerAllows is whether the capability picker may offer id, by the
// resolved form's capabilities.offered and .hidden. A grant already held
// is the caller's to keep showing (useHeldIds): this is only the menu.
export function pickerAllows(formCaps, id) {
  if (!formCaps) return true;
  const names = grantNames(id);
  if ((formCaps.hidden || []).some((h) => names.includes(h))) return false;
  const offered = formCaps.offered;
  if (!Array.isArray(offered) || offered.includes("*")) return true;
  return offered.some((o) => names.includes(o));
}

// pickerDisplay is the configured label, description and order of id.
export function pickerDisplay(formCaps, id) {
  const display = formCaps?.display || {};
  for (const n of grantNames(id)) if (display[n]) return display[n];
  return null;
}

// sortByDisplay orders rows by their configured order, those with none
// after, each keeping its place otherwise.
export function sortByDisplay(formCaps, rows, idOf) {
  if (!formCaps || !Object.keys(formCaps.display || {}).length) return rows;
  const orderOf = (r) => {
    const d = pickerDisplay(formCaps, idOf(r));
    return d && typeof d.order === "number" ? d.order : Infinity;
  };
  return rows
    .map((r, i) => [r, i])
    .sort((a, b) => orderOf(a[0]) - orderOf(b[0]) || a[1] - b[1])
    .map(([r]) => r);
}
