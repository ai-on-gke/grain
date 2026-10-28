package ui

import (
	"net/http"
	"time"
)

// restartWatchHold is how long GET /api/restart-watch holds a request
// with nothing to report before answering restarting: false, after
// which the frontend asks again. Under the 30 s or more that the
// proxies grain sits behind (IAP's load balancer, a reverse
// proxy's default) allow a quiet response, so none of them cuts the
// request first and reads as an outage.
const restartWatchHold = 25 * time.Second

// restartWatchRecheck is how often a held watch re-reads
// Config.Restarting, for the half of it ShutdownStarted does not cover:
// an upgrade that is running, which ends in a restart.
const restartWatchRecheck = time.Second

// handleRestartWatch is GET /api/restart-watch: a long poll the frontend
// keeps open so that it hears "restarting" while the daemon can still
// say it. GET /api/config carries the same flag, but the frontend asks
// that every few seconds and a SIGTERM'd daemon with little to drain
// stops answering within milliseconds, so the last /api/config answer
// before the gap almost never said so and the banner read "cannot be
// reached" for an ordinary restart (grain/task-3543). This request is
// already in flight when the shutdown starts, and http.Server.Shutdown
// waits for it to finish, so the answer is delivered before the listener
// goes away.
//
// It answers {"restarting": true} at once if the daemon already is, or
// as soon as Config.ShutdownStarted closes or Config.Restarting turns
// true; {"restarting": false} after restartWatchHold otherwise. A
// deployment with neither wired (grain demo's throwaway UI) has nothing
// to wait on and answers false straight away; the frontend paces its
// own re-asking.
func (s *Server) handleRestartWatch(w http.ResponseWriter, r *http.Request) {
	cfg := s.tasks.Config
	restarting := func() bool { return cfg.Restarting != nil && cfg.Restarting() }
	answer := func(v bool) {
		writeJSON(w, http.StatusOK, map[string]bool{"restarting": v})
	}
	if restarting() {
		answer(true)
		return
	}
	if cfg.ShutdownStarted == nil && cfg.Restarting == nil {
		answer(false)
		return
	}
	hold := time.NewTimer(restartWatchHold)
	defer hold.Stop()
	recheck := time.NewTicker(restartWatchRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-cfg.ShutdownStarted: // a nil channel never fires
			answer(true)
			return
		case <-recheck.C:
			if restarting() {
				answer(true)
				return
			}
		case <-hold.C:
			answer(false)
			return
		case <-r.Context().Done():
			return
		}
	}
}
