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
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// GChatLiveWebhookEnv names the environment variable holding a real
// external Google Chat space's incoming webhook URL. The live test posts
// one clearly marked message there, and is skipped without it. The URL
// is a secret: bind it from grain's secret store (gchat-live-webhook),
// never write it in a file.
const GChatLiveWebhookEnv = "GRAIN_GCHAT_LIVE_WEBHOOK"

func TestGChatLiveWebhook(t *testing.T) {
	hook := os.Getenv(GChatLiveWebhookEnv)
	if hook == "" {
		t.Skipf("%s is not set; it is the webhook URL of a real Chat space to post a test message to", GChatLiveWebhookEnv)
	}
	s, g, err := newGChat(GChatServerConfig{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	hooks, _ := json.Marshal(map[string]string{"live": hook})
	_, err = g.configure(context.Background(), map[string]any{
		"dataPlaneToken": strings.Repeat("t", MinTokenBytes),
		"grain_url":      "https://grain.example.com",
		"display_name":   "grain live test",
		"spaces":         []any{map[string]any{"name": "live", "transport": "webhook"}},
		"webhook_urls":   string(hooks),
	})
	if err != nil {
		// The error names fields, never the URL.
		t.Fatal(err)
	}
	if _, err := g.mint(context.Background(), "live-316-1-0", time.Now().Add(time.Hour), map[string]any{"space": "live"}); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	gr := g.grants["live-316-1-0"]
	g.mu.Unlock()
	res, err := g.post(context.Background(), gr, "live", "*[grain test]* gchat live test at "+time.Now().UTC().Format(time.RFC3339)+". Nobody needs to act on it.", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Message, "spaces/") || !strings.Contains(res.Thread, "/threads/") {
		t.Errorf("post = %+v, want the message and thread names", res)
	}
}
