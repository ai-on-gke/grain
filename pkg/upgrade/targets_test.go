package upgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestTargetsOffersTheStandardBranchesAndReleasedVersions: the pane's
// selector lists the channels, then the released versions newest first
// (from the registry's tags, rc and branch tags left out), then any
// literal branch the operator allowed -- each marked with whether Start
// would take it.
func TestTargetsOffersTheStandardBranchesAndReleasedVersions(t *testing.T) {
	listed := 0
	u := New(Config{
		Image:           &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
		AllowedBranches: []string{"main", "latest", "v*", "release/*", "ops/pinned"},
		ListTags: func(_ context.Context, repo string) ([]string, error) {
			listed++
			if repo != "ghcr.io/bwsalmon/grain/grain" {
				t.Errorf("ListTags(%q), want the configured repository", repo)
			}
			return []string{"main", "latest", "sha-1234567", "v1.5.0", "v1.10.0", "v1.6.0", "v1.7.0-rc.1", "candidate", "grain..task-1"}, nil
		},
	})
	got := u.Targets(context.Background())
	want := []Target{
		{Name: "latest", Kind: TargetChannel, Allowed: true},
		{Name: "candidate", Kind: TargetChannel, Allowed: false},
		{Name: "main", Kind: TargetChannel, Allowed: true},
		{Name: "v1.10.0", Kind: TargetVersion, Allowed: true},
		{Name: "v1.6.0", Kind: TargetVersion, Allowed: true},
		{Name: "v1.5.0", Kind: TargetVersion, Allowed: true},
		{Name: "ops/pinned", Kind: TargetAllowed, Allowed: true},
	}
	if !reflect.DeepEqual(got.Targets, want) {
		t.Errorf("Targets =\n%+v\nwant\n%+v", got.Targets, want)
	}
	if !reflect.DeepEqual(got.AllowedBranches, []string{"main", "latest", "v*", "release/*", "ops/pinned"}) {
		t.Errorf("AllowedBranches = %v", got.AllowedBranches)
	}
	if got.VersionsError != "" {
		t.Errorf("VersionsError = %q, want none", got.VersionsError)
	}
	// The tag list is cached: the pane is opened far more often than a
	// release is promoted.
	u.Targets(context.Background())
	if listed != 1 {
		t.Errorf("registry listed %d times, want 1", listed)
	}
}

// TestTargetsWithoutARegistry: the source pipeline resets onto
// origin/<branch>, which neither "latest" nor a version tag is, so it is
// offered branches only; and a registry that cannot be listed still
// leaves the channels, with the reason said.
func TestTargetsWithoutARegistry(t *testing.T) {
	src := New(Config{SrcDir: t.TempDir()})
	got := src.Targets(context.Background())
	want := []Target{
		{Name: "candidate", Kind: TargetChannel, Allowed: true},
		{Name: "main", Kind: TargetChannel, Allowed: true},
	}
	if !reflect.DeepEqual(got.Targets, want) {
		t.Errorf("source Targets = %+v, want %+v", got.Targets, want)
	}

	broken := New(Config{
		Kube: &KubeConfig{Repository: "registry.example/grain"},
		ListTags: func(context.Context, string) ([]string, error) {
			return nil, errors.New("registry.example answered 401 Unauthorized")
		},
	})
	got = broken.Targets(context.Background())
	if len(got.Targets) != 3 || got.Targets[0].Name != "latest" {
		t.Errorf("Targets with an unlistable registry = %+v, want the three channels", got.Targets)
	}
	if !strings.Contains(got.VersionsError, "401") {
		t.Errorf("VersionsError = %q, want the registry's answer", got.VersionsError)
	}
}

// TestTargetsOnADefaultDeployment: one that never set
// -upgrade-allowed-branches offers every channel and released version as
// one it takes (grain/task-2011) -- and the source pipeline still is not
// offered "latest", a tag it cannot reset onto, just because it is
// allowed.
func TestTargetsOnADefaultDeployment(t *testing.T) {
	img := New(Config{
		Image: &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
		ListTags: func(context.Context, string) ([]string, error) {
			return []string{"main", "latest", "candidate", "v1.6.0", "v1.7.0", "v1.7.0-rc.1", "sha-1234567"}, nil
		},
	})
	want := []Target{
		{Name: "latest", Kind: TargetChannel, Allowed: true},
		{Name: "candidate", Kind: TargetChannel, Allowed: true},
		{Name: "main", Kind: TargetChannel, Allowed: true},
		{Name: "v1.7.0", Kind: TargetVersion, Allowed: true},
		{Name: "v1.6.0", Kind: TargetVersion, Allowed: true},
	}
	if got := img.Targets(context.Background()); !reflect.DeepEqual(got.Targets, want) {
		t.Errorf("image Targets =\n%+v\nwant\n%+v", got.Targets, want)
	}

	src := New(Config{SrcDir: t.TempDir()})
	want = []Target{
		{Name: "candidate", Kind: TargetChannel, Allowed: true},
		{Name: "main", Kind: TargetChannel, Allowed: true},
	}
	if got := src.Targets(context.Background()); !reflect.DeepEqual(got.Targets, want) {
		t.Errorf("source Targets = %+v, want %+v", got.Targets, want)
	}
}

