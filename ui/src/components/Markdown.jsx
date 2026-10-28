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

import { memo } from "react";
import ReactMarkdown from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";
import remarkMermaid from "../mermaidPair.js";
import { safeUrl } from "../safeUrl.js";
import { MermaidComparison, MermaidDiagram } from "./MermaidDiagram.jsx";

export { safeUrl };

// Markdown renders a body of prose as the markdown it was almost
// certainly written in. Every agent grain dispatches writes markdown by
// default -- a relayed question comes back with headings, bullet lists,
// backticked paths and fenced diffs in it -- and rendering that as one
// flat run of text is what made a long answer hard to read
// (grain/task-93). It is used for the two places a task carries somebody
// else's words: a comment body and the task description, both of which a
// human writes in the same syntax when replying, so both get the same
// treatment rather than one rule for agents and another for people.
//
// Deliberately *not* used for an attempt's transcript
// (AttemptTranscriptOverlay): that is a log of thinking, text and tool
// calls interleaved, whose alignment is the whole of its legibility, and
// it stays in its <pre>.
//
// Nothing here renders raw HTML. react-markdown skips embedded HTML
// unless rehype-raw is added, which it is not, so a comment body -- a
// string that reaches the page straight off an agent or a GitHub user --
// cannot inject markup: an embedded tag comes out as escaped text. That
// is the whole reason this goes through a parser rather than a
// hand-rolled regex pass. Do not add rehype-raw here.
//
// remarkBreaks alongside remarkGfm because these bodies are read as
// comments, not as documents: GitHub renders a single newline in a
// comment as a line break, and so does the plain-text rendering this
// replaces (.description's own white-space: pre-wrap). Without it, a
// hand-typed reply's line breaks would silently collapse into one
// paragraph -- a regression dressed up as a feature.
//
// remarkMermaid turns ```mermaid fences into block-level diagram elements
// and pairs adjacent Before / After Mermaid blocks into a comparison view
// while keeping raw HTML in prose inert.
const REMARK_PLUGINS = [remarkGfm, remarkBreaks, remarkMermaid];

// Links go to a new tab, the same as every other outbound link in the
// UI (DetailOverlay's own pull request links): the overlay they are read
// in is a transient thing, and navigating it away loses whatever the
// operator was in the middle of. A fragment is the exception: it goes
// nowhere outside this page, and the ones grain writes itself name a task
// the timeline opens in place (taskRefs.js).
const COMPONENTS = {
  a: ({ node, ...props }) =>
    String(props.href ?? "").startsWith("#") ? (
      <a {...props} />
    ) : (
      <a {...props} target="_blank" rel="noopener noreferrer" />
    ),
  "mermaid-diagram": ({ node, ...props }) => (
    <MermaidDiagram
      code={props["data-code"] ?? node?.properties?.dataCode ?? ""}
      title={props["data-title"] ?? node?.properties?.dataTitle ?? ""}
    />
  ),
  "mermaid-comparison": ({ node, ...props }) => (
    <MermaidComparison
      beforeCode={
        props["data-before-code"] ?? node?.properties?.dataBeforeCode ?? ""
      }
      afterCode={
        props["data-after-code"] ?? node?.properties?.dataAfterCode ?? ""
      }
      beforeLabel={
        props["data-before-label"] ??
        node?.properties?.dataBeforeLabel ??
        "Before"
      }
      afterLabel={
        props["data-after-label"] ?? node?.properties?.dataAfterLabel ?? "After"
      }
    />
  ),
};

// className is the caller's own styling hook (.description,
// .timeline-comment-body), kept on the wrapper so the markdown block
// sits exactly where the plain <div> it replaced did; .markdown carries
// the element styles for everything the parser can emit.
function Markdown({ children, className }) {
  return (
    <div className={className ? `markdown ${className}` : "markdown"}>
      <ReactMarkdown
        remarkPlugins={REMARK_PLUGINS}
        components={COMPONENTS}
        urlTransform={safeUrl}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}

// memo, because this is the most expensive component in the UI and the
// cheapest one to compare: two props, both strings, so React's own
// shallow compare is two `===` against a remark/rehype pass over a body
// that can be kilobytes of prose (grain/task-174).
//
// What that buys is not a micro-optimisation, it is the difference
// between a screen that redraws and one that does not. Every place that
// renders many of these at once renders them *inside* something with
// state of its own that changes for unrelated reasons -- a task's
// timeline under an overlay that polls every three seconds, a chat
// transcript above a composer that sets state on every keystroke -- and
// without this, one character typed re-parsed every message in the
// conversation. Measured at 112ms of script per keystroke on a 41-message
// chat before this, growing linearly with the conversation:
// docs/hand-runs/2026-09-17-chat-keystroke.md.
//
// It is safe in the way memo usually is not, because there is nothing
// else in the props to get stale: no children beyond the string, no
// callbacks, no objects. A caller that passes a new body string gets a
// new render, which is the whole of what this component depends on.
export default memo(Markdown);
