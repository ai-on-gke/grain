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

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

// finishLoginRetries are the waits before each new try of a finish whose
// forge calls -- reading the pushed branch, finding or opening its pull
// request -- were refused because the grain host's own login for the
// forge has lapsed. Such a login often lapses on and off (a bridge whose
// helper loses its login for a minute), so the finish is tried again a
// few times before the task is parked on it. Tests shorten it.
var finishLoginRetries = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}

// finishLoginLapsed reports whether err -- a finish stage's -- is a forge
// refusing grain because a login the host itself holds has lapsed, and the
// sentence that says which login and what renews it. That is a forge
// call answered with its login page (gitproxy.LoginRedirectError), or one
// whose words carry the git proxy's marker (gitproxy.LoginLapsed) after
// crossing a process as text.
func finishLoginLapsed(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var lapsed *hostLoginLapsedError
	if errors.As(err, &lapsed) {
		return lapsed.Said, true
	}
	if login, ok := gitproxy.AsLoginRedirect(err); ok {
		return login.Error(), true
	}
	return gitproxy.LoginLapsed(err.Error())
}

// salvagePushedBranchRetryingLogin is salvagePushedBranch tried again
// after each of finishLoginRetries while it fails on a lapsed host login
// (finishLoginLapsed). salvagePushedBranch is safe to repeat: it reads the
// branch again and finds a pull request an earlier try opened rather than
// opening a second. Anything else it fails on is handed back at once.
func salvagePushedBranchRetryingLogin(ctx context.Context, store *model.Store, client github.Client,
	task model.Task, now time.Time) (bool, error) {

	for attempt := 0; ; attempt++ {
		handled, err := salvagePushedBranch(ctx, store, client, task, now)
		said, lapsed := finishLoginLapsed(err)
		if !lapsed || attempt >= len(finishLoginRetries) {
			return handled, err
		}
		wait := finishLoginRetries[attempt]
		log.Printf("orchestrator: task %s: finishing its pushed branch was refused on a lapsed host login (%s); trying again in %s",
			task.ID, said, wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return handled, err
		case <-timer.C:
		}
	}
}

// noteFinishEnding is noteFinishFailure, but for a finish refused on a
// lapsed host login (finishLoginLapsed): that is the host's doing, not
// the task's, and every task on the host meets it until a person renews
// the login. So the run is recorded as model.EnvironmentOutcome -- off the
// task's failure streak -- and the task parked on what to run
// (stopOnLapsedLoginAtFinish), the way a setup clone refused on the same
// login is (setupEnding). What the run pushed stays on the remote, so the
// next dispatch only has to finish it. A park that worked answers nil, as
// an empty branch's ending does, so nothing writes "finish-failed" over
// it on the way out.
func noteFinishEnding(ctx context.Context, store *model.Store, task model.Task, runID string, err error, now time.Time) error {
	said, lapsed := finishLoginLapsed(err)
	if !lapsed || store == nil {
		return noteFinishFailure(ctx, store, runID, err)
	}
	noteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runCleanupTimeout)
	defer cancel()
	detail := finishLoginLapsedDetail(task, said)
	if serr := store.SetRunOutcome(noteCtx, runID, model.EnvironmentOutcome, detail); serr != nil {
		return errors.Join(err, fmt.Errorf("orchestrator: recording run %s's lapsed host login: %w", runID, serr))
	}
	if perr := stopOnLapsedLoginAtFinish(noteCtx, store, task, detail, now); perr != nil {
		return errors.Join(err, perr)
	}
	return nil
}

// finishLoginLapsedDetail is the run's detail, and the sentence the task
// is parked on, for a finish refused on a lapsed host login.
func finishLoginLapsedDetail(task model.Task, said string) string {
	host := "its forge"
	if task.Target != nil {
		host = task.Target.GitHost()
	}
	return fmt.Sprintf("its pushed branch could not be turned into a pull request because the grain host's own login for %s "+
		"has lapsed, which only renewing that login fixes; the branch is left pushed. The forge said: %s",
		host, strings.TrimRight(said, "."))
}

// stopOnLapsedLoginAtFinish parks task after a run whose work is pushed
// but whose finish the forge refused on the host's lapsed login
// (noteFinishEnding): stopOnExpiredHostCredentials's park, opening with
// what the run did get done.
func stopOnLapsedLoginAtFinish(ctx context.Context, store *model.Store, task model.Task, why string, now time.Time) error {
	log.Printf("orchestrator: task %s's finish was refused on a lapsed host login; parking it rather than retrying", task.ID)
	return parkOnHostCredentials(ctx, store, task,
		fmt.Sprintf("This task's run finished, but %s.", strings.TrimRight(why, ".")), now)
}
