package ci

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Gate is how many job sandboxes may exist at once across everything one
// Engine is running -- the deployment-wide bound, as against
// Engine.Concurrency, which bounds one run.
//
// The two are not the same number and cannot be: Service.Start hands
// every request its own Engine.Run goroutine (that method's own doc
// comment: "a caller may run several at once against one Engine"), so a
// per-run semaphore bounds a run and nothing else. Three branches pushed
// at once are three runs, and Concurrency alone would let them hold three
// times as many sandboxes as the deployment has -- which is the same
// arithmetic that made grain CI and dispatch between them able to hold
// twice a deployment's pool (grain/task-296).
//
// Acquire blocks until a job may hold a sandbox and returns the release
// that gives it back. An error -- only ever ctx's -- means this job never
// got one and must not run; release is nil then, and nothing is owed.
// release is safe to call more than once and must be called exactly once
// per successful Acquire, which the engine does with defer.
type Gate interface {
	Acquire(ctx context.Context, req GateRequest) (release func(), err error)
}

// GateRequest is one job's ask of a Gate: what its sandbox will commit,
// what the job is worth against everything else queueing, and where to
// report a wait.
type GateRequest struct {
	// Want is what this job's sandbox will commit -- its shape, resolved
	// to the machine it actually takes (grain/task-173). A gate that only
	// counts ignores it; the deployment's own also holds it against a
	// memory and vCPU budget, which is why the shape is passed rather
	// than looked up. The zero commitment means "nothing to charge", and
	// is what a caller with no shape in hand passes.
	Want model.Commitment
	// Priority is what this job is worth against the others waiting here,
	// asked afresh on every attempt this job makes. nil means
	// PriorityNormal, which is what every job at a gate nobody ranks gets
	// and leaves the order to arrival alone.
	//
	// A func rather than a value because the answer moves while a job
	// queues: a merge queue's head changes as things merge, and a run
	// that had finished nothing when its jobs got in line has usually
	// finished something twenty minutes later. A job held for half an
	// hour is ranked on what is true now.
	Priority func() Priority
	// Wait, when it is not nil, is called each time the reason this job is
	// still queueing changes -- "waiting for a free sandbox", "waiting
	// for memory", "waiting behind higher-priority work" -- so that a job
	// held back is visible as held back rather than merely slow to start.
	// It is never called on a job that is admitted immediately, and it is
	// called with the zero Wait when the job is finally admitted.
	Wait func(Wait)
	// Assigned, when it is not nil, is called once with the id this
	// attempt's charge is filed under in the deployment's ledger --
	// gateWaiter.id, "unique across every gate in the process" -- the
	// moment it joins the queue, before it is known whether admission
	// ever succeeds. A gate knows nothing about runs or tasks; this is
	// the one thing it can hand back that means anything to whatever
	// answers SharedGate.Stuck's question about this same job.
	Assigned func(id string)
	// Adopted is true when this job's sandbox is already running on the host
	// from before a daemon restart: the gate must immediately record its slot
	// and ledger charge without blocking behind the queue.
	Adopted bool
}

// DefaultGatePoll is how often a waiting job re-asks a SharedGate whose
// limit it has not been able to meet. A release, and any other change to
// who is waiting, wakes every waiter immediately, so this is not the
// latency of an ordinary wait: it is only what bounds the cases the gate
// cannot be told about -- its own limit growing, because an operator
// raised this deployment's capacity in Settings while a job waited, and
// the passage of time itself, since a waiter's rank rises with its age. A
// second against a job that takes minutes is not worth a subscription to
// the store.
const DefaultGatePoll = time.Second

// DefaultAgeBoost is how long a waiter has to queue to gain one step of
// rank -- the anti-starvation floor under SharedGate's ordering. Two
// minutes against the PriorityMergeHead scale above means an ordinary job
// overtakes a merge-queue head's jobs after eight minutes of being passed
// over, which is long enough for the ordering to do its work on a suite
// that takes half an hour and short enough that nothing waits out a whole
// afternoon of higher-ranked arrivals.
//
// What a deployment that has chosen nothing gets, rather than the only
// value there is: both this and DefaultReserveAfter below are editable
// settings on the configuration row (model.Limits.CIAgeBoost and
// CIReserveAfter), re-read per admission, and a deployment whose jobs
// queue for hours has a reason to move them.
const DefaultAgeBoost = 2 * time.Minute

// DefaultReserveAfter is how long the budget has to have been refusing a
// waiter before this gate starts holding capacity for it rather than
// admitting smaller jobs past it -- for a deployment that has chosen no
// window of its own. See SharedGate.ReserveAfter.
const DefaultReserveAfter = time.Minute

