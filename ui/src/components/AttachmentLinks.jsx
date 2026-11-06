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

import { useState } from "react";
import ChevronLeftIcon from "@mui/icons-material/ChevronLeft";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import CloseIcon from "@mui/icons-material/Close";
import {
  Box,
  ButtonBase,
  Chip,
  Dialog,
  IconButton,
  Link,
  Typography,
} from "@mui/material";

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

// The previous and next arrows at the viewer's sides.
const navButtonSx = {
  position: "absolute",
  top: "50%",
  transform: "translateY(-50%)",
  color: "common.white",
  bgcolor: "rgba(0, 0, 0, 0.5)",
  "&:hover": { bgcolor: "rgba(0, 0, 0, 0.7)" },
};

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
//
// With several images -- a run's before and after contact sheets, its
// screenshots beside the Figma frame they implement -- the viewer pages
// through all of them: the arrows at its sides and the keyboard's left and
// right step to the previous and next, wrapping round, and a counter says
// where in the set this one is.
function ImageView({ images, index, onIndex, onClose }) {
  const { src, filename } = images[index];
  const many = images.length > 1;
  const step = (delta) =>
    onIndex((index + delta + images.length) % images.length);
  const onKeyDown = (evt) => {
    if (!many) return;
    if (evt.key === "ArrowLeft") {
      evt.preventDefault();
      step(-1);
    } else if (evt.key === "ArrowRight") {
      evt.preventDefault();
      step(1);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      onKeyDown={onKeyDown}
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
      {many && (
        <>
          <IconButton
            aria-label="Previous image"
            onClick={() => step(-1)}
            sx={{ ...navButtonSx, left: 8 }}
          >
            <ChevronLeftIcon />
          </IconButton>
          <IconButton
            aria-label="Next image"
            onClick={() => step(1)}
            sx={{ ...navButtonSx, right: 8 }}
          >
            <ChevronRightIcon />
          </IconButton>
        </>
      )}
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
          {many && `${index + 1} / ${images.length} · `}
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

// Thumbnail is an attached image drawn small, in line with the note it
// came with, so a run's screenshots are seen where they are reported
// rather than behind a file name. It is a button, never a link: a click
// opens ImageView over the UI (grain/task-78). The picture is decorative
// -- alt="" -- since the file name under it is the button's label, and
// the one <img> named after the file stays the viewer's own.
function Thumbnail({ src, filename, onOpen }) {
  return (
    <ButtonBase
      onClick={onOpen}
      focusRipple
      sx={{
        display: "flex",
        flexDirection: "column",
        alignItems: "stretch",
        width: { xs: 140, sm: 180 },
        borderRadius: 1,
        overflow: "hidden",
        border: 1,
        borderColor: "divider",
        bgcolor: "action.hover",
        textAlign: "left",
      }}
    >
      <Box
        component="img"
        src={src}
        alt=""
        loading="lazy"
        sx={{
          display: "block",
          width: "100%",
          height: { xs: 90, sm: 110 },
          objectFit: "cover",
          objectPosition: "top",
          bgcolor: "background.default",
        }}
      />
      <Typography
        variant="caption"
        noWrap
        sx={{ px: 0.75, py: 0.25, display: "block" }}
      >
        {filename}
      </Typography>
    </ButtonBase>
  );
}

// AttachmentLinks renders the files already attached to a task or a
// comment (ui.Attachment's own {id, filename, contentType, size} shape,
// bwsalmon/agents#522).
//
// Images come first, as thumbnails that open in ImageView above, over the
// UI and with an X to leave by, paging through every image of the set.
// Everything else is a row of chips, each a link straight at
// GET /api/tasks/{taskId}/attachments/{id} in a new tab -- a zip or a
// PDF is something a browser hands to a download or to its own reader
// rather than something this could draw, and there is nothing for this
// component to fetch ahead of a click either way.
export default function AttachmentLinks({ taskId, attachments }) {
  // The index into images of the one being looked at, or null. One at a
  // time: the viewer is the whole window.
  const [viewing, setViewing] = useState(null);

  if (!attachments || attachments.length === 0) return null;
  const images = attachments.filter(isImage).map((a) => ({
    id: a.id,
    src: attachmentURL(taskId, a),
    filename: a.filename,
  }));
  const files = attachments.filter((a) => !isImage(a));
  return (
    <Box sx={{ mt: 0.5 }}>
      {images.length > 0 && (
        <Box
          sx={{ display: "flex", flexWrap: "wrap", gap: 1 }}
          data-testid="attachment-thumbnails"
        >
          {images.map((img, i) => (
            <Thumbnail
              key={img.id}
              src={img.src}
              filename={img.filename}
              onOpen={() => setViewing(i)}
            />
          ))}
        </Box>
      )}
      {files.length > 0 && (
        <Box
          sx={{
            display: "flex",
            flexWrap: "wrap",
            gap: 0.5,
            mt: images.length > 0 ? 0.5 : 0,
          }}
        >
          {files.map((a) => (
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
          ))}
        </Box>
      )}
      {viewing !== null && images[viewing] && (
        <ImageView
          images={images}
          index={viewing}
          onIndex={setViewing}
          onClose={() => setViewing(null)}
        />
      )}
    </Box>
  );
}
