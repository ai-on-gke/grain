import { useState } from "react";
import CloseIcon from "@mui/icons-material/Close";
import { Box, Chip, Dialog, IconButton, Link, Typography } from "@mui/material";

// Where an attachment's bytes actually live: GET
// /api/tasks/{taskId}/attachments/{id} is the only place they are served
// (handleGetAttachment). It serves raster images and plain text inline
// with their own content type, so a browser renders an image there rather
// than saving it; anything else (HTML, SVG, archives) is forced to an
// application/octet-stream download, since the type is uploader-chosen
// and rendering it in grain's own origin would be stored XSS (#2374).
const attachmentURL = (taskId, a) => `/api/tasks/${taskId}/attachments/${a.id}`;

// Is this one of the attachments the viewer below can show? The content
// type is the uploader's own (fileToAttachment reads File.type), so it
// is whatever the browser that picked the file said -- a file picked
// with no type at all arrives as application/octet-stream and is treated
// as a plain download, which is the honest answer for bytes nobody can
// name.
// The image types handleGetAttachment serves inline (its
// inlineAttachmentTypes), and so the only ones the viewer below can
// draw. Any other image/* -- SVG above all, a document that can carry
// script -- arrives as an octet-stream download, which an <img> shows as
// a broken picture, so those get the plain link like any other file.
const viewableImageTypes = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/bmp",
]);

const isImage = (a) =>
  viewableImageTypes.has(
    (a.contentType || "").split(";")[0].trim().toLowerCase(),
  );

// ImageView is the full-screen look at one attached image, and it exists
// for one reason: an image opened as a link had no way back
// (grain/task-78). grain is meant to be added to an iOS home screen
// (index.html's apple-mobile-web-app-capable), and in that window there
// is no address bar and no back button -- following a link to a raw
// image replaced the whole app with the image and left the person
// holding the phone stuck there until they killed the app. So an image
// is shown *over* the UI instead, by a Dialog that closes on Escape, on
// a click anywhere off the picture, and on the X in its corner. Nothing
// here navigates.
//
// The X is the affordance the complaint asked for by name, so it is
// drawn over the image rather than in a bar of its own: the picture gets
// the whole window, which on a phone is the only way a screenshot is
// legible at all.
function ImageView({ src, filename, onClose }) {
  return (
    <Dialog
      open
      onClose={onClose}
      fullScreen
      // Dark ground under the picture, not the paper's usual surface: an
      // image with transparency or a pale border reads against it in
      // either theme, and it is the shape every other full-screen image
      // viewer has trained people to expect.
      PaperProps={{
        sx: {
          bgcolor: "rgba(0, 0, 0, 0.92)",
          backgroundImage: "none",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        },
      }}
    >
      {/* Everything around the picture is a way out: a click that lands
          on the ground closes, the same as a click on a centered
          Dialog's backdrop would. The picture itself swallows its own
          clicks so that reaching out to look at it is not a dismissal. */}
      <Box
        onClick={onClose}
        sx={{
          position: "absolute",
          inset: 0,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          p: 2,
        }}
      >
        <Box
          component="img"
          src={src}
          alt={filename}
          onClick={(evt) => evt.stopPropagation()}
          sx={{ maxWidth: "100%", maxHeight: "100%", objectFit: "contain" }}
        />
      </Box>
      {/* Above the image in the stack, and inset by the safe area, so the
          way out never lands under a notched phone's status bar -- the
          same allowance Overlay's own X makes. */}
      <IconButton
        aria-label="Close image"
        onClick={onClose}
        sx={{
          position: "absolute",
          top: "calc(8px + env(safe-area-inset-top, 0px))",
          right: 8,
          color: "common.white",
          bgcolor: "rgba(0, 0, 0, 0.5)",
          "&:hover": { bgcolor: "rgba(0, 0, 0, 0.7)" },
        }}
      >
        <CloseIcon />
      </IconButton>
      {/* Which file this is, and the way to keep it. The link downloads
          rather than navigating (same-origin, so the attribute is
          honoured) -- following it to the image's own URL is exactly the
          dead end this viewer replaced. */}
      <Box
        sx={{
          position: "absolute",
          left: 8,
          bottom: "calc(8px + env(safe-area-inset-bottom, 0px))",
          maxWidth: "calc(100% - 64px)",
        }}
      >
        <Typography variant="caption" sx={{ color: "common.white" }} noWrap>
          {filename}
          {" · "}
          <Link
            href={src}
            download={filename}
            sx={{ color: "common.white" }}
            underline="always"
          >
            Download
          </Link>
        </Typography>
      </Box>
    </Dialog>
  );
}

// AttachmentLinks renders the files already attached to a task or a
// comment (ui.Attachment's own {id, filename, contentType, size} shape,
// bwsalmon/agents#522) as a row of clickable chips.
//
// An image opens in ImageView above, over the UI and with an X to leave
// by. Everything else is still a link straight at
// GET /api/tasks/{taskId}/attachments/{id} in a new tab -- a zip or a
// PDF is something a browser hands to a download or to its own reader
// rather than something this could draw, and there is nothing for this
// component to fetch ahead of a click either way.
export default function AttachmentLinks({ taskId, attachments }) {
  // The attachment being looked at, or null. One at a time: the viewer
  // is the whole window.
  const [viewing, setViewing] = useState(null);

  if (!attachments || attachments.length === 0) return null;
  return (
    <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5, mt: 0.5 }}>
      {attachments.map((a) =>
        isImage(a) ? (
          <Chip
            key={a.id}
            size="small"
            clickable
            onClick={() => setViewing(a)}
            label={a.filename}
          />
        ) : (
          <Chip
            key={a.id}
            size="small"
            component="a"
            href={attachmentURL(taskId, a)}
            target="_blank"
            rel="noopener noreferrer"
            clickable
            label={a.filename}
          />
        ),
      )}
      {viewing && (
        <ImageView
          src={attachmentURL(taskId, viewing)}
          filename={viewing.filename}
          onClose={() => setViewing(null)}
        />
      )}
    </Box>
  );
}
