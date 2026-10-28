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

package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEnoughTimeToRetryReadsTheRunsOwnDeadline(t *testing.T) {
	if !EnoughTimeToRetry(context.Background(), time.Hour) {
		t.Error("a context with no deadline refused a retry")
	}
	roomy, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if !EnoughTimeToRetry(roomy, BaseTransientBackoff) {
		t.Error("an hour of runtime refused a 30-second backoff")
	}
	tight, cancelTight := context.WithTimeout(context.Background(), BaseTransientBackoff+time.Second)
	defer cancelTight()
	if EnoughTimeToRetry(tight, BaseTransientBackoff) {
		t.Error("a run with a second left after the backoff allowed a retry")
	}
}

func TestTransientBackoffBacksOff(t *testing.T) {
	if got := TransientBackoff(1); got != BaseTransientBackoff {
		t.Errorf("TransientBackoff(1) = %v, want %v", got, BaseTransientBackoff)
	}
	if got := TransientBackoff(2); got != 2*BaseTransientBackoff {
		t.Errorf("TransientBackoff(2) = %v, want %v", got, 2*BaseTransientBackoff)
	}
}

// The three ways the policy says no, each of which is one of the bounds
// it exists to be: the budget, the run's own deadline, and a run
// cancelled while it waits.
func TestWaitBeforeRetryIsBounded(t *testing.T) {
	slept := []time.Duration{}
	p := RetryPolicy{
		Retries: 2,
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	if !p.WaitBeforeRetry(context.Background(), 1) || !p.WaitBeforeRetry(context.Background(), 2) {
		t.Fatal("the policy refused a retry inside its own budget")
	}
	if p.WaitBeforeRetry(context.Background(), 3) {
		t.Error("a third retry was allowed against a budget of two")
	}
	if len(slept) != 2 || slept[0] != BaseTransientBackoff || slept[1] != 2*BaseTransientBackoff {
		t.Errorf("waits = %v, want the doubling backoff and nothing for the refused retry", slept)
	}

	tight, cancel := context.WithTimeout(context.Background(), BaseTransientBackoff)
	defer cancel()
	if p.WaitBeforeRetry(tight, 1) {
		t.Error("a retry was allowed with no runtime left to spend on it")
	}

	cancelled := RetryPolicy{
		Retries: 2,
		Sleep:   func(context.Context, time.Duration) error { return errors.New("run cancelled") },
	}
	if cancelled.WaitBeforeRetry(context.Background(), 1) {
		t.Error("a run cancelled during the backoff was still retried")
	}
}

// The zero value retries nothing: a Framework that has not opted in gets
// exactly the behaviour it had before this policy existed.
func TestTheZeroRetryPolicyRetriesNothing(t *testing.T) {
	if (RetryPolicy{}).WaitBeforeRetry(context.Background(), 1) {
		t.Error("the zero RetryPolicy allowed a retry")
	}
}
