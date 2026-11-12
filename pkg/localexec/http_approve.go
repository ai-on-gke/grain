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

package localexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPApprover asks Grain's daemon for a person's approval over HTTP: it
// posts each Request to /api/tasks/{task}/local-script-approvals, long-polls
// /wait until an answer arrives or ctx ends, and marks the call expired if
// ctx ends first.
type HTTPApprover struct {
	// BaseURL is the daemon's base URL (scheme://host[:port]), or the full
	// /api/tasks/{task}/local-script-approvals URL when TaskID is empty.
	BaseURL string
	TaskID  string
	Token   string
	Client  *http.Client
}

type httpApprovalReply struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (a HTTPApprover) approvalsURL() string {
	base := strings.TrimRight(a.BaseURL, "/")
	if a.TaskID == "" {
		return base
	}
	return base + "/api/tasks/" + url.PathEscape(a.TaskID) + "/local-script-approvals"
}

func (a HTTPApprover) httpClient() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

// Approve posts r to the daemon and waits for a person to approve or deny it.
func (a HTTPApprover) Approve(ctx context.Context, r Request) (bool, error) {
	endpoint := a.approvalsURL()
	body, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// The ask itself is not cut short by ctx: the server records the
	// pending approval before it answers, so a call cancelled mid-ask
	// would leave an approval in the inbox that nothing ever expires
	// (it has no ID to expire yet). Finish the ask, bounded, and let the
	// loop below see ctx and expire what it created.
	createCtx, cancelCreate := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	approval, err := a.doJSON(createCtx, http.MethodPost, endpoint, body)
	cancelCreate()
	if err != nil {
		return false, fmt.Errorf("asking for approval: %w", err)
	}
	for {
		switch approval.Status {
		case "approved":
			return true, nil
		case "denied":
			return false, nil
		case "expired":
			return false, errors.New("nobody answered before the call stopped waiting")
		}
		if ctx.Err() != nil {
			break
		}
		waitURL := endpoint + "/" + url.PathEscape(approval.ID) + "/wait"
		next, err := a.doJSON(ctx, http.MethodPost, waitURL, []byte("{}"))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		approval = next
	}
	expireCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	expireURL := endpoint + "/" + url.PathEscape(approval.ID) + "/expire"
	_, _ = a.doJSON(expireCtx, http.MethodPost, expireURL, []byte("{}"))
	return false, ctx.Err()
}

func (a HTTPApprover) doJSON(ctx context.Context, method, rawURL string, body []byte) (httpApprovalReply, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return httpApprovalReply{}, err
	}
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return httpApprovalReply{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return httpApprovalReply{}, errors.New(msg)
	}
	var out httpApprovalReply
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return httpApprovalReply{}, err
		}
	}
	return out, nil
}
