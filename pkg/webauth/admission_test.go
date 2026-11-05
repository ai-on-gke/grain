package webauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeIdentityVerifier struct {
	caller *Caller
	err    error
}

func (f *fakeIdentityVerifier) Authenticate(r *http.Request) (*Caller, error) {
	if f.err != nil {
		return nil, f.err
	}
	if hdr := r.Header.Get("X-Test-Email"); hdr != "" {
		return &Caller{
			Email:   hdr,
			Subject: "sub:" + hdr,
		}, nil
	}
	if f.caller != nil {
		return f.caller, nil
	}
	return nil, errors.New("missing test identity")
}

type failingGroupChecker struct{}

func (failingGroupChecker) IsMember(ctx context.Context, groupEmail, memberEmail string) (bool, error) {
	return false, errors.New("upstream directory timeout")
}

func TestAdmitterHumanServicePrincipalAndRoles(t *testing.T) {
	groups := NewStaticGroups(map[string][]string{
		"group:platform-admins@example.com": {"admin-group@example.com"},
		"group:builders@example.com":        {"builder@example.com"},
	})
	disabledSet := map[string]bool{
		"disabled@example.com": true,
	}

	admitter, err := NewAdmitter(AdmissionConfig{
		Verifier: &fakeIdentityVerifier{},
		Groups:   groups,
		AllowedDomains: []string{
			"example.com",
		},
		Admins: []string{
			"user:direct-admin@example.com",
			"group:platform-admins@example.com",
		},
		Creators: []string{
			"user:direct-creator@example.com",
			"group:builders@example.com",
		},
		ServicePrincipals: []string{
			"serviceAccount:scanner@project.iam.gserviceaccount.com",
		},
		IsDisabled: func(ctx context.Context, email string) (bool, error) {
			return disabledSet[email], nil
		},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}

	t.Run("direct admin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Test-Email", "direct-admin@example.com")
		c, err := admitter.Admit(req)
		if err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if c.Kind != PrincipalHuman || !c.IsAdmin || !c.CanCreate {
			t.Fatalf("unexpected caller: %+v", c)
		}
	})

	t.Run("group admin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Test-Email", "admin-group@example.com")
		c, err := admitter.Admit(req)
		if err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if !c.IsAdmin || !c.CanCreate {
			t.Fatalf("expected group admin with create rights: %+v", c)
		}
	})

	t.Run("group creator", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Test-Email", "builder@example.com")
		c, err := admitter.Admit(req)
		if err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if c.IsAdmin || !c.CanCreate {
			t.Fatalf("expected non-admin creator: %+v", c)
		}
	})

	t.Run("regular viewer when creators configured", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Test-Email", "viewer@example.com")
		c, err := admitter.Admit(req)
		if err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if c.IsAdmin || c.CanCreate {
			t.Fatalf("expected viewer without create rights: %+v", c)
		}
	})

	t.Run("service principal admitted for catalog read but rejected on human endpoint", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/directory", nil)
		req.Header.Set("X-Test-Email", "scanner@project.iam.gserviceaccount.com")
		c, err := admitter.Admit(req)
		if err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if c.Kind != PrincipalService || c.IsAdmin || c.CanCreate {
			t.Fatalf("unexpected service principal: %+v", c)
		}

		rec := httptest.NewRecorder()
		h := admitter.RequireHuman(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for service principal on human route, got %d", rec.Code)
		}
	})

	t.Run("disallowed domain rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Test-Email", "stranger@other.org")
		if _, err := admitter.Admit(req); !errors.Is(err, ErrCallerForbidden) {
			t.Fatalf("expected ErrCallerForbidden, got %v", err)
		}
	})

	t.Run("disabled human rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("X-Test-Email", "disabled@example.com")
		if _, err := admitter.Admit(req); !errors.Is(err, ErrCallerForbidden) {
			t.Fatalf("expected ErrCallerForbidden, got %v", err)
		}
	})
}

func TestAdmitterDefaultCreatorsAndFailClosed503(t *testing.T) {
	admitter, err := NewAdmitter(AdmissionConfig{
		Verifier:       &fakeIdentityVerifier{},
		Groups:         failingGroupChecker{},
		AllowedDomains: []string{"example.com"},
		Admins:         []string{"group:admins@example.com"},
	})
	if err != nil {
		t.Fatalf("NewAdmitter: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("X-Test-Email", "alice@example.com")
	rec := httptest.NewRecorder()
	h := admitter.RequireAdmitted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when group resolver fails, got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After: 5, got %q", got)
	}
}
