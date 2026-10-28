package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/iap/v1"

	"github.com/bwsalmon/grain/pkg/iapreconciler"
)

const (
	testIAPResource = "projects/123456/iap_web/compute/services/grain-ui"
)

type fakeIAPWebClient struct {
	mu       sync.Mutex
	policy   *iap.Policy
	setErr   error
	resource string
	setCalls int
	lastSet  *iap.Policy
}

func (f *fakeIAPWebClient) GetIAPWebIAMPolicy(_ context.Context, resource string) (*iap.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resource = resource
	if f.policy == nil {
		return &iap.Policy{Etag: "e0"}, nil
	}
	return f.policy, nil
}

func (f *fakeIAPWebClient) SetIAPWebIAMPolicy(_ context.Context, resource string, p *iap.Policy) (*iap.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls++
	f.lastSet = p
	if f.setErr != nil {
		return nil, f.setErr
	}
	f.policy = p
	return p, nil
}

type fakeBackendClient struct {
	policy   *compute.Policy
	setCalls int
}

func (f *fakeBackendClient) GetBackendServiceIAMPolicy(context.Context, string, string) (*compute.Policy, error) {
	if f.policy == nil {
		return &compute.Policy{Etag: "e0"}, nil
	}
	return f.policy, nil
}

func (f *fakeBackendClient) SetBackendServiceIAMPolicy(_ context.Context, _, _ string, p *compute.Policy) (*compute.Policy, error) {
	f.setCalls++
	f.policy = p
	return p, nil
}

func accessors(p *iap.Policy) []string {
	for _, b := range p.Bindings {
		if b.Role == iapreconciler.RoleIAPHTTPSResourceAccessor && b.Condition == nil {
			return b.Members
		}
	}
	return nil
}

func TestIAPWebResource(t *testing.T) {
	var cfg Config
	if res, src := IAPWebResource(cfg); res != "" || src != "" {
		t.Errorf("empty = %q, %q; want nothing", res, src)
	}

	cfg.IapResource = testIAPResource
	if res, src := IAPWebResource(cfg); res != cfg.IapResource || src != "iap-resource" {
		t.Errorf("explicit = %q, %q; want %q, \"iap-resource\"", res, src, cfg.IapResource)
	}

	cfg.IapResource = iapreconciler.ResourceNone
	if res, _ := IAPWebResource(cfg); res != "" {
		t.Errorf("none = %q; want nothing", res)
	}
}

func TestReconcileIAPBothTargets(t *testing.T) {
	backend := &fakeBackendClient{}
	web := &fakeIAPWebClient{setErr: errors.New("permission iap.webServiceVersions.setIamPolicy denied")}
	cfg := Config{
		GcpProject:             "bsalmon-gke-dev",
		GcpServiceAccountEmail: "agent@bsalmon-gke-dev.iam.gserviceaccount.com",
		IapBackendService:      "grain-ui-backend",
		IapResource:            testIAPResource,
		IapClient:              backend,
		IapWebClient:           web,
	}

	err := ReconcileIAP(context.Background(), nil, cfg)
	if err == nil || !strings.Contains(err.Error(), "setIamPolicy denied") {
		t.Fatalf("ReconcileIAP = %v; want the IAP resource's failure", err)
	}
	if backend.setCalls != 1 {
		t.Fatalf("backend setCalls = %d; want 1 despite the other target failing", backend.setCalls)
	}
	var members []string
	for _, b := range backend.policy.Bindings {
		if b.Role == iapreconciler.RoleIAPHTTPSResourceAccessor {
			members = b.Members
		}
	}
	if !slices.Equal(members, []string{"serviceAccount:agent@bsalmon-gke-dev.iam.gserviceaccount.com"}) {
		t.Errorf("backend members = %v; want only the desired set", members)
	}

	st := IAPStatus()
	if len(st) != 2 || st[0].Kind != IAPKindBackendService || st[0].LastError != "" || st[1].Kind != IAPKindWebResource || st[1].LastError == "" {
		t.Errorf("status = %+v; want the backend synced and the IAP resource failed", st)
	}

	// Turning both off clears the report.
	cfg.IapBackendService, cfg.IapResource = "", iapreconciler.ResourceNone
	if err := ReconcileIAP(context.Background(), nil, cfg); err != nil {
		t.Fatalf("ReconcileIAP with nothing configured: %v", err)
	}
	if st := IAPStatus(); len(st) != 0 {
		t.Errorf("status = %+v; want none", st)
	}
}

func TestReconcileIAPExplicitResource(t *testing.T) {
	web := &fakeIAPWebClient{}
	cfg := Config{
		GcpProject:             "p",
		IapResource:            testIAPResource,
		GcpServiceAccountEmail: "agent@p.iam.gserviceaccount.com",
		IapWebClient:           web,
	}
	if err := ReconcileIAP(context.Background(), nil, cfg); err != nil {
		t.Fatalf("ReconcileIAP: %v", err)
	}
	if got := accessors(web.lastSet); !slices.Equal(got, []string{"serviceAccount:agent@p.iam.gserviceaccount.com"}) {
		t.Errorf("accessors = %v", got)
	}
}

func TestLogIgnoredRelaySettings(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	// Nothing stored: nothing logged.
	LogIgnoredRelaySettings(dir, nil, logf)
	if len(lines) != 0 {
		t.Fatalf("lines = %v; want none when nothing is stored", lines)
	}

	// Stored relay-settings.json + secret relay/token: one log line.
	if err := os.WriteFile(filepath.Join(dir, "relay-settings.json"), []byte(`{"url":"https://relay.example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	secretsStore := OpenSecrets(dir)
	if err := secretsStore.Set("relay", "token", []byte("secret-val")); err != nil {
		t.Fatal(err)
	}
	LogIgnoredRelaySettings(dir, secretsStore, logf)
	if len(lines) != 1 {
		t.Fatalf("lines = %v; want exactly one log line", lines)
	}
	if !strings.Contains(lines[0], "relay-settings.json") || !strings.Contains(lines[0], "secret relay/token") || !strings.Contains(lines[0], "the rendezvous relay has been removed") {
		t.Errorf("log line = %q; want both relay-settings.json and secret relay/token mentioned as ignored", lines[0])
	}
}
