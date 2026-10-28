import { Alert, Portal, Snackbar } from "@mui/material";

// Portalled to document.body rather than rendered where App mounts it.
// Most of the errors this reports come from inside a modal -- the New
// task panel, a task pane -- and an open MUI modal marks every other
// child of <body> aria-hidden, #root included. Rendered in place, the
// alert was drawn above the modal (the snackbar z-index below) but sat
// inside that aria-hidden #root, so a screen reader never heard it and
// the alert role was not in the accessibility tree at all. A portal
// mounted after the modal opened is a sibling the modal did not hide.
//
// The message scrolls inside a box at most 40% of the window tall. A
// refused template delete names every suite and schedule in the way, and
// with 25 of them (2026-10-07 standing-work walkthrough) the alert grew
// taller than a 375 px phone's screen: its first line, the one saying
// what was refused, sat above the top edge, and the alert covered every
// control on the page until it went away.
export default function ErrorBanner({ message }) {
  return (
    <Portal>
      <Snackbar
        open
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
        sx={{ zIndex: (theme) => theme.zIndex.snackbar }}
      >
        <Alert
          severity="error"
          variant="filled"
          sx={{
            maxWidth: "min(90vw, 480px)",
            "& .MuiAlert-message": { maxHeight: "40vh", overflowY: "auto" },
          }}
        >
          {message}
        </Alert>
      </Snackbar>
    </Portal>
  );
}
