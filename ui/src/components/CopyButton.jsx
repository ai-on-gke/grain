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

import { useEffect, useRef, useState } from "react";
import CheckIcon from "@mui/icons-material/Check";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import { IconButton, Tooltip } from "@mui/material";

// CopyButton copies text to the clipboard with visual confirmation
// (icon and tooltip toggle to "Copied" for two seconds).
export default function CopyButton({
  text,
  label = "Copy",
  size = "small",
  className,
  sx,
}) {
  const [copied, setCopied] = useState(false);
  const timerRef = useRef(null);

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  const handleCopy = async (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (!text) return;

    const copyFallback = () => {
      const textarea = document.createElement("textarea");
      textarea.value = text;
      textarea.style.position = "fixed";
      textarea.style.opacity = "0";
      document.body.appendChild(textarea);
      textarea.select();
      try {
        document.execCommand("copy");
      } finally {
        document.body.removeChild(textarea);
      }
    };

    let copiedOk = false;
    if (typeof navigator !== "undefined" && navigator?.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(text);
        copiedOk = true;
      } catch {
        // Fallback to execCommand if clipboard writeText rejects (e.g. permission/iframe restrictions)
        try {
          copyFallback();
          copiedOk = true;
        } catch (err) {
          console.error("Failed to copy text via fallback:", err);
        }
      }
    } else {
      try {
        copyFallback();
        copiedOk = true;
      } catch (err) {
        console.error("Failed to copy text:", err);
      }
    }

    if (copiedOk) {
      setCopied(true);
      if (timerRef.current) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => {
        setCopied(false);
      }, 2000);
    }
  };

  const title = copied ? "Copied" : label;

  return (
    <Tooltip title={title}>
      <IconButton
        type="button"
        size={size}
        aria-label={title}
        onClick={handleCopy}
        className={className ? `copy-button ${className}` : "copy-button"}
        sx={{
          color: copied
            ? "var(--accent, #6366f1)"
            : "var(--text-tertiary, #888)",
          minWidth: "auto",
          "@media (pointer: coarse)": {
            minWidth: "44px",
            minHeight: "44px",
          },
          "&:hover": {
            color: "var(--text, #111)",
            background: "var(--surface-hover)",
          },
          ...sx,
        }}
      >
        {copied ? (
          <CheckIcon sx={{ fontSize: "0.95rem" }} />
        ) : (
          <ContentCopyIcon sx={{ fontSize: "0.95rem" }} />
        )}
      </IconButton>
    </Tooltip>
  );
}
