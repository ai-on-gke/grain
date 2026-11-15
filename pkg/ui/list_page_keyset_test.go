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

package ui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// keyedPage is one page of a newest-first list as these tests read it,
// each row reduced to its key.
type keyedPage struct {
	keys       []string
	nextCursor string
}

// readKeyedPage reads path with query as a person naming a limit, which
// pages like an agent, and keys each row of items with key.
func readKeyedPage[T any](t *testing.T, srv *ui.Server, path string, query url.Values, key func(T) string) keyedPage {
	t.Helper()
	rec := do(t, srv, http.MethodGet, path+"?"+query.Encode(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s?%s: %d %s", path, query.Encode(), rec.Code, rec.Body.String())
	}
	var page struct {
		Items      []T    `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("GET %s answered %q: %v", path, rec.Body.String(), err)
	}
	out := keyedPage{nextCursor: page.NextCursor}
	for _, item := range page.Items {
		out.keys = append(out.keys, key(item))
	}
	return out
}

// walkWithHeadInsert reads the first page of path, has insert put a row
// at the head of the list, reads the second page with the first's
// cursor, and checks the second page carries on exactly where the first
// ended in the list as it stood: nothing repeated, nothing skipped. A
// cursor of the offset alone would answer the first page's last row
// again, since the insert moved every row down one.
func walkWithHeadInsert[T any](t *testing.T, srv *ui.Server, path string, limit int, key func(T) string, insert func()) {
	t.Helper()
	whole := readKeyedPage(t, srv, path, url.Values{"limit": {"0"}}, key)
	if len(whole.keys) < 2*limit {
		t.Fatalf("%s has %d rows; want at least %d to page through", path, len(whole.keys), 2*limit)
	}
	first := readKeyedPage(t, srv, path, url.Values{"limit": {strconv.Itoa(limit)}}, key)
	if first.nextCursor == "" {
		t.Fatalf("%s's first page of %d has no nextCursor", path, limit)
	}
	insert()
	after := readKeyedPage(t, srv, path, url.Values{"limit": {"0"}}, key)
	if len(after.keys) != len(whole.keys)+1 || after.keys[1] != whole.keys[0] {
		t.Fatalf("%s after the insert = %v; want one new row at the head of %v", path, after.keys, whole.keys)
	}
	second := readKeyedPage(t, srv, path, url.Values{"limit": {strconv.Itoa(limit)}, "cursor": {first.nextCursor}}, key)
	want := whole.keys[limit:min(2*limit, len(whole.keys))]
	if fmt.Sprint(second.keys) != fmt.Sprint(want) {
		t.Errorf("%s's second page after a head insert = %v; want %v (first page %v)", path, second.keys, want, first.keys)
	}
}

// tick has client's clock move on a minute at every read of it, so rows
// filed one after another sort newest-first by when they were filed
// rather than tying on the same second.
func tick(client *ui.Client) {
	var mu sync.Mutex
	now := baseTime
	client.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Minute)
		return now
	}
}

// A row inserted at the head of a newest-first list between two page
// reads neither repeats the first page's last row on the second nor
// pushes a row off it (task 4025): the cursor resumes after the row the
// first page ended on, wherever that row now sits.
func TestNewestFirstListsResumeAfterTheirLastRowAcrossAHeadInsert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("templates", func(t *testing.T) {
		t.Parallel()
		srv, client := testServer(t)
		tick(client)
		for i := range 12 {
			if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
				Name: fmt.Sprintf("tmpl-%02d", i), Title: fmt.Sprintf("Template %02d", i), Repo: "acme/widgets",
			}); err != nil {
				t.Fatal(err)
			}
		}
		walkWithHeadInsert(t, srv, "/api/templates", 5, func(tm ui.Template) string { return tm.Name }, func() {
			if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "tmpl-new", Title: "New", Repo: "acme/widgets"}); err != nil {
				t.Fatal(err)
			}
		})
	})

	raise := func(t *testing.T, client *ui.Client, i int) {
		t.Helper()
		if _, err := client.RaiseNotification(ctx, ui.RaiseNotificationRequest{
			Subject: fmt.Sprintf("ci:widgets-%02d", i), Title: fmt.Sprintf("main is red %02d", i), Severity: "warning", Repo: "acme/widgets",
		}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("notifications", func(t *testing.T) {
		t.Parallel()
		srv, client := testServer(t)
		for i := range 12 {
			raise(t, client, i)
		}
		walkWithHeadInsert(t, srv, "/api/notifications", 5, func(n struct {
			Subject string `json:"subject"`
		}) string {
			return n.Subject
		}, func() { raise(t, client, 99) })
	})

	t.Run("inbox", func(t *testing.T) {
		t.Parallel()
		srv, client := testServer(t)
		for i := range 12 {
			raise(t, client, i)
		}
		walkWithHeadInsert(t, srv, "/api/inbox", 5, func(it ui.InboxItem) string { return it.Subject }, func() { raise(t, client, 99) })
	})
}

// A row deleted from the first page between two reads does not push the
// second page's first row back onto the first page, where the caller has
// already been.
func TestNewestFirstListResumesAfterItsLastRowAcrossADeleteAhead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, client := testServer(t)
	tick(client)
	var ids []string
	for i := range 12 {
		tm, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
			Name: fmt.Sprintf("tmpl-%02d", i), Title: fmt.Sprintf("Template %02d", i), Repo: "acme/widgets",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tm.ID)
	}
	key := func(tm ui.Template) string { return tm.Name }
	whole := readKeyedPage(t, srv, "/api/templates", url.Values{"limit": {"0"}}, key)
	first := readKeyedPage(t, srv, "/api/templates", url.Values{"limit": {"5"}}, key)
	if err := client.DeleteTemplate(ctx, ids[len(ids)-1]); err != nil {
		t.Fatal(err)
	}
	second := readKeyedPage(t, srv, "/api/templates", url.Values{"limit": {"5"}, "cursor": {first.nextCursor}}, key)
	if want := whole.keys[5:10]; fmt.Sprint(second.keys) != fmt.Sprint(want) {
		t.Errorf("second page after deleting a first-page row = %v; want %v (whole list %v)", second.keys, want, whole.keys)
	}
}

// A cursor of the offset alone, as every cursor was before they carried
// the last row's key, still reads on from that offset.
func TestAnOffsetOnlyCursorStillPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv, client := testServer(t)
	for i := range 8 {
		if _, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
			Name: fmt.Sprintf("tmpl-%02d", i), Title: fmt.Sprintf("Template %02d", i), Repo: "acme/widgets",
		}); err != nil {
			t.Fatal(err)
		}
	}
	key := func(tm ui.Template) string { return tm.Name }
	whole := readKeyedPage(t, srv, "/api/templates", url.Values{"limit": {"0"}}, key)
	page := readKeyedPage(t, srv, "/api/templates", url.Values{"limit": {"3"}, "cursor": {ui.ListCursorFor(4)}}, key)
	if want := whole.keys[4:7]; fmt.Sprint(page.keys) != fmt.Sprint(want) {
		t.Errorf("an offset-only cursor at 4 = %v; want %v", page.keys, want)
	}
}