// SharedGate is the Gate a deployment uses: a counter with a limit that
// is re-read rather than fixed, so a pool resized under a running daemon
// is honoured by the next job rather than at the next restart.
//
// Limit is asked for on every attempt, and is expected to be cheap --
// cmd/grain's own reads it out of the configuration this process has
// already applied (liveConfig.current), not out of the database. Budget
// is read the same way and for the same reason.
//
// Waiters are ordered rather than racing. Every release used to wake
// every waiter and whichever goroutine reached the mutex first won, which
// is not merely arbitrary but biased: admission is two tests, so a job
// needs a hole of its own *shape*, and a stream of small jobs keeps
// slipping through releases a 4 vCPU / 16 GiB job cannot use. The jobs
// that starved were the expensive ones, which are the critical path, and
// they starved until ci.DefaultJobTimeout cancelled them
// (grain/task-316). So a waiter now asks "am I the best-ranked candidate
// that fits?" before it takes -- Priority first, then age -- and a waiter
// the budget has been refusing for ReserveAfter has capacity held for it
// rather than handed to whoever is small enough to use it first.
type SharedGate struct {
	// Limit is how many jobs may hold a sandbox right now, whatever run
	// they belong to. Zero or less is a meaningful answer -- "none, wait".
	//
	// nil means this gate imposes no count of its own, which is what the
	// deployment's gate does: a deployment-wide ceiling on CI jobs is
	// exactly what grain/task-28 removed, leaving the resource budget. A
	// per-run gate (OneRun) always sets it.
	Limit func() int
	// Budget, when set, is how much machine every sandbox on this host may
	// commit between them -- the deployment's whole overcommit-derived
	// budget (model.Limits.Budget), re-read on every attempt so that an
	// operator resizing the deployment is honoured by the next job.
	//
	// It used to be CI's own *share* of that budget, the deployment's total
	// less a standing reservation for every configured agent slot. There is
	// no share and no reservation now: this gate charges its jobs to the
	// same ledger the agent side charges its runs to (Ledger below), and
	// both are admitted against the one total.
	//
	// nil, and a budget that bounds nothing, both leave the counts as the
	// whole rule, which is what every deployment did before a job's shape
	// was counted at all.
	Budget func() model.Budget
	// Ledger, when set, is this deployment's live commitment ledger --
	// what every sandbox that exists right now is charged to, agent runs
	// and CI jobs alike (model.Ledger). A job admitted here charges its
	// shape there under the ledger's own lock, in the same operation that
	// tests the budget, so an agent run starting in the same instant
	// cannot be given the same capacity.
	//
	// nil keeps the gate's own tally of what its jobs have committed,
	// which is what a per-run gate and every test that drives a gate on
	// its own use: correct for the jobs at this gate, and blind to
	// anything else on the host.
	Ledger func() *model.Ledger
	// Default, when set, is the deployment's default sandbox commitment,
	// which a job's own requested shape (`sandbox:`) is held against as a
	// per-dimension floor ("at least"): the backend builds the job a sandbox
	// at least as large in each dimension as both the workflow's ask and the
	// deployment default, so each dimension is charged max(want, Default()).
	// nil leaves the job's own shape as-is (charging nothing for a job that
	// named none), which is the honest answer for a backend that applies no
	// shape.
	Default func() model.Commitment
	// OneRun marks this gate as one run's own concurrency bound rather
	// than the pool every run shares. It changes nothing about how the
	// gate admits: it changes what a job held here is told it is waiting
	// for -- WaitForRun and a sentence about this run, rather than
	// WaitForSlot and a sentence about the deployment -- because the two
	// limits are raised in different places and a job that names the
	// wrong one sends an operator to the wrong setting.
	OneRun bool
	// Poll bounds how long a waiter goes before re-reading Limit; zero
	// means DefaultGatePoll.
	Poll time.Duration
	// AgeBoost, when set, is how long a waiter queues to gain one step of
	// rank. A func rather than a duration for the reason Limit and Budget
	// above are: it is an operator setting (model.Limits.CIAgeBoost), and
	// it is asked for on every attempt, so a boost changed in Settings is
	// honoured by the jobs already queueing rather than by the ones that
	// queue after the next restart.
	//
	// nil, and a func answering zero, both mean DefaultAgeBoost.
	//
	// It is the anti-starvation floor, and the reason a priority order
	// here cannot hold a job back for ever: whatever is ranked above a
	// waiter, the waiter catches up with it by waiting. A negative
	// AgeBoost turns the boost off and leaves a pure priority order,
	// which is only worth reaching for in a test -- it is exactly the
	// starvation this exists to prevent.
	AgeBoost func() time.Duration
	// ReserveAfter, when set, is how long the budget has to have been
	// refusing a waiter before this gate holds released capacity for it:
	// past that window, no waiter ranked below it is admitted, so the hole
	// it needs can fill instead of being spent in slivers. Re-read per
	// attempt like AgeBoost above, and for the same reason.
	//
	// nil and zero both mean DefaultReserveAfter; a negative value turns
	// reservation off, which leaves the size bias this exists to remove --
	// a 16 GiB job at the head of the queue still never runs if capacity
	// only ever frees 4 GiB at a time.
	//
	// It costs a little capacity: while the hole fills, jobs that would
	// have fitted wait. That is the deliberate trade against the
	// alternative -- admitting the largest eligible waiter first, which
	// needs no window but starves small jobs behind a huge one.
	ReserveAfter func() time.Duration
	// Stuck, when set, is asked on a refusal that is about this job's own
	// shape -- never on a count refusal, which is not about
	// shape at all, and never on a refusal every waiter would get alike
	// (take's own aboutShape) -- whether the capacity in this job's way
	// can ever be released: specifically, whether it is held entirely by
	// work that is itself waiting for this very job to answer it, such as
	// a task's own agent sandbox kept alive while the task waits on the
	// CI run this job belongs to.
	//
	// true admits the job exactly the way the Sole rule in
	// model.Budget.Admits does, and for the reason stated there: no
	// amount of waiting will make room that is never coming. The string
	// is what a caller logs to say why, the way Sole's own message is
	// logged inline.
	//
	// jobID is this waiter's own id (gateWaiter.id) -- the one thing this
	// gate has that means anything to a caller that knows about runs and
	// tasks, which this package deliberately does not (see GateRequest's
	// own doc on why it stays that way).
	//
	// nil never admits past a refusal this way, which is every gate
	// except the deployment's own Jobs: a per-run gate's waiters are all
	// one run's own jobs, and a job stuck behind its own run's siblings
	// is not a cycle running it early would break.
	Stuck func(jobID string) (bool, string)
	// AgentWaiting, when set, reports whether an agent sandbox is waiting
	// for room in this deployment's shared sandbox budget. When nil, the
	// gate reads Ledger.AgentWaiting() directly.
	AgentWaiting func() bool
	// Wake, when set, is called when a CI job releases its sandbox while an
	// agent sandbox is waiting for room in the shared budget -- waking the
	// reconcile loop so dispatch can claim the freed capacity immediately.
	Wake func()
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time
	// Log, when set, is called once per job that has to wait, with what
	// it is waiting for -- the one line that tells an operator wondering
	// why a check is slow that the deployment is full rather than the
	// job. nil discards.
	Log func(format string, args ...any)

	mu   sync.Mutex
	held int
	// committed is what the jobs holding this gate have taken out of the
	// budget, tracked here rather than recomputed because a release has
	// to give back exactly what its own Acquire took, whatever the
	// budget or the shapes have done in between.
	//
	// Only used where there is no Ledger. With one, the deployment's own
	// ledger is where a charge lives, because the figure an admission has
	// to be tested against is every sandbox on the host rather than this
	// gate's own.
	committed model.Commitment
	// waiters is everybody queueing here right now, because the order is
	// decided by comparing waiters against each other: a waiter joins
	// before its first attempt and leaves when it is admitted or gives
	// up.
	waiters map[*gateWaiter]struct{}
	// joined numbers the waiters, so that two that arrived in the same
	// instant still have an order and the comparison is total rather than
	// a coin toss.
	joined uint64
	// wake is closed by whichever release drops the count and replaced
	// under mu, so waiters that subscribed before their failed attempt
	// cannot miss the release that happened during it. nil until the
	// first waiter asks for one.
	wake chan struct{}
}

