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

import React, { useState } from "react";
import Markdown from "./Markdown.jsx";

export default function ExpandableMarkdown({ children, className }) {
  const [expanded, setExpanded] = useState(false);
  const cutoff = 1500;

  if (!children || typeof children !== "string") {
    return <Markdown className={className}>{children}</Markdown>;
  }

  const isCut = !expanded && children.length > cutoff;

  if (isCut) {
    return (
      <div
        className="expandable-comment"
        style={{ display: "flex", flexDirection: "column" }}
      >
        <div
          style={{
            maxHeight: "15rem",
            overflow: "hidden",
            position: "relative",
          }}
        >
          <Markdown className={className}>{children}</Markdown>
          <div
            style={{
              position: "absolute",
              bottom: 0,
              left: 0,
              right: 0,
              height: "4rem",
              background: "linear-gradient(transparent, var(--canvas))",
              pointerEvents: "none",
            }}
          />
        </div>
        <button
          type="button"
          onClick={(e) => {
            e.preventDefault();
            e.stopPropagation();
            setExpanded(true);
          }}
          style={{
            background: "none",
            border: "none",
            padding: "0.5rem 0 0 0",
            color: "var(--text-hyperlink)",
            cursor: "pointer",
            fontSize: "0.85rem",
            fontWeight: 600,
            alignSelf: "flex-start",
          }}
        >
          Show more
        </button>
      </div>
    );
  }

  return <Markdown className={className}>{children}</Markdown>;
}
