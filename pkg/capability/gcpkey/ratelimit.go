package gcpkey

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/googleapi"

	"github.com/bwsalmon/grain/pkg/model"
)

// The IAM API meters key creation and deletion against one per-project
// "Write requests per minute" quota. A deployment whose CI handed a key
// to every job of a suite ran straight into it (grain/task-281): every
// mint answered 429, every job's run ended errored, and every revoke of
// a key already minted answered 429 too and left the key to count
// against the account's ten. What follows is how this package treats
// that answer instead: a mint that is rate limited is a temporary
// exhaustion the caller waits out (model.ResourceExhaustedError, which
// CI admission and dispatch both queue on rather than fail), and a
// revoke that is rate limited is retried before it gives up.

// rateLimitRetryMin and rateLimitRetrySpread are how long a rate-limited
// mint asks its caller to wait: at least the minimum, plus a random part
// of the spread so that every job refused in the same minute does not
// come back in the same second and be refused again. The quota is per
// minute, so anything much shorter is spent being refused.
const (
	rateLimitRetryMin    = 30 * time.Second
	rateLimitRetrySpread = 30 * time.Second
)

// revokeRetryDelays are the waits between attempts at deleting one key
// while IAM answers 429: four more attempts over about half a minute,
// well inside the two minutes a CI job's release is given
// (pkg/daemon ciReleaseTimeout). A key still standing after them is left
// to Reap, which deletes it once it is past the trailing edge.
var revokeRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// sleep waits d or until ctx is done. A variable so tests need not wait.
var sleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// jitter is the random part of a rate-limited mint's wait. A variable so
// tests can pin it.
var jitter = func(spread time.Duration) time.Duration {
	if spread <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(spread)))
}

// isRateLimited reports whether err is GCP refusing a call for quota or
// rate -- HTTP 429, or the RESOURCE_EXHAUSTED status it carries -- as
// opposed to the FAILED_PRECONDITION that means an account is at its
// ten-key cap (explainCreateFailure), which waiting a minute does not
// fix.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		if apiErr.Code == http.StatusTooManyRequests {
			return true
		}
		if strings.Contains(apiErr.Body, "RESOURCE_EXHAUSTED") {
			return true
		}
	}
	text := err.Error()
	return strings.Contains(text, "Error 429") ||
		strings.Contains(text, "RESOURCE_EXHAUSTED") ||
		strings.Contains(text, "rateLimitExceeded")
}

// rateLimitedMint is a mint IAM refused for quota, as the temporary
// exhaustion it is: the caller queues the work and asks again after
// RetryAfter rather than failing it.
func rateLimitedMint(account string, err error) *model.ResourceExhaustedError {
	return &model.ResourceExhaustedError{
		Resource:   "the IAM API's write quota",
		RetryAfter: rateLimitRetryMin + jitter(rateLimitRetrySpread),
		Err:        fmt.Errorf("gcpkey: minting a key for %s: %w", account, err),
	}
}

// deleteKeyRetrying is Minter.DeleteKey, attempted again after each of
// revokeRetryDelays for as long as IAM answers that it is rate limited.
// Any other answer, success or failure, is returned at once.
func deleteKeyRetrying(ctx context.Context, minter Minter, account, id string) error {
	err := minter.DeleteKey(ctx, account, id)
	for _, delay := range revokeRetryDelays {
		if !isRateLimited(err) {
			return err
		}
		if serr := sleep(ctx, delay); serr != nil {
			return err
		}
		err = minter.DeleteKey(ctx, account, id)
	}
	return err
}
