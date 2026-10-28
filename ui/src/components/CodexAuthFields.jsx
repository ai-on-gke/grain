import { useEffect, useState } from "react";
import { Alert, Button, Stack, TextField, Typography } from "@mui/material";
import api from "../api.js";
import SecretFileButton, { loadedFileNote } from "./SecretFileButton.jsx";

export default function CodexAuthFields({
  backend,
  onCredentialChange,
  showError,
}) {
  const [status, setStatus] = useState(null);
  const [value, setValue] = useState("");
  const [loaded, setLoaded] = useState(null);
  const [busy, setBusy] = useState(false);
  const url = `/api/agent-backends/${encodeURIComponent(backend.id)}/codex-auth`;

  useEffect(() => {
    let active = true;
    (async () => {
      try {
        const result = await api(url);
        if (active) setStatus(result);
      } catch (err) {
        if (active) showError(err);
      }
    })();
    return () => {
      active = false;
    };
  }, [url, showError]);

  const save = async (method) => {
    setBusy(true);
    try {
      const result = await api(url, {
        method,
        ...(method === "PUT" ? { body: JSON.stringify({ value }) } : {}),
      });
      setStatus(result);
      setValue("");
      setLoaded(null);
      onCredentialChange?.();
    } catch (err) {
      showError(err);
    } finally {
      setBusy(false);
    }
  };

  // Grain renews the login itself; when the authority refuses it for good
  // the file stays, but only a new upload makes it work again.
  const refreshError = status?.set
    ? status.refreshError
    : status?.sharedSet
      ? status.sharedRefreshError
      : null;

  return (
    <Stack spacing={1} sx={{ mt: 2 }}>
      <Typography variant="body2" role="status">
        {!status
          ? "Checking subscription login…"
          : status.set
            ? "Subscription login configured for this backend."
            : status.sharedSet
              ? "Using the existing shared subscription login. Upload a file to give this backend its own account."
              : "No subscription login configured."}
      </Typography>
      {refreshError && (
        <Alert severity="error" role="alert">
          {refreshError}
        </Alert>
      )}
      <Typography variant="body2" color="text.secondary">
        Upload auth.json from your Codex login. Grain keeps refreshed tokens for
        this backend. API keys are ignored while subscription authentication is
        selected.
      </Typography>
      <Stack
        direction="row"
        spacing={1}
        useFlexGap
        flexWrap="wrap"
        alignItems="center"
      >
        <TextField
          type="password"
          size="small"
          label="Codex auth.json"
          autoComplete="new-password"
          value={value}
          disabled={busy || status?.enabled === false}
          onChange={(event) => {
            setValue(event.target.value);
            setLoaded(null);
          }}
        />
        <SecretFileButton
          name="Codex auth.json"
          onLoad={(text, file) => {
            setValue(text);
            setLoaded(file);
          }}
          showError={showError}
        />
        <Button
          type="button"
          disabled={busy || !value.trim() || !status?.enabled}
          onClick={() => save("PUT")}
        >
          Save login
        </Button>
        <Button
          type="button"
          disabled={busy || !status?.set}
          onClick={() => save("DELETE")}
        >
          Clear backend login
        </Button>
      </Stack>
      {loaded && (
        <Typography variant="caption">
          {loadedFileNote(loaded, "Save login")}
        </Typography>
      )}
    </Stack>
  );
}
