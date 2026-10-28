import { useState } from "react";
import { Button, Chip, Stack, TextField, Typography } from "@mui/material";
import api from "../api.js";
import SecretFileButton, { loadedFileNote } from "./SecretFileButton.jsx";

// grain/task-110: one secret, set where it is used rather than on a pane
// of its own. A capability reports which credentials it resolves and
// where each one is written (ui.CapabilitySecret), so a missing secret is
// filled in beside the capability that is missing it -- the Secrets tab
// this replaces could set the same value, but only for someone who
// already knew the exact secret and key names, which nothing on it said.
//
// Write-only, like every other control built on the secrets store: a
// value goes in, presence comes back, and no value is ever read out --
// which is why a set secret still shows an empty box (with a placeholder
// saying what typing in it would do) rather than a masked value.
//
// The buttons are all type="button" and Enter is swallowed into "set
// this one": this may render inside a pane whose own <form> saves
// deployment settings, and a stray
// submit would save those from a field that has nothing to do with them.
//
// Upload (grain/task-160, SecretFileButton) fills the same box from a
// file, for the credentials that arrive as one -- github-app/private-key
// above all, whose downloaded .pem does not survive being pasted through
// a single-line box reliably. It loads, it does not store: `loaded` is
// only what the helper text says the box now holds, and Set is still the
// press that writes it.
export default function SecretField({ secret, showError, onChanged }) {
  const [value, setValue] = useState("");
  const [loaded, setLoaded] = useState(null);
  const path = `/api/secrets/${encodeURIComponent(secret.secret)}/${encodeURIComponent(secret.key)}`;

  // onChanged is what refreshes the "set"/"not set" this renders from:
  // the mutation's own reply is the whole secrets listing, not this
  // capability's readiness, so the pane above re-reads settings rather
  // than this trying to patch a status it does not own.
  const setSecret = async () => {
    try {
      await api(path, { method: "PUT", body: JSON.stringify({ value }) });
      setValue("");
      setLoaded(null);
      await onChanged();
    } catch (err) {
      showError(err);
    }
  };

  // Deletes the one key, not the whole secret: a secret is a bag of keys
  // here, and github-app holds two of them for the same capability --
  // clearing the app id has no business taking the private key with it.
  // A secret whose last key goes stops being listed at all, so the
  // "exists but resolves to nothing" state a bare-named credential would
  // otherwise be left in does not arise.
  //
  // Confirmed first, like every other control here that throws something
  // away: a write-only value cannot be read back, so the only way to undo
  // a stray press is to find the credential again and paste it.
  const clearSecret = async () => {
    if (
      !window.confirm(
        `Clear ${secret.secret}/${secret.key}? The stored value cannot be read back, so putting it back means pasting it again.`,
      )
    )
      return;
    try {
      await api(path, { method: "DELETE" });
      await onChanged();
    } catch (err) {
      showError(err);
    }
  };

  return (
    <Stack direction="row" spacing={1} alignItems="flex-start" sx={{ mt: 1 }}>
      <TextField
        type="password"
        size="small"
        fullWidth
        autoComplete="off"
        label={secret.name}
        placeholder={
          secret.set
            ? "replace the stored value"
            : "paste a value, or upload the file it came in"
        }
        helperText={
          loaded
            ? loadedFileNote(loaded)
            : `stored as ${secret.secret}/${secret.key} -- write-only, never shown or read back`
        }
        InputLabelProps={{ shrink: true }}
        inputProps={{ "aria-label": secret.name }}
        value={value}
        onChange={(evt) => {
          setValue(evt.target.value);
          setLoaded(null);
        }}
        onKeyDown={(evt) => {
          if (evt.key === "Enter") {
            evt.preventDefault();
            if (value.trim() !== "") setSecret();
          }
        }}
      />
      <Chip
        size="small"
        label={secret.set ? "set" : "not set"}
        color={secret.set ? "success" : "default"}
        sx={{ mt: 1 }}
      />
      <SecretFileButton
        name={secret.name}
        showError={showError}
        onLoad={(text, file) => {
          setValue(text);
          setLoaded(file);
        }}
        sx={{ mt: 0.5 }}
      />
      <Button
        type="button"
        variant="outlined"
        size="small"
        disabled={value.trim() === ""}
        onClick={setSecret}
        sx={{ mt: 0.5 }}
      >
        Set
      </Button>
      <Button
        type="button"
        variant="text"
        size="small"
        disabled={!secret.set}
        onClick={clearSecret}
        sx={{ mt: 0.5 }}
      >
        Clear
      </Button>
    </Stack>
  );
}

// SecretFields is every secret one capability resolves, under a heading
// that says what they are for -- rendered by CapabilitiesPanel, and
// nothing at all for a capability that needs none (self-debug) or for a
// UI with no secrets store to write to, which reports none.
export function SecretFields({ secrets, showError, onChanged }) {
  const list = secrets || [];
  if (list.length === 0) return null;
  return (
    <>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
        {list.length === 1
          ? "Credential this needs:"
          : "Credentials this needs:"}
      </Typography>
      {list.map((s) => (
        <SecretField
          key={s.name}
          secret={s}
          showError={showError}
          onChanged={onChanged}
        />
      ))}
    </>
  );
}
