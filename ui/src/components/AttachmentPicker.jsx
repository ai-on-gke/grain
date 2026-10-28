import { useRef } from "react";
import AttachFileIcon from "@mui/icons-material/AttachFile";
import { Box, Chip, IconButton, Tooltip } from "@mui/material";

// AttachmentPicker is the file-attach control NewTaskOverlay and the
// Timeline's own reply box both need (bwsalmon/agents#522): a paperclip
// button that opens the browser's file picker, a chip per file already
// chosen (with its own remove button), and onChange(files) -- plain File
// objects, not yet read -- so a caller decides when to actually convert
// them (on submit, via fileToAttachment) rather than paying to read one
// that might still be removed before Send is ever clicked.
//
// variant="icon" is that same paperclip tucked into the bottom-right
// corner of the text box the files belong to, rather than sitting on a
// row of its own beneath it (grain/task-85): children is that box, and
// the clip is positioned against it, which buys the description field
// back the line the control used to take. The chips still sit below the
// box, where a picked file is out of the way of what is being typed.
//
// getPastedFiles (and its alias getPastedImages) extracts File objects from
// a clipboard event, if any. Used by AttachmentPicker, TaskChat, and comment
// composers so pasting files (images, logs, text, archives) into a task description
// or task chat attaches them as if picked with the clip.
export function getPastedFiles(evt) {
  const clipboardData = evt?.clipboardData || evt?.nativeEvent?.clipboardData;
  if (!clipboardData) return [];

  const files = [];
  if (clipboardData.files && clipboardData.files.length > 0) {
    for (let i = 0; i < clipboardData.files.length; i++) {
      const file = clipboardData.files[i];
      if (file) {
        files.push(file);
      }
    }
    return files;
  }

  if (clipboardData.items) {
    for (let i = 0; i < clipboardData.items.length; i++) {
      const item = clipboardData.items[i];
      const file =
        typeof item.getAsFile === "function" ? item.getAsFile() : null;
      if (file) {
        files.push(file);
      }
    }
  }

  return files;
}

export const getPastedImages = getPastedFiles;

// getDroppedFiles extracts File objects from a drag/drop event, if any.
export function getDroppedFiles(evt) {
  const dataTransfer = evt?.dataTransfer || evt?.nativeEvent?.dataTransfer;
  if (!dataTransfer) return [];

  const files = [];
  if (dataTransfer.files && dataTransfer.files.length > 0) {
    for (let i = 0; i < dataTransfer.files.length; i++) {
      const file = dataTransfer.files[i];
      if (file) {
        files.push(file);
      }
    }
    return files;
  }

  if (dataTransfer.items) {
    for (let i = 0; i < dataTransfer.items.length; i++) {
      const item = dataTransfer.items[i];
      const file =
        typeof item.getAsFile === "function" ? item.getAsFile() : null;
      if (file) {
        files.push(file);
      }
    }
  }

  return files;
}

export function handleFilePaste(evt, files, onChange) {
  const pasted = getPastedFiles(evt);
  if (pasted.length > 0) {
    evt.preventDefault();
    if (typeof onChange === "function") {
      onChange(
        typeof files === "function"
          ? files(pasted)
          : [...(files || []), ...pasted],
      );
    }
    return true;
  }
  return false;
}

export const handleImagePaste = handleFilePaste;

export function handleFileDrop(evt, files, onChange) {
  const dropped = getDroppedFiles(evt);
  if (dropped.length > 0) {
    evt.preventDefault();
    if (typeof onChange === "function") {
      onChange(
        typeof files === "function"
          ? files(dropped)
          : [...(files || []), ...dropped],
      );
    }
    return true;
  }
  return false;
}

export default function AttachmentPicker({
  files,
  onChange,
  variant = "button",
  children,
}) {
  const inputRef = useRef(null);

  const handlePaste = (evt) => {
    handleFilePaste(evt, files, onChange);
  };

  const handleDrop = (evt) => {
    handleFileDrop(evt, files, onChange);
  };

  const handleDragOver = (evt) => {
    evt.preventDefault();
  };

  const add = (evt) => {
    const picked = Array.from(evt.target.files || []);
    onChange([...files, ...picked]);
    // Lets the same file be picked again after being removed below --
    // otherwise a browser treats an unchanged <input> value as no change
    // at all and never fires onChange a second time for it.
    evt.target.value = "";
  };
  const remove = (index) => onChange(files.filter((_, i) => i !== index));

  // The paperclip carries the control's whole accessible name: the
  // input behind it is hidden plumbing, clicked through the ref rather
  // than by the user, so labelling both would just give "Attach files"
  // two elements to mean.
  const input = (
    <input ref={inputRef} type="file" multiple hidden onChange={add} />
  );
  const clip = (
    <Tooltip title="Attach files">
      <IconButton
        size="small"
        aria-label="Attach files"
        onClick={() => inputRef.current.click()}
        sx={
          variant === "icon"
            ? { position: "absolute", right: 6, bottom: 6 }
            : undefined
        }
      >
        <AttachFileIcon fontSize="small" />
      </IconButton>
    </Tooltip>
  );
  const chips = files.length > 0 && (
    <Box sx={{ display: "flex", flexWrap: "wrap", gap: 0.5, mt: 0.5 }}>
      {files.map((f, i) => (
        <Chip
          key={`${f.name}-${i}`}
          size="small"
          label={f.name}
          onDelete={() => remove(i)}
        />
      ))}
    </Box>
  );

  if (variant === "icon") {
    return (
      <Box
        sx={{ mt: 2, mb: 1 }}
        onPaste={handlePaste}
        onDrop={handleDrop}
        onDragOver={handleDragOver}
      >
        <Box sx={{ position: "relative" }}>
          {children}
          {input}
          {clip}
        </Box>
        {chips}
      </Box>
    );
  }

  return (
    <Box
      onPaste={handlePaste}
      onDrop={handleDrop}
      onDragOver={handleDragOver}
      sx={{
        display: "inline-flex",
        alignItems: "center",
        gap: 0.5,
        flexWrap: "wrap",
      }}
    >
      {input}
      {clip}
      {chips}
    </Box>
  );
}
