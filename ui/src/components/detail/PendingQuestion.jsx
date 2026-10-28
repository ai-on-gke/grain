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
import { Button, TextField } from "@mui/material";
import api from "../../api.js";
import fileToAttachment from "../../attachments.js";
import { linkTaskRefs, taskRefFromHref } from "../../taskRefs.js";
import AttachmentLinks from "../AttachmentLinks.jsx";
import AttachmentPicker, {
  getPastedImages,
  getDroppedFiles,
} from "../AttachmentPicker.jsx";
import Markdown from "../Markdown.jsx";

// PendingQuestion puts the question a parked run asked, and the reply box
// that answers it, at the top of the task pane -- ahead of the task's
// original description and the timeline below -- so a reader opening a
// task in awaiting_reply sees what is being asked and can answer at once.
//
// An interactive task has its own chat view (TaskChat), and a task parked
// on request_secret has PendingSecret's write-only box instead.
export default function PendingQuestion({ t, act, onOpenTask }) {
  const textareaRef = useRef(null);
  const [attachments, setAttachments] = useState([]);
  const sending = useRef(false);
  const prevTaskId = useRef(t.id);

  useEffect(() => {
    if (prevTaskId.current !== t.id) {
      prevTaskId.current = t.id;
      if (textareaRef.current) {
        textareaRef.current.value = "";
      }
      setAttachments([]);
    }
  }, [t.id]);

  const comments = t.comments || [];
  const question = comments[comments.length - 1];
  if (
    t.state !== "awaiting_reply" ||
    t.interactive ||
    t.pendingSecret ||
    !question ||
    (!question.body &&
      !(question.attachments && question.attachments.length > 0))
  ) {
    return null;
  }

  const send = async () => {
    if (sending.current) return;
    const body = textareaRef.current?.value || "";
    if (!body.trim() && attachments.length === 0) return;
    sending.current = true;
    try {
      const uploaded = await Promise.all(
        attachments.map((f) => fileToAttachment(f)),
      );
      const files = attachments;
      if (textareaRef.current) {
        textareaRef.current.value = "";
      }
      setAttachments([]);
      const posted = await act(
        () =>
          api(`/api/tasks/${t.id}/comments`, {
            method: "POST",
            body: JSON.stringify({
              body,
              attachments: uploaded,
            }),
          }),
        t.id,
      );
      if (!posted) {
        if (textareaRef.current && !textareaRef.current.value.trim()) {
          textareaRef.current.value = body;
        }
        setAttachments((cur) => (cur.length ? cur : files));
      }
    } finally {
      sending.current = false;
    }
  };

  return (
    <div
      className="pending-question"
      data-testid="pending-question"
      onClick={(ev) => {
        if (!onOpenTask) return;
        if (
          ev.button !== 0 ||
          ev.metaKey ||
          ev.ctrlKey ||
          ev.shiftKey ||
          ev.altKey
        ) {
          return;
        }
        const id = taskRefFromHref(
          ev.target.closest?.("a")?.getAttribute("href"),
        );
        if (id) {
          ev.preventDefault();
          ev.stopPropagation();
          onOpenTask(id);
        }
      }}
    >
      <div className="pending-question-header">
        <strong>Waiting for your reply</strong>
      </div>
      {(question.summary || t.questionSummary) && (
        <p className="question-summary-line">
          <strong>{question.summary || t.questionSummary}</strong>
        </p>
      )}
      {question.body && (
        <Markdown className="pending-question-body">
          {question.authorKind === "automation"
            ? linkTaskRefs(question.body)
            : question.body}
        </Markdown>
      )}
      <AttachmentLinks taskId={t.id} attachments={question.attachments} />
      <div
        className="pending-question-form"
        onPaste={(evt) => {
          const files = getPastedImages(evt);
          if (files.length > 0) {
            evt.preventDefault();
            setAttachments((current) => [...current, ...files]);
          }
        }}
        onDrop={(evt) => {
          const dropped = getDroppedFiles(evt);
          if (dropped.length > 0) {
            evt.preventDefault();
            setAttachments((current) => [...current, ...dropped]);
          }
        }}
        onDragOver={(evt) => {
          evt.preventDefault();
        }}
      >
        <TextField
          multiline
          rows={2}
          placeholder="Write a reply…"
          inputRef={textareaRef}
          inputProps={{ "aria-label": `Reply to ${t.id}` }}
          fullWidth
          size="small"
        />
        <AttachmentPicker files={attachments} onChange={setAttachments} />
        <Button variant="contained" onClick={send}>
          Send
        </Button>
      </div>
    </div>
  );
}