// TestAnyBranchPattern: "**" is how an operator opts into upgrading onto
// any branch at all, slashes included; it still refuses a malformed one.
func TestAnyBranchPattern(t *testing.T) {
	u := New(Config{SrcDir: t.TempDir(), AllowedBranches: []string{"main", AnyBranchPattern}})
	for _, b := range []string{"main", "candidate", "grain/task-1914", "claude/a/b/c"} {
		if _, err := u.ValidateBranch(b); err != nil {
			t.Errorf("ValidateBranch(%q) = %v, want nil", b, err)
		}
	}
	for _, b := range []string{"a..b", "-x", "a//b"} {
		if _, err := u.ValidateBranch(b); err == nil {
			t.Errorf("ValidateBranch(%q) = nil, want a malformed-name error", b)
		}
	}
}

// TestListRegistryTags drives the registry v2 conversation a tag list
// takes: the anonymous token challenge, then the pages Link names.
func TestListRegistryTags(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:owner/grain:pull" || r.URL.Query().Get("service") != "reg" {
				t.Errorf("token request %s", r.URL)
			}
			w.Write([]byte(`{"token":"anon"}`))
		case "/v2/owner/grain/tags/list":
			if r.Header.Get("Authorization") != "Bearer anon" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:owner/grain:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</v2/owner/grain/tags/list?n=1000&last=main>; rel="next"`)
				w.Write([]byte(`{"tags":["main","v1.5.0"]}`))
				return
			}
			w.Write([]byte(`{"tags":["v1.6.0"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	saved := registryClient
	registryClient = srv.Client()
	defer func() { registryClient = saved }()

	host := strings.TrimPrefix(srv.URL, "https://")
	tags, err := ListRegistryTags(context.Background(), host+"/owner/grain")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"main", "v1.5.0", "v1.6.0"}) {
		t.Errorf("tags = %v", tags)
	}
}

