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

import { useEffect, useId, useState } from "react";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import AttachmentLinks from "./AttachmentLinks.jsx";
import Markdown from "./Markdown.jsx";

// questionSummaryOf is the one line a question is shown by where there is
// room for no more of it (pkg/model's Comment.Summary): the summary its
// asker gave, the task's own (Task.QuestionSummary, which the daemon
// derives for a question stored without one), or failing both the
// question's first line.
export function questionSummaryOf(question, t) {
  if (question?.summary) return question.summary;
  if (t?.questionSummary) return t.questionSummary;
  const first = (question?.body || "")
    .split("\n")
    .map((l) => l.replace(/^[\s#>*+-]+/, "").trim())
    .find((l) => l !== "");
  return first || "";
}

// QuestionDisclosure is a question shown by its summary alone, with the
// full question -- markdown and attachments -- one click away in place:
// what the inbox shows of every parked task, so a page of them reads as
// a list of decisions rather than a wall of reasoning.
//
// The toggle is a real button carrying aria-expanded and aria-controls,
// so it is reachable and operable from the keyboard and a screen reader
// says whether the question is showing. Collapsed by default, and
// collapsed again when the row it hangs off becomes another task's.
export default function QuestionDisclosure({
  t,
  question,
  renderedBody,
  defaultExpanded = false,
}) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const bodyId = useId();
  useEffect(() => {
    setExpanded(defaultExpanded);
  }, [t.id, defaultExpanded]);
  const summary = questionSummaryOf(question, t);
  const hasAttachments = (question?.attachments || []).length > 0;
  const hasMore = !!question?.body || hasAttachments;
  return (
    <div className="question-disclosure">
      <button
        type="button"
        className="question-disclosure-toggle"
        aria-expanded={expanded}
        aria-controls={bodyId}
        disabled={!hasMore}
        onClick={(ev) => {
          ev.stopPropagation();
          setExpanded((v) => !v);
        }}
      >
        <ExpandMoreIcon
          fontSize="small"
          className="question-disclosure-chevron"
          aria-hidden="true"
        />
        <span className="question-disclosure-summary">{summary}</span>
        <span className="question-disclosure-hint">
          {expanded ? "Hide question" : "Show question"}
        </span>
      </button>
      <div id={bodyId} className="question-disclosure-body" hidden={!expanded}>
        {expanded && question?.body && (
          <Markdown className="inbox-question">{renderedBody}</Markdown>
        )}
        {expanded && (
          <AttachmentLinks taskId={t.id} attachments={question?.attachments} />
        )}
      </div>
    </div>
  );
}
