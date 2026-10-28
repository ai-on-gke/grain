package gcpkey

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/googleapi"

	"github.com/ai-on-gke/grain/pkg/model"
)

// iamQuota429 is the refusal grain/task-281's daemon log carried for
// every mint and every revoke once CI ran into the IAM write quota.
func iamQuota429() error {
	return &googleapi.Error{
		Code: http.StatusTooManyRequests,
		Message: "Quota exceeded for quota metric 'Write requests' and limit " +
			"'Write requests per minute' of service 'iam.googleapis.com'",
	}
}

// noWait stubs the package's clock and jitter for one test, recording
// the waits asked for.
func noWait(t *testing.T) *[]time.Duration {
	t.Helper()
	var waited []time.Duration
	oldSleep, oldJitter := sleep, jitter
	sleep = func(ctx context.Context, d time.Duration) error {
		waited = append(waited, d)
		return ctx.Err()
	}
	jitter = func(time.Duration) time.Duration { return 7 * time.Second }
	t.Cleanup(func() { sleep, jitter = oldSleep, oldJitter })
	return &waited
}

func TestIsRateLimited(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", iamQuota429(), true},
		{"wrapped 429", errors.Join(errors.New("gcpkey: deleting key k"), iamQuota429()), true},
		{"RESOURCE_EXHAUSTED body", &googleapi.Error{Code: 400, Body: `{"error":{"status":"RESOURCE_EXHAUSTED"}}`}, true},
		{"key cap is not a rate limit", &googleapi.Error{Code: 400, Message: "Precondition check failed.", Body: "FAILED_PRECONDITION"}, false},
		{"403", &googleapi.Error{Code: http.StatusForbidden}, false},
		{"plain", errors.New("no such key"), false},
	} {
		if got := isRateLimited(tc.err); got != tc.want {
			t.Errorf("%s: isRateLimited = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A mint IAM refuses for quota is a temporary exhaustion with a wait on
// it -- which CI admission queues the job on -- not an error that ends
// the run, and the other accounts are not tried, since the quota is the
// project's.
func TestMaterializeRateLimitedIsATemporaryExhaustion(t *testing.T) {
	noWait(t)
	minter := newFakeMinter()
	minter.createErr = iamQuota429()
	p := testProvider(minter)
	p.Config.ServiceAccountEmail = "grain-sb-agent-@example-project.iam.gserviceaccount.com"
	p.Config.ServiceAccountCount = 3
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "material"}}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	exhausted, ok := model.ResourceExhausted(err)
	if !ok {
		t.Fatalf("Materialize error = %v, want a model.ResourceExhaustedError", err)
	}
	if want := rateLimitRetryMin + 7*time.Second; exhausted.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v (minimum plus jitter)", exhausted.RetryAfter, want)
	}
	if !isRateLimited(err) {
		t.Errorf("error %v lost GCP's own 429", err)
	}
	if len(minter.accountsSeen) != 1 {
		t.Errorf("CreateKey tried %v, want one account: the quota is per project", minter.accountsSeen)
	}
}

// flakyDeleter answers 429 to its first failures deletes, then delegates.
type flakyDeleter struct {
	*fakeMinter
	failures int
	calls    int
}

func (f *flakyDeleter) DeleteKey(ctx context.Context, account, id string) error {
	f.calls++
	if f.calls <= f.failures {
		return iamQuota429()
	}
	return f.fakeMinter.DeleteKey(ctx, account, id)
}

func TestRevokeRetriesARateLimitedDelete(t *testing.T) {
	waited := noWait(t)
	inner := newFakeMinter()
	inner.keys["key-a"] = KeyInfo{ID: "key-a", CreatedAt: time.Now()}
	minter := &flakyDeleter{fakeMinter: inner, failures: 2}
	p := testProvider(inner)
	p.NewMinter = func(context.Context, string) (Minter, error) { return minter, nil }
	creds := &fakeCredentials{material: map[string]string{"c": "material"}}

	err := p.Revoke(context.Background(), testContext(creds, time.Now()), model.Lease{
		Resource: "key-a", MintedBy: model.CredentialRef{Name: "c"},
	})
	if err != nil {
		t.Fatalf("Revoke = %v, want the third attempt to delete the key", err)
	}
	if _, still := inner.keys["key-a"]; still {
		t.Error("key-a should have been deleted")
	}
	if minter.calls != 3 || len(*waited) != 2 {
		t.Errorf("calls = %d, waits = %v; want 3 calls with 2 waits between them", minter.calls, *waited)
	}
}

// Still rate limited after every retry: the error says so, and the
// other accounts are not asked -- each would be one more refused write
// against the same project quota.
func TestRevokeGivesUpOnAPersistentRateLimitWithoutTryingEveryAccount(t *testing.T) {
	noWait(t)
	inner := newFakeMinter()
	minter := &flakyDeleter{fakeMinter: inner, failures: 1000}
	p := testProvider(inner)
	p.Config.ServiceAccountEmail = "grain-sb-agent-@example-project.iam.gserviceaccount.com"
	p.Config.ServiceAccountCount = 3
	p.NewMinter = func(context.Context, string) (Minter, error) { return minter, nil }
	creds := &fakeCredentials{material: map[string]string{"c": "material"}}

	err := p.Revoke(context.Background(), testContext(creds, time.Now()), model.Lease{
		Resource: "key-a", MintedBy: model.CredentialRef{Name: "c"},
	})
	if !isRateLimited(err) {
		t.Fatalf("Revoke = %v, want the rate limit reported", err)
	}
	if want := 1 + len(revokeRetryDelays); minter.calls != want {
		t.Errorf("DeleteKey calls = %d, want %d (one account's attempts, no others)", minter.calls, want)
	}
}
