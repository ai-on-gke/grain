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

package agent

// Kinds of TranscriptItem: the same words as Silo's own normalised
// transcript items (silo events.Item), so a reader handing them on has
// nothing to translate.
const (
	ItemText       = "text"
	ItemThinking   = "thinking"
	ItemToolCall   = "tool_call"
	ItemToolResult = "tool_result"
	ItemError      = "error"
)

// TranscriptItem is one entry of a run's narrative as a framework's own
// parser read it: each paragraph the rendered transcript writes, kept
// apart by what it is. An item is whole when it is listed -- a reader
// tailing a run that is still going sees the same items, in the same
// order, every time it reads it, with new ones only ever after them --
// which is what lets a caller name each by its place in the list.
type TranscriptItem struct {
	Kind string
	// Text is the item as the transcript renders it: the assistant's
	// words, a tool call's name and arguments, what a tool returned.
	Text string
	// Tool is a tool call's or result's tool, when the framework says.
	Tool    string
	IsError bool
}
