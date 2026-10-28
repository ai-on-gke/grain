import { useState } from "react";
import { Alert, Button } from "@mui/material";

// A save the server refused because the schedule, suite or template it
// was editing has been saved by somebody else since this pane loaded it
// (ui.staleEditError: a 409 coded "stale"). Every one of these panes
// PATCHes its whole form, so before that refusal existed the second of
// two tabs to save silently put back everything the first had changed
// (the 2026-10-13 recurring/composed walkthrough, step 2).
export function isStaleEdit(err) {
  return err?.status === 409 && err?.code === "stale";
}

// StaleEditNotice says so inside the pane, above the form, rather than
// in the page's error banner: the pane and everything typed into it stay
// exactly where they are, so whoever is looking can copy out what they
// meant to change before taking Reload, which throws it away for the
// saved copy.
export function StaleEditNotice({ error, onReload }) {
  if (!error) return null;
  return (
    <Alert
      severity="warning"
      role="alert"
      sx={{ mt: 1, mb: 1 }}
      action={
        <Button color="inherit" size="small" onClick={onReload}>
          Reload
        </Button>
      }
    >
      {error.message} Your changes here were not saved; Reload replaces them
      with the saved copy.
    </Alert>
  );
}

// withReload wraps an editing pane so that it can start over from the
// copy the page now holds: onReload refreshes the page's list (the
// pane's own onSaved) and then remounts the pane, since every pane here
// seeds its state from its props once, when it opens.
export function withReload(Pane) {
  return function Reloadable(props) {
    const [generation, setGeneration] = useState(0);
    const reload = async () => {
      try {
        await props.onSaved?.();
      } catch (err) {
        props.showError?.(err);
        return;
      }
      setGeneration((g) => g + 1);
    };
    return <Pane key={generation} {...props} onReload={reload} />;
  };
}