// gateWaiter is one job queueing at a SharedGate, as the other waiters
// see it. Everything on it is read and written under the gate's mutex.
type gateWaiter struct {
	// charge is what admitting this job would commit -- Want, resolved
	// through Default.
	charge model.Commitment
	// id is what this job's charge is filed under in the deployment's
	// ledger: unique across every gate in the process, so a release gives
	// back its own entry and nobody else's.
	id string
	// rank is what the caller last said this job was worth. Refreshed at
	// the top of each of this waiter's own attempts, and stored here
	// rather than re-asked, so that ordering never calls another job's
	// Priority func -- the worst that costs is a rank one poll out of
	// date.
	rank Priority
	// since is when this job started queueing: the age half of its score,
	// and the tiebreak between equal ranks.
	since time.Time
	// seq is the order it arrived in.
	seq uint64
	// short is when the budget first refused this waiter for want of
	// room, and the zero time when the thing holding it is not the
	// budget. It is what ReserveAfter is measured from.
	short time.Time
}

func (g *SharedGate) poll() time.Duration {
	if g.Poll > 0 {
		return g.Poll
	}
	return DefaultGatePoll
}

// gateWindows is the two time-based halves of this gate's ordering as of
// one attempt: how fast a waiter's rank rises, and how long the budget
// has to have been refusing one before capacity is held for it.
//
// Read once per attempt and carried down, rather than asked for again
// inside the comparison, for two reasons. The callbacks behind them are
// the deployment's live configuration and are called outside the mutex,
// exactly as Limit and Budget are; and one attempt ranks every waiter
// against the same boost, so a setting that changes mid-comparison
// cannot make the order disagree with itself.
type gateWindows struct {
	boost   time.Duration
	reserve time.Duration
}

