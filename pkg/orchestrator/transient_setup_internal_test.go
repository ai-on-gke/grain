package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// transientSetupDetail is run 831-35's own detail, the setup failure this
// retry exists for.
const transientSetupDetail = `this run's sandbox could not be prepared: orchestrator: configuring git credentials for run 831-35: ` +
	`orchestrator: configuring git credentials in granule sandbox pod "grain-granule-831-35": gitcredentials: ` +
	`writing remote .git-credentials and .gitconfig: error: unable to upgrade connection: error dialing backend: dial timeout, backstop`

// An ordinary task whose setup fails on the cluster's exec transport is
// suspended -- not counted, its Run now request kept -- and retried after
// 10s, 30s and 90s; only the try after those ends setup-failed.
func TestATransientSetupFailureIsRetriedBeforeItCounts(t *testing.T) {
	store, ctx := openTestStore(t)
	if err := store.PutTask(ctx, stdTask("o")); err != nil {
		t.Fatal(err)
	}
	for i, wait := range transientSetupRetries {
		at := standingNow.Add(time.Duration(i) * time.Hour)
		id := "o-" + string(rune('1'+i))
		requested := at.Add(-time.Second)
		if err := store.ObserveField(ctx, "o", requested, func(o *model.Observation) { o.RunNowAt = &requested }); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: id, TaskID: "o", Attempt: i + 1, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := finishSetupFailed(ctx, store, "o", id, at, transientSetupDetail); err != nil {
			t.Fatal(err)
		}
		resume, err := store.SuspensionResumeAt(ctx, "o")
		if err != nil || resume == nil || !resume.Equal(at.Add(wait)) {
			t.Fatalf("retry %d: resume at = %v, %v; want %v", i+1, resume, err, at.Add(wait))
		}
		if n := failureCount(t, ctx, store, "o"); n != 0 {
			t.Fatalf("retry %d: failure streak = %d, want a transient setup failure to count for nothing", i+1, n)
		}
		obs, err := store.GetObservation(ctx, "o")
		if err != nil {
			t.Fatal(err)
		}
		if obs == nil || obs.RunNowAt == nil {
			t.Fatalf("retry %d: the Run now request was spent, want it kept for the retry", i+1)
		}
	}
	last := standingNow.Add(time.Duration(len(transientSetupRetries)) * time.Hour)
	if err := store.StartRun(ctx, model.Run{ID: "o-9", TaskID: "o", Attempt: 9, StartedAt: last}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := finishSetupFailed(ctx, store, "o", "o-9", last, transientSetupDetail); err != nil {
		t.Fatal(err)
	}
	if n := failureCount(t, ctx, store, "o"); n != 1 {
		t.Fatalf("after the retries, failure streak = %d, want the setup failure counted", n)
	}
	runs, err := store.Runs(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		want := model.SuspendedOutcome
		if r.ID == "o-9" {
			want = model.SetupFailedOutcome
			if !strings.Contains(r.Detail, "exec tunnel is failing") {
				t.Errorf("run %s detail = %q, want it to name the exec tunnel", r.ID, r.Detail)
			}
		}
		if r.Outcome != want {
			t.Errorf("run %s outcome = %q, want %q", r.ID, r.Outcome, want)
		}
	}
}

// A setup failure that is not the transport's -- a repository that does
// not exist, a credential the deployment lacks -- counts at once, as
// before.
func TestAnOrdinarySetupFailureIsNotRetried(t *testing.T) {
	store, ctx := openTestStore(t)
	if err := store.PutTask(ctx, stdTask("o")); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "o-1", TaskID: "o", Attempt: 1, StartedAt: standingNow}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := finishSetupFailed(ctx, store, "o", "o-1", standingNow,
		"this run's sandbox could not be prepared: cloning acme/widgets: fatal: repository not found"); err != nil {
		t.Fatal(err)
	}
	if n := failureCount(t, ctx, store, "o"); n != 1 {
		t.Fatalf("failure streak = %d, want 1", n)
	}
	if resume, err := store.SuspensionResumeAt(ctx, "o"); err != nil || resume != nil {
		t.Fatalf("resume at = %v, %v; want no suspension", resume, err)
	}
}

// One exec-tunnel setup failure is weather; execTunnelAlertRuns of them
// across the deployment within execTunnelAlertWindow raise the
// cluster.exec-tunnel alert, once, bumped rather than repeated.
func TestSeveralExecTunnelSetupFailuresRaiseADeploymentAlert(t *testing.T) {
	store, ctx := openTestStore(t)
	for i := range execTunnelAlertRuns + 1 {
		id := string(rune('a' + i))
		if err := store.PutTask(ctx, stdTask(id)); err != nil {
			t.Fatal(err)
		}
		at := standingNow.Add(time.Duration(i) * time.Minute)
		if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: at}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := finishSetupFailed(ctx, store, id, id+"-1", at, transientSetupDetail); err != nil {
			t.Fatal(err)
		}
		open, err := store.OpenNotification(ctx, ExecTunnelSubject)
		if err != nil {
			t.Fatal(err)
		}
		if raised := open != nil; raised != (i+1 >= execTunnelAlertRuns) {
			t.Fatalf("after %d exec-tunnel setup failures, %s raised = %v", i+1, ExecTunnelSubject, raised)
		}
		if open != nil && open.RaiseCount != i+2-execTunnelAlertRuns {
			t.Fatalf("after %d failures, raise count = %d, want %d", i+1, open.RaiseCount, i+2-execTunnelAlertRuns)
		}
	}
}

// A tunnel that fails only under running agents -- runs 831-33 and
// 831-34's shape, setup fine and run_command failing after the exec
// runner's own retries -- raises the same alert, counted once per run,
// alongside whatever setups it failed.
func TestMidRunExecTunnelFailuresRaiseTheDeploymentAlert(t *testing.T) {
	store, ctx := openTestStore(t)
	if err := finishSetupFailedRun(t, store, "s", standingNow); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"a", "b"} {
		at := standingNow.Add(time.Duration(i+1) * time.Minute)
		if err := store.RecordExecTransportFailure(ctx, model.ExecTransportFailure{
			RunID: id + "-1", TaskID: id, At: at, Calls: 4,
		}); err != nil {
			t.Fatal(err)
		}
		noteExecTunnelRate(ctx, store, at)
		open, err := store.OpenNotification(ctx, ExecTunnelSubject)
		if err != nil {
			t.Fatal(err)
		}
		if raised := open != nil; raised != (i == 1) {
			t.Fatalf("after one setup and %d mid-run failures, %s raised = %v", i+1, ExecTunnelSubject, raised)
		}
		if open != nil && !strings.Contains(open.Body, "2 running agents had tool calls fail (8 calls)") {
			t.Errorf("alert body = %q, want it to count the mid-run failures", open.Body)
		}
	}
}

// finishSetupFailedRun files task id and fails its first run's setup on
// the exec tunnel at at.
func finishSetupFailedRun(t *testing.T, store *model.Store, id string, at time.Time) error {
	t.Helper()
	ctx := t.Context()
	if err := store.PutTask(ctx, stdTask(id)); err != nil {
		return err
	}
	if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1, StartedAt: at}, model.Limits{}); err != nil {
		return err
	}
	return finishSetupFailed(ctx, store, id, id+"-1", at, transientSetupDetail)
}
