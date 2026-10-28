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

package ghwebhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxClientResponseBytes = 8 << 20

// Client fetches verified GitHub webhook events from a github-webhook
// receiver over the signed /v1/events endpoint.
type Client struct {
	BaseURL     string
	RelaySecret string
	HTTP        *http.Client
	Now         func() time.Time
}

func (c *Client) httpClient(wait time.Duration) *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: wait + 10*time.Second}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Poll fetches events with Seq > after, optionally long-polling for up to
// wait (rounded to whole seconds, capped at MaxLongPollWait) when the queue
// is currently caught up.
func (c *Client) Poll(ctx context.Context, after uint64, limit int, wait time.Duration) (EventsResponse, error) {
	if strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.RelaySecret) == "" {
		return EventsResponse{}, fmt.Errorf("ghwebhook: client requires BaseURL and RelaySecret")
	}
	if limit <= 0 {
		limit = 100
	}
	if wait < 0 {
		wait = 0
	}
	if wait > MaxLongPollWait {
		wait = MaxLongPollWait
	}
	vals := url.Values{}
	vals.Set("after", fmt.Sprint(after))
	vals.Set("limit", fmt.Sprint(limit))
	if waitSecs := int(wait / time.Second); waitSecs > 0 {
		vals.Set("wait", fmt.Sprint(waitSecs))
	}
	requestURI := "/v1/events?" + vals.Encode()
	fullURL := strings.TrimSuffix(c.BaseURL, "/") + requestURI

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return EventsResponse{}, err
	}
	ts := c.now()
	req.Header.Set(RelayTimestampHeader, fmt.Sprint(ts.Unix()))
	req.Header.Set(RelaySignatureHeader, SignRelay(c.RelaySecret, ts, http.MethodGet, requestURI, nil))

	resp, err := c.httpClient(wait).Do(req)
	if err != nil {
		return EventsResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClientResponseBytes))
	if err != nil {
		return EventsResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return EventsResponse{}, fmt.Errorf("ghwebhook: gateway answered %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out EventsResponse
	if err := json.Unmarshal(bytes.TrimSpace(body), &out); err != nil {
		return EventsResponse{}, fmt.Errorf("ghwebhook: decoding gateway response: %w", err)
	}
	return out, nil
}