// windows resolves those two, each callback's zero meaning grain's own
// number. Called before the mutex is taken.
func (g *SharedGate) windows() gateWindows {
	w := gateWindows{boost: DefaultAgeBoost, reserve: DefaultReserveAfter}
	if g.AgeBoost != nil {
		if boost := g.AgeBoost(); boost != 0 {
			w.boost = boost
		}
	}
	if g.ReserveAfter != nil {
		if reserve := g.ReserveAfter(); reserve != 0 {
			w.reserve = reserve
		}
	}
	return w
}

func (g *SharedGate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *SharedGate) log(format string, args ...any) {
	if g.Log != nil {
		g.Log(format, args...)
	}
}

// Acquire implements Gate: take one of the deployment's job sandboxes,
// waiting for one to be given back if they are all held -- or for room
// in the resource budget, if what is short is the machine rather than
// the count, or for the jobs this deployment would rather run first to
// have had their turn.
//
// The subscribe-then-try order is what makes the wait safe: a waiter
// takes the channel a release will close *before* it tests the counter,
// so a release landing between the test and the select still wakes it.
// Ordering is layered on top of that rather than replacing it with a
// queue of blocked goroutines -- every waiter is still woken by every
// release, and each one decides for itself whether it is the candidate
// this gate would rather admit (take).
func (g *SharedGate) Acquire(ctx context.Context, req GateRequest) (func(), error) {
	charge := g.charge(req.Want)
	w := g.join(charge)
	if req.Assigned != nil {
		req.Assigned(w.id)
	}
	// Whatever happens next, this job stops being something the others
	// have to rank themselves against -- and the one that was second in
	// line is now first, which is worth waking them for.
	defer g.leave(w)
	if req.Adopted {
		ledger := g.ledger()
		g.mu.Lock()
		var giveBack func()
		if ledger != nil {
			_, giveBack = ledger.Admit(model.Budget{}, model.PoolCI, w.id, w.charge)
		} else {
			g.committed = g.committed.Plus(w.charge)
		}
		g.held++
		rel := g.releaser(w, giveBack)
		g.mu.Unlock()
		return rel, nil
	}
	waited, last := false, Wait{}
	for {
		wake := g.subscribe()
		ledgerWake := g.ledger().Change()
		release, why, ok := g.take(w, rankOf(req.Priority))
		if ok {
			if waited {
				g.log("ci: a job sandbox came free (%d in use)", g.inUse())
				if req.Wait != nil {
					req.Wait(Wait{})
				}
			}
			return release, nil
		}
		if why != last {
			// Said once per *reason*, not once per attempt: a job held
			// first by the count and then by memory has two different
			// things to tell whoever is watching, and a job held by one
			// thing for forty minutes has one.
			waited, last = true, why
			g.log("ci: this job is waiting -- %s", why.Reason)
			if req.Wait != nil {
				req.Wait(why)
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		case <-ledgerWake:
		case <-time.After(g.poll()):
		}
	}
}

// rankOf is what a request's Priority says now, or PriorityNormal where
// the caller ranks nothing. Called outside the gate's mutex, always: this
// is a callback into whatever wired the engine up.
func rankOf(priority func() Priority) Priority {
	if priority == nil {
		return PriorityNormal
	}
	return priority()
}

// charge is what this job costs the budget: each dimension is at least the
// deployment's default (when Default is set) and at least what the job's
// own `sandbox:` declaration asked for.
func (g *SharedGate) charge(want model.Commitment) model.Commitment {
	if g.Default == nil {
		return want
	}
	def := g.Default()
	return model.Commitment{
		CPUs:     max(want.CPUs, def.CPUs),
		MemoryMB: max(want.MemoryMB, def.MemoryMB),
		DiskGB:   max(want.DiskGB, def.DiskGB),
	}
}

// ledgerSeq numbers every acquisition made at any gate in this process,
// so that the id a charge is filed under in the deployment's ledger is
// unique across gates as well as within one.
var ledgerSeq atomic.Uint64

// join puts a job in the waiter set. Every job joins before its first
// attempt, admitted immediately or not, because a job that took a permit
// without ever being visible as a candidate is a job the ordering could
// not have held back.
func (g *SharedGate) join(charge model.Commitment) *gateWaiter {
	now := g.now()
	id := "job-" + strconv.FormatUint(ledgerSeq.Add(1), 10)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiters == nil {
		g.waiters = map[*gateWaiter]struct{}{}
	}
	g.joined++
	w := &gateWaiter{charge: charge, id: id, since: now, seq: g.joined}
	g.waiters[w] = struct{}{}
	return w
}

// leave takes a job out of the waiter set -- admitted, or given up -- and
// wakes the others, since the best-ranked candidate that fits may now be
// one of them and nothing else is going to tell them so.
func (g *SharedGate) leave(w *gateWaiter) {
	g.mu.Lock()
	_, waiting := g.waiters[w]
	delete(g.waiters, w)
	wake := g.wake
	if waiting {
		g.wake = nil
	}
	g.mu.Unlock()
	if waiting && wake != nil {
		close(wake)
	}
}

// take counts one more held sandbox if every bound admits it, and returns
// the release that gives it back -- or why it refused, so a caller can say
// what it is waiting for without asking twice.
//
// rank is what w's caller says it is worth as of this attempt; it is
// recorded on the waiter so that the other waiters can rank themselves
// against it without calling anybody's callback under the mutex.
//
// The order of the two bounds is deliberate. This gate's own count
// first, where it has one (a per-run gate always does); then the budget,
// because a job that has not even reached a count it can see has nothing
// to say about memory yet.
//
// The budget is asked twice on the admitting path, and that is the point
// rather than a redundancy: the first ask is a read, so the ordering
// below can be decided on it, and the second is Ledger.Admit, which tests
// and charges under one lock. Between the two an agent run may take the
// capacity this job was about to, and the second ask is what notices.
func (g *SharedGate) take(w *gateWaiter, rank Priority) (release func(), why Wait, ok bool) {
	limit, counted := g.limit()
	var budget model.Budget
	if g.Budget != nil {
		budget = g.Budget()
	}
	ledger := g.ledger()
	agentWaiting := g.agentWaiting(ledger)
	agentShape := g.agentWaitShape(ledger)
	windows := g.windows()
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	w.rank = rank
	if counted && g.held >= limit {
		// Said as the count, not as a hole: every count slot is the same
		// size, so a gate that is full by the count is holding every job
		// equally and has nothing to say about shapes. What it does not do
		// is forget how long the budget has been refusing this job (short,
		// below) -- a deployment that keeps going count-full for a moment
		// between releases would otherwise reset the reservation clock of
		// the very job the reservation is for.
		return nil, g.countWait(g.held, limit), false
	}
	live := g.live(ledger)
	repair := rank.MaySpendReserve()
	verdict := budget.AdmitsAs(live, w.charge, repair)
	if !verdict.OK {
		// Two things the budget's own refusal cannot say, both of which
		// cost real time to work out from the outside.
		//
		// Who is holding the capacity. Budget.memoryRefusal says "is
		// committed by this deployment's live sandboxes" because a budget
		// knows nothing about pools or teams -- its own doc says whoever
		// refuses the work adds that. This is that caller. Without it a
		// job held by an agent run and a job held by other CI jobs read
		// identically, and the first is the one worth acting on: it is an
		// agent run, possibly the very run that pushed this branch.
		//
		// And whether waiting can help at all. A job whose own shape is
		// larger than the whole budget only ever runs alone (the Sole
		// rule in Budget.Admits), which is said today only once it *is*
		// admitted -- so a job in that state waits with a message implying
		// capacity is coming. Said here instead, on the refusal, where
		// somebody reading it still has a decision to make.
		verdict.Reason = g.annotate(verdict.Reason, budget, ledger, w.charge)
		// aboutShape is whether this refusal is about this job's own
		// *shape* at all, as against one every waiter gets alike (a
		// volume under its free-space floor). short, below, only runs on
		// the first kind, and Stuck is only ever asked on it too: no
		// amount of capacity held back for this waiter, and no amount of
		// work elsewhere finishing on its own, makes a byte of disk
		// appear.
		aboutShape := budget.Admits(live, model.Commitment{}).OK
		if !aboutShape {
			w.short = time.Time{}
		} else if w.short.IsZero() {
			w.short = now
		}
		// Sole's own reasoning -- "no amount of waiting will make room
		// that the budget was never going to have" -- applies just as
		// well to room held by a waiter that will not move until this
		// job runs as it does to room the budget never had at all. Stuck
		// is how a caller that knows about runs and tasks, which this
		// gate does not, answers that for one specific refusal.
		if aboutShape && g.Stuck != nil && budget.AdmitsException(live, w.charge).OK {
			if stuck, reason := g.Stuck(w.id); stuck {
				if release, why, ok := g.commit(w, ledger, budget, true); ok {
					g.log("ci: this job is waiting on capacity that cannot be released -- %s -- "+
						"running it anyway rather than waiting for a release that is not coming", reason)
					return release, why, true
				}
			}
		}
		if g.reserving(w, now, windows.reserve) {
			return nil, holeWait(w.charge, verdict.Reason), false
		}
		return nil, roomWait(verdict.Reason), false
	}
	w.short = time.Time{}
	if !g.OneRun && !repair && (agentWaiting || ledger.AgentWaiting()) {
		if g.Stuck != nil && (agentShape.IsZero() || !budget.Admits(live, agentShape).OK) && budget.AdmitsException(live, w.charge).OK {
			if stuck, reason := g.Stuck(w.id); stuck {
				if release, why, ok := g.commit(w, ledger, budget, true); ok {
					g.log("ci: this job is waiting on capacity that cannot be released -- %s -- "+
						"running it anyway rather than waiting for a release that is not coming", reason)
					return release, why, true
				}
			}
		}
		return nil, agentWait(), false
	}
	// This job fits. Whether it goes is the other question an ordered
	// gate asks: is it the candidate this deployment would rather admit?
	if ahead := g.aheadOf(w, budget, live, now, windows); ahead > 0 {
		return nil, g.orderWait(ahead, limit), false
	}
	if verdict.Sole {
		g.log("ci: this job asks for %s, which is more than this deployment's whole sandbox budget (%s) -- "+
			"running it alone rather than holding it for capacity that is not coming",
			w.charge, budget.Summary())
	}
	// And the charge itself, against the ledger every other sandbox on
	// this host is charged to. A refusal here is the race the read above
	// cannot close: somebody else spent the capacity in between, and this
	// job waits for the next release exactly as if the budget had refused
	// it the first time.
	return g.commit(w, ledger, budget, false)
}

// commit finishes an admission take has already decided to make: the
// ledger charge every other sandbox on this host shares (or this gate's
// own tally, where there is no ledger), and the gate's own counters.
//
// force is what Stuck's own admission needs and the ordinary path never
// sets: it admits under the explicit exception policy
// (model.Ledger.AdmitException) rather than ordinary Admit, so a
// deadlock-breaking job may exceed the budget by at most one sandbox
// while existing commitments are within the cap.
func (g *SharedGate) commit(w *gateWaiter, ledger *model.Ledger, budget model.Budget, force bool) (func(), Wait, bool) {
	w.short = time.Time{}
	var giveBack func()
	if ledger != nil {
		var taken model.BudgetVerdict
		var release func()
		if force {
			taken, release = ledger.AdmitException(budget, model.PoolCI, w.id, w.charge)
		} else {
			taken, release = ledger.AdmitAs(budget, model.PoolCI, w.id, w.charge, w.rank.MaySpendReserve())
		}
		if !taken.OK {
			return nil, roomWait(taken.Reason), false
		}
		giveBack = release
	} else {
		g.committed = g.committed.Plus(w.charge)
	}
	g.held++
	return g.releaser(w, giveBack), Wait{}, true
}

// annotate adds to a budget refusal the two things the budget could not
// know: what is holding the capacity (the ledger's own breakdown, which
// names the pools), and whether this job's shape could ever share the
// host at all.
//
// Purely a message. Nothing here changes what is admitted -- the Sole
// rule already admits an oversized job when it can have the host to
// itself, and this only stops the wait before that from reading like an
// ordinary queue.
func (g *SharedGate) annotate(reason string, budget model.Budget, ledger *model.Ledger, want model.Commitment) string {
	if held := ledger.Describe(); held != "" && held != "nothing" {
		reason += " -- held by " + held
	}
	// Asked against an empty host: OK with Sole set means the only way
	// this shape is ever admitted is with nothing else live.
	if alone := budget.Admits(model.Commitment{}, want); alone.OK && alone.Sole {
		reason += ". This job's own shape is larger than the whole budget, so no release will " +
			"make room for it beside anything else -- it runs only when the host is otherwise empty"
	}
	return reason
}

// limit is this gate's own count bound and whether it has one at all.
func (g *SharedGate) limit() (int, bool) {
	if g.Limit == nil {
		return 0, false
	}
	return g.Limit(), true
}

func (g *SharedGate) ledger() *model.Ledger {
	if g.Ledger == nil {
		return nil
	}
	return g.Ledger()
}

func (g *SharedGate) agentWaiting(ledger *model.Ledger) bool {
	if g.AgentWaiting != nil && g.AgentWaiting() {
		return true
	}
	return ledger.AgentWaiting()
}

func (g *SharedGate) agentWaitShape(ledger *model.Ledger) model.Commitment {
	if shape := ledger.AgentWaitShape(); !shape.IsZero() {
		return shape
	}
	if g.Default != nil {
		return g.Default()
	}
	return model.Commitment{}
}

// live is what an admission here is tested against: every live sandbox on
// the host where this gate shares the deployment's ledger, and this gate's
// own jobs where it does not. Called with g.mu held, which is the one
// place a gate lock is taken before the ledger's -- never the other way
// round.
func (g *SharedGate) live(ledger *model.Ledger) model.Commitment {
	if ledger == nil {
		return g.committed
	}
	return ledger.Live()
}

// aheadOf counts the waiters this gate would rather admit than w: those
// better-ranked ones that fit right now, whose own next attempt takes
// this capacity, and those better-ranked ones that do *not* fit but have
// been refused long enough to be holding capacity back for themselves
// (reserving).
//
// Reservation is the half that ordering alone cannot do. A 16 GiB job at
// the front of the queue still never runs if capacity only ever frees in
// 4 GiB slivers: nothing in an order stops the sliver being spent. So
// once the budget has been refusing a waiter for ReserveAfter, the jobs
// ranked below it wait even though they fit, and the hole fills.
//
// Nothing can be held here for ever, which is the property that makes the
// whole arrangement safe: whoever is best-ranked is by definition ahead of
// nobody, so it is admitted the moment it fits, and every waiter becomes
// best-ranked eventually because rank rises with age (AgeBoost). A
// reservation for a job bigger than the whole budget ends the same way --
// the budget admits such a job as soon as nothing else holds it
// (model.Budget.Admits' Sole rule), and holding the smaller jobs back is
// exactly how it gets there.
func (g *SharedGate) aheadOf(w *gateWaiter, budget model.Budget, live model.Commitment, now time.Time, windows gateWindows) int {
	ahead := 0
	for o := range g.waiters {
		if o == w || !g.better(o, w, now, windows.boost) {
			continue
		}
		if budget.Admits(live, o.charge).OK || g.reserving(o, now, windows.reserve) {
			ahead++
		}
	}
	return ahead
}

// better reports whether o is the waiter to admit before w: the higher
// score wins, and equal scores go in the order they arrived, so a
// deployment under sustained pressure is FIFO among equals rather than a
// race to the mutex.
func (g *SharedGate) better(o, w *gateWaiter, now time.Time, boost time.Duration) bool {
	os, ws := g.score(o, now, boost), g.score(w, now, boost)
	if os != ws {
		return os > ws
	}
	return o.seq < w.seq
}

// score is a waiter's rank as of now: what its caller ranked it, plus one
// step for every AgeBoost it has been queueing. The age term is what
// keeps the bottom of the order from starving -- see AgeBoost -- and it
// is computed here rather than stored so that a waiter asleep between
// attempts still rises.
func (g *SharedGate) score(w *gateWaiter, now time.Time, boost time.Duration) int {
	if boost < 0 {
		return int(w.rank)
	}
	waited := now.Sub(w.since)
	if waited < 0 {
		waited = 0
	}
	return int(w.rank) + int(waited/boost)
}

// reserving reports whether the gate is holding capacity for w: the
// budget has refused it, and has been refusing it for longer than
// ReserveAfter -- or at all, for a waiter ranked to reserve at once
// (Priority.ReservesAtOnce).
func (g *SharedGate) reserving(w *gateWaiter, now time.Time, after time.Duration) bool {
	if after < 0 || w.short.IsZero() {
		return false
	}
	if w.rank.ReservesAtOnce() {
		return true
	}
	return now.Sub(w.short) >= after
}

// countWait is the wait message for a gate that is full by the count
// alone -- the one an operator recognises as max_ci_jobs, or, for a
// per-run gate, as how wide one run is allowed to be (OneRun).
func (g *SharedGate) countWait(held, limit int) Wait {
	if g.OneRun {
		return Wait{
			Cause: WaitForRun,
			Reason: "for one of the " + plural(limit, "sandbox", "sandboxes") + " this run may hold at once; " +
				plural(held, "job of it holds", "jobs of it hold") + " one right now",
		}
	}
	return Wait{
		Cause: WaitForSlot,
		Reason: "for one of the " + plural(limit, "sandbox", "sandboxes") + " this deployment keeps for CI; " +
			plural(held, "job holds", "jobs hold") + " one right now",
	}
}

// roomWait is the wait message for a job the resource budget has refused:
// there is no count holding it, there is not enough machine left.
//
// The budget's own sentence is the whole of it, and that sentence now
// reports every live sandbox on the host against the deployment's whole
// overcommit-derived budget. It used to report CI's own *share* -- the
// total less a standing reservation for every configured agent slot -- and
// the number an operator reached for was the overcommit ratio, because it
// was the only one in the sentence that moved (grain/task-506,
// grain/task-28).
func roomWait(reason string) Wait {
	return Wait{
		Cause:  WaitForRoom,
		Reason: "for room in this deployment's sandbox budget -- " + reason,
	}
}

// orderWait is the wait message for a job that fits and is being held
// anyway because the gate would rather admit somebody else first -- which
// is two different sentences, for the same reason countWait is: the
// deployment's pool holds a job behind other branches' work, and a run's
// own gate (OneRun) holds it behind its own siblings.
//
// A per-run gate is ranked by nothing at all: the jobs queueing at it are
// all one run's, so every waiter there scores the same and the order is
// the order they got in line (Engine.runJobs ranks nothing here). Telling
// such a job it is "behind higher-priority work this deployment would
// rather run first" names a priority nobody set and sends an operator to
// max_ci_jobs when the limit holding it is Concurrency -- so a per-run
// gate names its own width here, exactly as it does when it is full by
// the count.
func (g *SharedGate) orderWait(ahead, limit int) Wait {
	if g.OneRun {
		return Wait{
			Cause: WaitForRun,
			Reason: "for one of the " + plural(limit, "sandbox", "sandboxes") + " this run may hold at once; " +
				plural(ahead, "job of it is", "jobs of it are") + " in line ahead of it",
		}
	}
	return priorityWait(ahead)
}

// priorityWait is the wait message for a job that fits and is being held
// anyway, because this deployment would rather spend the capacity on
// something else first. A job held for forty minutes should say which
// kind of waiting it is doing (grain/task-170, one layer down), and
// "behind other work" is a different answer from "the deployment is
// full".
func priorityWait(ahead int) Wait {
	return Wait{
		Cause: WaitForPriority,
		Reason: "behind higher-priority work; " + plural(ahead, "job", "jobs") +
			" this deployment would rather run first " + verb(ahead, "is", "are") + " queued ahead of it",
	}
}

// agentWait is the wait message for a job held back so that released capacity
// can accumulate for a queued agent sandbox waiting on the shared budget.
func agentWait() Wait {
	return Wait{
		Cause:  WaitForPriority,
		Reason: "waiting behind a queued agent sandbox",
	}
}

// holeWait is the wait message for the job a reservation is being held
// for: it is not waiting for capacity in general, it is waiting for a
// hole of its own shape, and the deployment is keeping smaller jobs out
// while one forms.
func holeWait(want model.Commitment, reason string) Wait {
	return Wait{
		Cause: WaitForHole,
		Reason: "for a hole big enough for it (" + want.String() + "): this deployment is holding capacity " +
			"back for this job rather than letting smaller ones in ahead of it -- " + reason,
	}
}

func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return strconv.Itoa(n) + " " + word
}

