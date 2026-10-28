package ui_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// authzReleaseServer is a deployment whose acme/widgets has a release
// "2.1" still provisioning.
func authzReleaseServer(t *testing.T) (*ui.Server, *model.Store) {
	srv, _, store := authzReleaseServerWithClient(t)
	return srv, store
}

func authzReleaseServerWithClient(t *testing.T) (*ui.Server, *ui.Client, *model.Store) {
	t.Helper()
	srv, client, store := releaseServer(t, nil)
	ctx := context.Background()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelease(ctx, repo, "2.1", time.Now()); err != nil {
		t.Fatal(err)
	}
	return srv, client, store
}

// Creating, cutting, promoting and merging back a release need what
// starting a release run does (requireRepoPerson): an agent is refused
// with a 403 and nothing is written, while any person -- the owner --
// gets past the check, to whatever the release's own state then says (a
// provisioning release cannot be cut yet, which is a 400).
func TestReleaseWritesNeedAPerson(t *testing.T) {
	t.Parallel()
	routes := []struct {
		name, method, path, body string
		allowed                  int
	}{
		{"create", http.MethodPost, "/api/repos/acme/widgets/releases", `{"name":"r-%s"}`, http.StatusCreated},
		{"delete", http.MethodDelete, "/api/repos/acme/widgets/releases/2.1", "", http.StatusNoContent},
		{"cut", http.MethodPost, "/api/repos/acme/widgets/releases/2.1/candidates", "", http.StatusBadRequest},
		{"promote", http.MethodPost, "/api/repos/acme/widgets/releases/2.1/candidates/promote", "", http.StatusBadRequest},
		{"merge", http.MethodPost, "/api/repos/acme/widgets/releases/2.1/merge", "", http.StatusBadRequest},
	}
	users := []struct {
		user    string
		allowed bool
	}{
		{agentPrefix + "run-1", false}, {"zed", true}, {"owner", true},
	}
	for _, rt := range routes {
		for _, u := range users {
			t.Run(rt.name+"/"+u.user, func(t *testing.T) {
				t.Parallel()
				srv, store := authzReleaseServer(t)
				body := rt.body
				if body != "" {
					body = fmt.Sprintf(body, u.user)
				}
				rec := doAsCaller(t, srv, u.user, rt.method, rt.path, body)
				want := http.StatusForbidden
				if u.allowed {
					want = rt.allowed
				}
				if rec.Code != want {
					t.Fatalf("%s %s: %d %s, want %d", u.user, rt.name, rec.Code, rec.Body, want)
				}
				list, err := store.ListReleases(context.Background(), widgets)
				if err != nil {
					t.Fatal(err)
				}
				wantReleases := 1
				if u.allowed && rt.name == "create" {
					wantReleases = 2
				} else if u.allowed && rt.name == "delete" {
					wantReleases = 0
				}
				if len(list) != wantReleases {
					t.Fatalf("releases = %+v, want %d", list, wantReleases)
				}

				rec = doAsCaller(t, srv, u.user, http.MethodGet, "/api/repos/acme/widgets/release-creation", "")
				got := decode[ui.ReleaseCreation](t, rec)
				if got.Allowed != u.allowed || got.Manageable != u.allowed || (!u.allowed && got.Reason == "") {
					t.Fatalf("release-creation for %s = %+v, want allowed and manageable %v", u.user, got, u.allowed)
				}
			})
		}
	}
}

// Asking for a branch needs a person (requireRepoPerson): an agent is
// refused with a 403 and nothing is recorded.
func TestCreateBranchNeedsAPerson(t *testing.T) {
	t.Parallel()
	cases := []struct {
		user string
		want int
	}{
		{agentPrefix + "run-1", http.StatusForbidden},
		{"zed", http.StatusCreated},
		{"owner", http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.user, func(t *testing.T) {
			t.Parallel()
			srv, store := authzReleaseServer(t)
			rec := doAsCaller(t, srv, tc.user, http.MethodPost, "/api/repos/acme/widgets/branches", `{"name":"b-`+strings.TrimPrefix(tc.user, agentPrefix)+`"}`)
			if rec.Code != tc.want {
				t.Fatalf("%s: %d %s, want %d", tc.user, rec.Code, rec.Body, tc.want)
			}
			list, err := store.ListBranches(context.Background(), widgets)
			if err != nil {
				t.Fatal(err)
			}
			if wantRows := map[bool]int{true: 1, false: 0}[tc.want == http.StatusCreated]; len(list) != wantRows {
				t.Fatalf("branches = %+v, want %d", list, wantRows)
			}
		})
	}
}