// A registry on this machine that speaks plain HTTP -- `docker run -p
// 5000:5000 registry:2`, which docker and cosign both pull from and
// verify against without being told it is insecure -- lists its
// versions too, rather than the selector reporting "server gave HTTP
// response to HTTPS client" (docs/test-runs/2026-10-05-image-signing-
// and-upgrade-targets.md, finding D). A registry anywhere else is never
// asked over plain HTTP.
func TestListRegistryTagsFallsBackToHTTPOnlyOnLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/owner/grain/tags/list" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tags":["main","v1.6.0"]}`))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]

	tags, err := ListRegistryTags(context.Background(), "localhost:"+port+"/owner/grain")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"main", "v1.6.0"}) {
		t.Errorf("tags = %v", tags)
	}

	for host, want := range map[string]bool{
		"localhost": true, "localhost:5000": true, "127.0.0.1:5000": true, "[::1]:5000": true,
		"ghcr.io": false, "registry.example:5000": false, "10.0.0.1:5000": false,
	} {
		if got := loopbackRegistry(host); got != want {
			t.Errorf("loopbackRegistry(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestTargetsFiltersReleasedVersionsByEdition(t *testing.T) {
	tags := []string{
		"main",
		"latest",
		"candidate",
		"v2.0.0-google",
		"v2.0.0-rc.1-google",
		"v2.0.0",
		"v2.0.0-rc.1",
		"v1.9.0-google",
		"v1.9.0",
		"v1.8.0-oss",
		"sha-1234567",
	}

	// Unfiltered releasedVersions recognizes both bare and edition-qualified release tags,
	// while excluding rc and sha tags.
	if got, want := releasedVersions(tags), []string{"v2.0.0", "v2.0.0-google", "v1.9.0", "v1.9.0-google", "v1.8.0-oss"}; !reflect.DeepEqual(got, want) {
		t.Errorf("releasedVersions(tags) = %v, want %v", got, want)
	}

	// A Google edition install offers only Google-edition release tags.
	googleUp := New(Config{
		Edition: "google",
		Image:   &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
		ListTags: func(context.Context, string) ([]string, error) {
			return tags, nil
		},
	})
	wantGoogle := []Target{
		{Name: "latest", Kind: TargetChannel, Allowed: true},
		{Name: "candidate", Kind: TargetChannel, Allowed: true},
		{Name: "main", Kind: TargetChannel, Allowed: true},
		{Name: "v2.0.0-google", Kind: TargetVersion, Allowed: true},
		{Name: "v1.9.0-google", Kind: TargetVersion, Allowed: true},
	}
	if got := googleUp.Targets(context.Background()).Targets; !reflect.DeepEqual(got, wantGoogle) {
		t.Errorf("Google edition Targets =\n%+v\nwant\n%+v", got, wantGoogle)
	}

	// A core / OSS edition install offers only core / OSS release tags, not -google ones.
	coreUp := New(Config{
		Edition: "oss",
		Image:   &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
		ListTags: func(context.Context, string) ([]string, error) {
			return tags, nil
		},
	})
	wantCore := []Target{
		{Name: "latest", Kind: TargetChannel, Allowed: true},
		{Name: "candidate", Kind: TargetChannel, Allowed: true},
		{Name: "main", Kind: TargetChannel, Allowed: true},
		{Name: "v2.0.0", Kind: TargetVersion, Allowed: true},
		{Name: "v1.9.0", Kind: TargetVersion, Allowed: true},
		{Name: "v1.8.0-oss", Kind: TargetVersion, Allowed: true},
	}
	if got := coreUp.Targets(context.Background()).Targets; !reflect.DeepEqual(got, wantCore) {
		t.Errorf("Core/OSS edition Targets =\n%+v\nwant\n%+v", got, wantCore)
	}
}

func TestTargetsDiscardedRC(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running string
		tags    []string
		want    *DiscardedRC
	}{
		{
			name:    "discarded-tag-no-replacement",
			running: "v2.3.0-rc.1",
			tags:    []string{"latest", "candidate", "rc", "v2.1.1", "v2.0.0", "v2.3.0-rc.1", "v2.3.0-rc.1-discarded"},
			want:    &DiscardedRC{Running: "v2.3.0-rc.1", Latest: "v2.1.1"},
		},
		{
			name:    "superseded-by-newer-rc",
			running: "v2.3.0-rc.1",
			tags:    []string{"latest", "candidate", "rc", "v2.1.1", "v2.3.0-rc.1", "v2.3.0-rc.1-discarded", "v2.3.0-rc.2"},
			want:    &DiscardedRC{Running: "v2.3.0-rc.1", Latest: "v2.1.1", ReplacementRC: "v2.3.0-rc.2"},
		},
		{
			name:    "superseded-without-explicit-discarded-tag",
			running: "v2.3.0-rc.1",
			tags:    []string{"latest", "candidate", "v2.1.1", "v2.3.0-rc.1", "v2.3.0-rc.2"},
			want:    &DiscardedRC{Running: "v2.3.0-rc.1", Latest: "v2.1.1", ReplacementRC: "v2.3.0-rc.2"},
		},
		{
			name:    "skips-discarded-intermediate-rc",
			running: "v2.3.0-rc.1",
			tags:    []string{"v2.1.1", "v2.3.0-rc.1", "v2.3.0-rc.1-discarded", "v2.3.0-rc.2", "v2.3.0-rc.2-discarded", "v2.3.0-rc.3"},
			want:    &DiscardedRC{Running: "v2.3.0-rc.1", Latest: "v2.1.1", ReplacementRC: "v2.3.0-rc.3"},
		},
		{
			name:    "already-promoted",
			running: "v2.3.0-rc.1",
			tags:    []string{"v2.1.1", "v2.3.0-rc.1", "v2.3.0"},
			want:    nil,
		},
		{
			name:    "active-rc-still-soaking",
			running: "v2.3.0-rc.1",
			tags:    []string{"v2.1.1", "v2.3.0-rc.1", "candidate", "rc"},
			want:    nil,
		},
		{
			name:    "running-promoted-release",
			running: "v2.1.1",
			tags:    []string{"v2.1.1", "v2.3.0-rc.1", "v2.3.0-rc.1-discarded"},
			want:    nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := New(Config{
				Image:          &ImageConfig{Repository: "ghcr.io/bwsalmon/grain/grain"},
				RunningRelease: func() string { return tc.running },
				ListTags: func(context.Context, string) ([]string, error) {
					return tc.tags, nil
				},
			})
			got := u.Targets(context.Background()).DiscardedRC
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Targets().DiscardedRC = %+v, want %+v", got, tc.want)
			}
			if gotDirect := u.DiscardedRC(context.Background()); !reflect.DeepEqual(gotDirect, tc.want) {
				t.Errorf("DiscardedRC() = %+v, want %+v", gotDirect, tc.want)
			}
		})
	}
}