func verb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (g *SharedGate) inUse() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

// releaser returns the one-shot release for a sandbox this gate has just
// handed out: the counts here, and the deployment's ledger entry through
// giveBack (nil on a gate with no ledger, whose charge is its own tally).
// sync.Once rather than a bare closure because a release called twice
// would hand a second job capacity nobody gave back, and the engine's own
// defer is not the only thing that could ever call it.
func (g *SharedGate) releaser(w *gateWaiter, giveBack func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			ledger := g.ledger()
			waiting := g.agentWaiting(ledger)
			ledgerWoke := ledger != nil && ledger.AgentWaiting() && ledger.Wake != nil
			if giveBack != nil {
				giveBack()
			}
			g.mu.Lock()
			g.held--
			if giveBack == nil {
				g.committed = g.committed.Minus(w.charge)
			}
			wake := g.wake
			g.wake = nil
			g.mu.Unlock()
			if wake != nil {
				close(wake)
			}
			if waiting {
				if g.Wake != nil {
					g.Wake()
				} else if !ledgerWoke && ledger != nil && ledger.Wake != nil {
					ledger.Wake()
				}
			}
		})
	}
}

// subscribe returns the channel the next release will close.
func (g *SharedGate) subscribe() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wake == nil {
		g.wake = make(chan struct{})
	}
	return g.wake
}
