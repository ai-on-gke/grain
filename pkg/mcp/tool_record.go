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

package mcp

import (
	"encoding/json"
	"os"
	"sync"
)

// ToolCallRecord is one tool call as received over JSON-RPC before any
// downstream CLI or stream-json truncation.
type ToolCallRecord struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type toolRecorder struct {
	path string
	mu   sync.Mutex
}

// AnnounceRecordedTools tells r where to record every tool call it handles,
// appending each call's name and untruncated arguments as a JSON line to path.
func (r *Registry) AnnounceRecordedTools(path string) {
	if path == "" {
		return
	}
	r.recorder = &toolRecorder{path: path}
}

func (r *Registry) recordToolCall(name string, args map[string]any) {
	if r.recorder == nil {
		return
	}
	record := ToolCallRecord{
		Name:      name,
		Arguments: args,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	data = append(data, '\n')
	r.recorder.mu.Lock()
	defer r.recorder.mu.Unlock()
	f, err := os.OpenFile(r.recorder.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(data)
}
