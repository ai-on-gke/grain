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

import { useCallback, useEffect, useRef, useState } from "react";
import api from "./api.js";
import { readNumberBox } from "./numberBox.js";
import {
  settingErrorMessage,
  settingLabel,
  settingMin,
} from "./settingsPlan.js";
import { guardLeaving } from "./unsavedEdits.js";

// useSettingsSection is the Settings view's one save and dirty-state
// model (docs/design/settings-panel.md §3.4). It holds GET /api/settings
// for every section, and the gestures that commit a field:
//
//   - checkboxes, radios and selects through saveChoice, on change;
//   - single-line text and number boxes through onTextBlur, onIntBlur
//     and onFloatBlur, on blur or Enter (onEnterKey);
//   - multi-line and composite cards through their own Save, which
//     report an edit they hold through markDirty.
//
// saveChoice is AdminOverlay's: optimistic, and rolled back when the PUT
// fails. After a save the field shows what the server answered, not what
// was typed ("1m" reads back as "60s").
//
// The state the section header reads is `status`: "saving" while a
// request is in flight, "dirty" while a Save card holds an edit (dirty
// names it), "error" when the last save failed (error.key, error.field
// and error.message, and retry to send it again), or "saved". Putting
// the failed field's box back to its saved value clears the error, and
// dismissError takes down the banner showError raised for it.
export function useSettingsSection({ showError, dismissError, onSaved } = {}) {
  const [settings, setSettings] = useState(null);
  // Why the last GET /api/settings failed, or "" once one has succeeded.
  const [settingsError, setSettingsError] = useState("");
  const [inFlight, setInFlight] = useState(0);
  const [error, setError] = useState(null);
  const errorRef = useRef(null);
  errorRef.current = error;
  // Which Save cards hold an edit, by card title. A ref as well as
  // state: the leave guard reads it outside of any render.
  const [dirtyCards, setDirtyCards] = useState([]);
  const dirtyRef = useRef(new Set());
  const settingsRef = useRef(null);
  settingsRef.current = settings;

  useEffect(() => guardLeaving(() => dirtyRef.current.size > 0), []);

  const markDirty = useCallback((card, dirty) => {
    const had = dirtyRef.current.has(card);
    if (had === Boolean(dirty)) return;
    if (dirty) dirtyRef.current.add(card);
    else dirtyRef.current.delete(card);
    setDirtyCards([...dirtyRef.current]);
  }, []);

  const reloadSettings = useCallback(async () => {
    try {
      const loaded = await api("/api/settings");
      setSettingsError("");
      if (loaded) setSettings(loaded);
    } catch (err) {
      setSettingsError(err?.message || String(err));
      showError?.(err);
    }
  }, [showError]);

  const save = useCallback(
    async (payload) => {
      setInFlight((n) => n + 1);
      try {
        const updated = await api("/api/settings", {
          method: "PUT",
          body: JSON.stringify(payload),
        });
        setSettings((prev) => ({ ...prev, ...payload, ...updated }));
        setError(null);
        try {
          await onSaved?.();
        } catch (err) {
          showError?.(err);
        }
        return true;
      } catch (err) {
        const field = Object.keys(payload)[0] || "";
        const message = settingErrorMessage(err?.message || String(err));
        setError({
          key: field,
          field: settingLabel(field),
          message,
          retry: () => saveChoiceRef.current?.(payload),
        });
        showError?.(message);
        return false;
      } finally {
        setInFlight((n) => n - 1);
      }
    },
    [onSaved, showError],
  );

  const saveChoice = useCallback(
    (payload) => {
      const current = settingsRef.current || {};
      const previous = Object.fromEntries(
        Object.keys(payload).map((key) => [key, current[key]]),
      );
      setSettings((prev) => ({ ...prev, ...payload }));
      return save(payload).then((saved) => {
        if (!saved) {
          setSettings((prev) => {
            const rollback = {};
            for (const key of Object.keys(payload)) {
              if (prev[key] === payload[key]) rollback[key] = previous[key];
            }
            return { ...prev, ...rollback };
          });
        }
        return saved;
      });
    },
    [save],
  );
  const saveChoiceRef = useRef(saveChoice);
  saveChoiceRef.current = saveChoice;

  // unchanged is a blur that leaves a box at what the server holds: no
  // save, but a failed save of that field is no longer outstanding --
  // the box was put back -- so the status stops saying it failed.
  const unchanged = (key) => {
    const failed = errorRef.current;
    if (failed?.key !== key) return;
    setError(null);
    dismissError?.(failed.message);
  };

  const refuseBox = (key, message) => {
    setError({
      key,
      field: settingLabel(key),
      message,
    });
    showError?.(message);
  };

  const onTextBlur = (key) => (evt) => {
    const val = evt.target.value.trim();
    if (val !== (settingsRef.current?.[key] || "")) {
      saveChoice({ [key]: val });
    } else {
      unchanged(key);
    }
  };

  // readFrom names the settings field the box shows, when that is not
  // the one it saves: the merge batch size box shows what was chosen
  // (mergeBatchSizeChosen, blank for grain's default) and saves
  // mergeBatchSize.
  const onIntBlur =
    (key, { defaultVal = 0, emptyVal = 0, readFrom = key } = {}) =>
    (evt) => {
      const current = settingsRef.current || {};
      const read = readNumberBox(evt.target, {
        integer: true,
        min: settingMin(key),
        label: settingLabel(key),
      });
      if (read.error) {
        refuseBox(key, read.error);
        return;
      }
      if (read.empty) {
        if ((current[readFrom] ?? defaultVal) !== emptyVal) {
          saveChoice({ [key]: emptyVal });
        } else {
          evt.target.value = evt.target.defaultValue ?? "";
          unchanged(key);
        }
        return;
      }
      if (read.value !== (current[readFrom] ?? defaultVal)) {
        saveChoice({ [key]: read.value });
      } else {
        unchanged(key);
      }
    };

  const onFloatBlur =
    (key, { defaultVal = 0, emptyVal = 0 } = {}) =>
    (evt) => {
      const current = settingsRef.current || {};
      const read = readNumberBox(evt.target, {
        min: settingMin(key),
        label: settingLabel(key),
      });
      if (read.error) {
        refuseBox(key, read.error);
        return;
      }
      if (read.empty) {
        if ((current[key] ?? defaultVal) !== emptyVal) {
          saveChoice({ [key]: emptyVal });
        } else {
          evt.target.value = evt.target.defaultValue ?? "";
          unchanged(key);
        }
        return;
      }
      if (read.value !== (current[key] ?? defaultVal)) {
        saveChoice({ [key]: read.value });
      } else {
        unchanged(key);
      }
    };

  const onEnterKey = (evt) => {
    if (evt.key === "Enter") evt.target.blur();
  };

  let status = "saved";
  if (inFlight > 0) status = "saving";
  else if (error) status = "error";
  else if (dirtyCards.length > 0) status = "dirty";

  return {
    settings,
    setSettings,
    settingsError,
    reloadSettings,
    save,
    saveChoice,
    onTextBlur,
    onIntBlur,
    onFloatBlur,
    onEnterKey,
    refuseBox,
    unchanged,
    markDirty,
    dirtyCards,
    error,
    status,
  };
}

// statusLine is what the section header says for a status.
export function statusLine({ status, dirtyCards = [], error }) {
  if (status === "saving") return "Saving…";
  // The error itself is in the banner showError raises; saying it here
  // as well put the same sentence on screen twice.
  if (status === "error" && error) return `Couldn't save ${error.field}`;
  if (status === "dirty") return `Unsaved changes in ${dirtyCards.join(", ")}`;
  return "All changes saved";
}
