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

package intree

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/geminikey"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// GeminiKeyMethod is the gcp server's gemini-key method, offered when the
// server's configuration has gemini_key: the in-process gemini-key's
// replacement (design/design/unified-grants.design.md Q6).
const GeminiKeyMethod = "gemini-key"

// GeminiMinterFunc builds the API Keys minter a gemini-key mint and
// release go through, authenticated as credentialJSON ("" for the
// server's ambient identity) in project.
type GeminiMinterFunc func(ctx context.Context, credentialJSON, project string) (geminikey.KeyMinter, error)

// newGeminiKeyMethod is the gemini-key method over c's gemini_key: a
// per-attempt Gemini API key created in its project with its admin's
// credential, placed and described exactly as the in-process gemini-key
// placed and described one (geminikey.KeyPath, geminikey.Prompt), and
// deleted at release or past the attempt's deadline.
func newGeminiKeyMethod(c *gcpsets.Config, newMinter GeminiMinterFunc, now func() time.Time) (*protocol.Method, error) {
	if newMinter == nil {
		newMinter = geminikey.NewKeyMinter
	}
	var mu sync.Mutex
	type minted struct {
		name string
		resp *protocol.MintResponse
	}
	keys := map[string]minted{}
	minter := func(ctx context.Context) (geminikey.KeyMinter, string, error) {
		a := c.GeminiAdmin()
		if a == nil {
			return nil, "", errors.New("gcp: gemini_key names no admin")
		}
		m, err := newMinter(ctx, a.Credential, c.GeminiKey.Project)
		return m, "gcp admin " + a.Name, err
	}
	return protocol.NewMethod(protocol.MethodSpec{
		Name: GeminiKeyMethod,
		Description: "Mint a per-attempt Gemini API key in project " + c.GeminiKey.Project +
			", restricted to the Generative Language API, deleted at release or past the attempt's deadline.",
		Args: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		Now:  now,
		Mint: func(ctx context.Context, attemptID string, deadline time.Time, _ map[string]any) (*protocol.MintResponse, error) {
			mu.Lock()
			held, renewing := keys[attemptID]
			mu.Unlock()
			if renewing {
				// A renewal keeps the key the run already holds.
				return held.resp, nil
			}
			m, who, err := minter(ctx)
			if err != nil {
				return nil, err
			}
			name, key, err := geminikey.MintRunKey(ctx, m, attemptID, who)
			if err != nil {
				var exhausted *model.ResourceExhaustedError
				if errors.As(err, &exhausted) {
					return &protocol.MintResponse{Status: protocol.StatusResourceExhausted, Reason: err.Error(),
						RetryAfter: now().Add(ReapInterval).UTC().Format(time.RFC3339)}, nil
				}
				return nil, err
			}
			resp := &protocol.MintResponse{
				Status:        protocol.StatusOK,
				Placements:    []protocol.Placement{{Path: geminikey.KeyPath, Mode: "0600", Content: key}},
				PromptSection: geminikey.Prompt(),
			}
			mu.Lock()
			keys[attemptID] = minted{name: name, resp: resp}
			mu.Unlock()
			return resp, nil
		},
		Release: func(ctx context.Context, attemptID string) error {
			mu.Lock()
			held, ok := keys[attemptID]
			mu.Unlock()
			if !ok {
				return nil
			}
			m, _, err := minter(ctx)
			if err != nil {
				return err
			}
			if err := m.DeleteKey(ctx, held.name); err != nil {
				return err
			}
			mu.Lock()
			delete(keys, attemptID)
			mu.Unlock()
			return nil
		},
	})
}

// geminiReaper is the tick deleting grain's Gemini keys in current's
// gemini_key project older than geminikey.MaxLease plus grace, every
// ReapInterval: the keys no release reached, which the in-process
// gemini-key's reap deleted.
func geminiReaper(s *Server, current func() *gcpsets.Config, newMinter GeminiMinterFunc, now func() time.Time) func(context.Context) {
	if newMinter == nil {
		newMinter = geminikey.NewKeyMinter
	}
	var last time.Time
	return func(ctx context.Context) {
		c := current()
		if c == nil || c.GeminiKey == nil || (!last.IsZero() && now().Sub(last) < ReapInterval) {
			return
		}
		last = now()
		a := c.GeminiAdmin()
		if a == nil {
			return
		}
		m, err := newMinter(ctx, a.Credential, c.GeminiKey.Project)
		if err != nil {
			s.Log.Printf("gcp: reaping Gemini keys: %v", err)
			return
		}
		deleted, err := geminikey.ReapBefore(ctx, m, now().Add(-geminikey.MaxLease-protocol.DefaultGrace), "gcp admin "+a.Name)
		for _, k := range deleted {
			s.Log.Printf("gcp: reaped unreleased Gemini key %s", k)
		}
		if err != nil {
			s.Log.Printf("gcp: reaping Gemini keys: %v", err)
		}
	}
}
