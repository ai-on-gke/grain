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

import { createContext, useContext, useEffect, useState } from "react";
import {
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";

export const SettingsReadOnlyContext = createContext(false);

// SettingsCard is one card of a Settings section: what used to be a
// sub-heading of a System tab ("CI queue", "Sandbox budget"), with an
// anchor so /settings/ci#ci-queue scrolls to it
// (docs/design/settings-panel.md §3.1).
//
// A card wrapping a panel that already names its own parts (a
// connection list, the memory editor) is given `label` instead of a
// title, so the section heading is not said twice.
export default function SettingsCard({
  title,
  label,
  anchor,
  allowInExternalMode = false,
  children,
}) {
  const readOnly = useContext(SettingsReadOnlyContext) && !allowInExternalMode;
  return (
    <Paper
      variant="outlined"
      component="section"
      id={anchor}
      aria-label={title || label}
      data-settings-card={anchor}
      sx={{ p: { xs: 1.5, sm: 2 }, mb: 2, scrollMarginTop: 16 }}
    >
      {title && (
        <Typography variant="subtitle1" component="h3" sx={{ fontWeight: 600 }}>
          {title}
        </Typography>
      )}
      {readOnly ? (
        // A disabled fieldset disables every native control inside it,
        // but MUI draws a Select, a Checkbox's box and a chip's delete as
        // elements of its own: those are dimmed and stopped by hand, so a
        // read-only card neither looks nor acts editable. Links still
        // work -- "View in config repo" is one.
        <Box
          component="fieldset"
          disabled
          data-settings-readonly
          sx={{
            border: 0,
            p: 0,
            m: 0,
            minInlineSize: 0,
            "& button:disabled, & .MuiCheckbox-root:has(input:disabled), & .MuiSwitch-root:has(input:disabled), & .MuiRadio-root:has(input:disabled)":
              { opacity: 0.5, cursor: "default" },
            "& .MuiSelect-select, & .MuiChip-deleteIcon, & .MuiAutocomplete-root":
              { pointerEvents: "none" },
          }}
        >
          {children}
        </Box>
      ) : (
        children
      )}
    </Paper>
  );
}

// settingHints are the helper texts and chips a field shows when it is
// pending a restart or stored with a value grain cannot run on (§3.4
// rule 5), read off GET /api/settings.
export function settingHints(settings) {
  const restartRequired = new Set(settings?.restartRequired || []);
  const pendingRestart = new Set(settings?.pendingRestart || []);
  const unrunnable = new Map(
    (settings?.unrunnable || []).map((u) => [u.key, u]),
  );

  const restartHint = (field, base) => {
    if (!restartRequired.has(field)) return base;
    if (pendingRestart.has(field)) {
      return (
        <>
          {base ? `${base} ` : ""}
          <Box component="span" sx={{ color: "warning.main" }}>
            Changed, but not applied &mdash; the daemon is still running with
            its previous value. Restart it to apply.
          </Box>
        </>
      );
    }
    return `${base ? `${base} ` : ""}Takes effect when the daemon restarts, unlike every other setting here.`;
  };

  const restartChip = (field) => {
    if (!restartRequired.has(field)) return null;
    const changed = pendingRestart.has(field);
    return (
      <Chip
        size="small"
        variant={changed ? "filled" : "outlined"}
        color={changed ? "warning" : "default"}
        label={changed ? "restart to apply" : "needs restart"}
        sx={{ ml: 1 }}
      />
    );
  };

  const unrunnableHint = (field, base) => {
    const u = unrunnable.get(field);
    if (!u) return restartHint(field, base);
    return (
      <>
        {base ? `${base} ` : ""}
        <Box component="span" sx={{ color: "warning.main" }}>
          Stored value {u.stored} is not one grain can run on, so this
          deployment is running with {u.running} &mdash; the value shown here.
          Save to store it.
        </Box>
      </>
    );
  };

  const unrunnableChip = (field) => {
    if (!unrunnable.has(field)) return restartChip(field);
    return (
      <Chip
        size="small"
        variant="filled"
        color="warning"
        label="not runnable"
        sx={{ ml: 1 }}
      />
    );
  };

  return {
    restartRequired,
    restartHint,
    restartChip,
    unrunnableHint,
    unrunnableChip,
  };
}

// MultilineSetting is a multi-line /api/settings field: it commits on
// its own Save, with Discard beside it, rather than on blur (§3.4 rule
// 1), and tells the section it holds an edit while it does.
export function MultilineSetting({
  s,
  card,
  name,
  label,
  helperText,
  minRows = 4,
}) {
  const stored = s.settings?.[name] || "";
  const [value, setValue] = useState(stored);
  const [saving, setSaving] = useState(false);
  useEffect(() => setValue(stored), [stored]);
  const dirty = value !== stored;
  const { markDirty } = s;
  useEffect(() => {
    markDirty(card, dirty);
  }, [markDirty, card, dirty]);
  useEffect(() => () => markDirty(card, false), [markDirty, card]);
  const readOnly = useContext(SettingsReadOnlyContext) || Boolean(s.readOnly);
  return (
    <>
      <TextField
        name={name}
        data-setting-id={name}
        label={label}
        helperText={helperText}
        value={value}
        onChange={(evt) => setValue(evt.target.value)}
        disabled={readOnly}
        multiline
        minRows={minRows}
        autoComplete="off"
        fullWidth
        margin="normal"
      />
      {dirty && (
        <Stack
          direction="row"
          spacing={1}
          justifyContent="flex-end"
          className="settings-card-actions"
        >
          <Button
            onClick={() => setValue(stored)}
            disabled={saving}
            data-testid={`${name}-discard`}
          >
            Discard
          </Button>
          <Button
            variant="contained"
            disabled={saving}
            data-testid={`${name}-save`}
            onClick={async () => {
              const trimmed = value.trim();
              setSaving(true);
              if (await s.saveChoice({ [name]: trimmed })) {
                setValue(trimmed);
              }
              setSaving(false);
            }}
          >
            Save
          </Button>
        </Stack>
      )}
    </>
  );
}

// DefaultableField renders a single-line setting that pre-fills its effective
// default value in the input rather than leaving the field empty with only a
// placeholder. It displays a small "default" chip when the effective value is
// the default, turns the chip into "custom" with a "Reset to default" action
// when edited or customized, and stores empty ("") when an unchanged default
// is blurred or reset so future default changes continue to apply.
export function DefaultableField({
  s,
  name,
  label,
  helperText,
  defaultValue,
  emptyValue = "",
  endAdornment,
  placeholder,
  inputProps,
  type = "text",
}) {
  const readOnly = useContext(SettingsReadOnlyContext) || Boolean(s.readOnly);
  const stored = s.settings?.[name];
  const apiSource =
    s.settings?.sources?.[name] ?? s.settings?.settingSources?.[name];
  const defaultStr =
    defaultValue !== undefined && defaultValue !== null
      ? String(defaultValue)
      : "";
  const storedStr =
    stored !== undefined && stored !== null ? String(stored) : "";
  const isStoredCustom =
    apiSource !== undefined
      ? apiSource === "custom"
      : storedStr.trim() !== "" && storedStr.trim() !== defaultStr.trim();

  const effectiveValue = isStoredCustom ? storedStr : defaultStr;
  const [draft, setDraft] = useState(effectiveValue);
  const [hasSavedCustom, setHasSavedCustom] = useState(isStoredCustom);

  useEffect(() => {
    setDraft(effectiveValue);
  }, [effectiveValue]);

  useEffect(() => {
    setHasSavedCustom(isStoredCustom);
  }, [isStoredCustom]);

  const trimmedDraft = draft.trim();
  const isDraftCustom =
    trimmedDraft !== "" && trimmedDraft !== defaultStr.trim();

  const handleReset = () => {
    setDraft(defaultStr);
    setHasSavedCustom(false);
    s.saveChoice?.({ [name]: emptyValue });
  };

  // save sends value, and puts the box back to what the server holds if
  // the server refuses it: the draft is this field's own state, which a
  // rolled-back setting does not reach when its source chip is
  // unchanged (2026-11-01 walkthrough: a refused namespace "Walk NS"
  // stayed in the box, looking saved).
  const save = (value) => {
    const before = { draft: effectiveValue, custom: isStoredCustom };
    Promise.resolve(s.saveChoice?.({ [name]: value })).then((saved) => {
      if (saved !== false) return;
      setDraft(before.draft);
      setHasSavedCustom(before.custom);
    });
  };

  const handleBlur = () => {
    const val = draft.trim();
    if (!isStoredCustom && !hasSavedCustom) {
      if (val === "" || val === defaultStr.trim()) {
        setDraft(defaultStr);
        s.unchanged?.(name);
        return;
      }
      setDraft(val);
      setHasSavedCustom(true);
      save(val);
      return;
    }
    if (val === "" || val === defaultStr.trim()) {
      setDraft(defaultStr);
      setHasSavedCustom(false);
      save(emptyValue);
      return;
    }
    if (val === storedStr.trim()) {
      setDraft(val);
      s.unchanged?.(name);
      return;
    }
    setDraft(val);
    setHasSavedCustom(true);
    save(val);
  };

  return (
    <TextField
      name={name}
      data-setting-id={name}
      label={label}
      helperText={helperText}
      type={type}
      value={draft}
      placeholder={placeholder ?? (defaultStr || undefined)}
      onChange={(evt) => setDraft(evt.target.value)}
      onBlur={handleBlur}
      onKeyDown={s.onEnterKey}
      disabled={readOnly}
      inputProps={inputProps}
      autoComplete="off"
      fullWidth
      margin="normal"
      InputProps={{
        endAdornment: (
          <Stack
            direction="row"
            spacing={0.5}
            alignItems="center"
            sx={{ flexShrink: 0 }}
          >
            <Chip
              size="small"
              label={isDraftCustom ? "custom" : "default"}
              color={isDraftCustom ? "primary" : "default"}
              variant={isDraftCustom ? "outlined" : "filled"}
              data-testid={`${name}-source-chip`}
            />
            {isDraftCustom && !readOnly && (
              <Button
                size="small"
                onMouseDown={(evt) => evt.preventDefault()}
                onClick={handleReset}
                data-testid={`${name}-reset`}
                sx={{ whiteSpace: "nowrap", textTransform: "none" }}
              >
                Reset to default
              </Button>
            )}
            {endAdornment}
          </Stack>
        ),
      }}
    />
  );
}
