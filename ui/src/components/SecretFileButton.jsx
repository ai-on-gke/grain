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

import { useRef } from "react";
import UploadFileIcon from "@mui/icons-material/UploadFile";
import { Button, Tooltip } from "@mui/material";
import readSecretFile from "../secretFile.js";

// SecretFileButton is "load this credential from the file it arrived in"
// beside a write-only secret box (grain/task-160): a button, a hidden
// <input type="file"> it clicks through a ref, and onLoad(text, file)
// with the file's contents exactly as they were on disk.
//
// It fills the box rather than storing anything. The credential still
// goes through the same PUT the typed value does, on the same Set press,
// so there is one write path to get wrong and picking the wrong file is
// still recoverable by picking another -- and the "set"/"not set" chip
// beside it keeps meaning what it meant. See secretFile.js for why a
// pasted PEM is worth avoiding in the first place.
//
// The button carries the whole accessible name, and it names the
// credential: a capability with two of these (github-app's id and its
// private key) renders two, and "Upload" alone would leave a screen
// reader -- and a test -- unable to say which box a press belongs to.
// The input behind it is hidden plumbing, clicked through the ref rather
// than by the user, the same shape AttachmentPicker already uses.
export default function SecretFileButton({ name, onLoad, showError, sx }) {
  const inputRef = useRef(null);

  const pick = async (evt) => {
    const file = (evt.target.files || [])[0];
    // Lets the same file be picked twice in a row -- a browser treats an
    // unchanged <input> value as no change at all and fires nothing the
    // second time, which matters here because "pick it again" is exactly
    // what someone does after a failed write.
    evt.target.value = "";
    if (!file) return;
    try {
      onLoad(await readSecretFile(file), file);
    } catch (err) {
      showError(err);
    }
  };

  return (
    <>
      <input ref={inputRef} type="file" hidden onChange={pick} />
      <Tooltip
        title={`Load ${name} from a file -- a .pem key file's line breaks survive intact, which pasting one often does not`}
      >
        <Button
          type="button"
          variant="outlined"
          size="small"
          aria-label={`Upload a file for ${name}`}
          startIcon={<UploadFileIcon fontSize="small" />}
          onClick={() => inputRef.current.click()}
          sx={sx}
        >
          Upload
        </Button>
      </Tooltip>
    </>
  );
}

// loadedFileNote is the one line both boxes show once a file is loaded
// and before it is stored -- what was read, how big it was, and that
// nothing has happened yet. The size is the file's own rather than the
// text's: it is what the operator can check against the file on disk.
// action is the label on the button that would store it, which is not
// the same word in both places.
export function loadedFileNote(loaded, action = "Set") {
  return `${loaded.name} loaded (${loaded.size} bytes) -- not stored yet, press ${action}`;
}
