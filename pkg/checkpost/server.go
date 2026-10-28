package checkpost

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds what will be read from one request before it is
// refused. A verdict is a few hundred bytes; this is three orders of
// magnitude of headroom and still small enough that a flood of them
// cannot exhaust memory.
//
// It is enforced before the signature is checked, because reading an
// unbounded body in order to decide whether its sender is allowed to
// send one is the wrong order to do those two things in.
const maxBody = 64 << 10

// Server is the HTTP surface: one route to report a verdict, one to read
// a commit's verdicts back, and one to say the service is alive.
//
// Both real routes are signed with the same secret. The read side is
// signed too, which is not obvious -- a verdict is not a secret, and
// grain's own queue is the only thing that reads them -- but the URL
// contains a repository name and a commit sha, and an unauthenticated
// read endpoint would let anybody who could reach this service enumerate
// what a private repository is building. Signing both also means one
// credential to deploy rather than two.
type Server struct {
	store  *Store
	secret string
	now    func() time.Time
	mux    *http.ServeMux
	// legacyReports accepts a report signed the old way, over its
	// timestamp and body only, and logs that it did (verify). True for
	// now, so the workflow `run:` steps already deployed keep reporting
	// while they move to the scheme that also signs method and path; the
	// shell reporter's own test turns it off, so a script that has not
	// moved fails there rather than passing on the fallback.
	legacyReports bool
}

// NewServer wires the routes. secret is what every request is signed
// with; a Server built with an empty one refuses everything, rather than
// accepting everything, because an operator who forgot to set it should
// find out on the first request instead of on the first abuse.
func NewServer(store *Store, secret string) *Server {
	s := &Server{store: store, secret: secret, now: time.Now, mux: http.NewServeMux(), legacyReports: true}
	s.mux.HandleFunc("POST /v1/verdicts", s.report)
	s.mux.HandleFunc("GET /v1/verdicts/{owner}/{repo}/{commit}", s.read)
	s.mux.HandleFunc("GET /healthz", s.health)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// health answers without a signature, and says nothing but that the
// process is up: a load balancer needs an unauthenticated probe, and a
// probe that reported counts or versions would be an unauthenticated
// window into a private deployment.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}

// report records one verdict.
//
// Idempotent by construction: a retry repeats the same answer about the
// same name and commit, which replaces itself (Store.Put), so a
// reporter that retries on a timeout cannot produce two contradictory
// entries. That is what lets the workflow step be a plain `curl
// --retry`. A report Store.Put will not let replace what it holds -- a
// success sent over a failure from the same run attempt -- is 409, with
// the reason, so the reporter's job fails visibly rather than believing
// it cleared a red build.
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	body, ok := s.authenticated(w, r)
	if !ok {
		return
	}
	var v Verdict
	if err := json.Unmarshal(body, &v); err != nil {
		http.Error(w, "checkpost: the body is not a JSON verdict\n", http.StatusBadRequest)
		return
	}
	if err := v.Validate(); err != nil {
		// The reporter is a shell script in somebody's workflow file and
		// this is the only debugging it has, so the field is named; the
		// value it held goes to the log rather than back in the body.
		log.Printf("checkpost: refusing a verdict: %v", err)
		msg := "checkpost: the verdict is not valid"
		var invalid *InvalidError
		if errors.As(err, &invalid) {
			msg = "checkpost: " + invalid.Summary()
		}
		http.Error(w, msg+"\n", http.StatusBadRequest)
		return
	}
	if err := s.store.Put(v); err != nil {
		var superseded *Superseded
		if errors.As(err, &superseded) {
			log.Printf("checkpost: %s %s: %s %s refused: %v", v.Repo, shortSHA(v.Commit), v.Name, v.Conclusion, err)
			http.Error(w, "checkpost: "+err.Error()+"\n", http.StatusConflict)
			return
		}
		log.Printf("checkpost: recording %s %s %s: %v", v.Repo, shortSHA(v.Commit), v.Name, err)
		http.Error(w, "checkpost: could not record the verdict\n", http.StatusInternalServerError)
		return
	}
	log.Printf("checkpost: %s %s: %s %s", v.Repo, shortSHA(v.Commit), v.Name, v.Conclusion)
	w.WriteHeader(http.StatusNoContent)
}

// read answers with everything reported about one commit.
//
// A commit nobody has reported on is an empty list and a 200, not a 404.
// The two are the same thing to the reader -- nothing has been said --
// and a 404 would invite somebody to treat "not found" as an error
// worth retrying rather than as the silence it is.
func (s *Server) read(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticated(w, r); !ok {
		return
	}
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	commit := r.PathValue("commit")
	if !isFullSHA(commit) {
		http.Error(w, "checkpost: the commit must be a full 40-character sha\n", http.StatusBadRequest)
		return
	}
	out := s.store.For(repo, commit)
	if out == nil {
		out = []Verdict{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("checkpost: writing the verdicts for %s %s: %v", repo, shortSHA(commit), err)
	}
}

// authenticated reads the body under its ceiling and verifies the
// signature over it, the method and the path, answering the request itself if either fails.
//
// Every failure is the same 401 with the same sentence, whatever went
// wrong. The caller who deserves a diagnosis is the operator, and they
// have the log; the caller who does not is told only that it did not
// work, because "your signature is wrong" and "your timestamp is stale"
// together tell somebody probing this endpoint which half to keep
// working on.
func (s *Server) authenticated(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if s.secret == "" {
		log.Printf("checkpost: refusing %s %s -- this service was started with no secret, so it "+
			"can authenticate nothing. Set one and restart.", r.Method, r.URL.Path)
		http.Error(w, "checkpost: this service is not configured to accept requests\n",
			http.StatusServiceUnavailable)
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		http.Error(w, "checkpost: the request body is too large\n", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	legacy, err := verify(s.secret, r.Header.Get(SignatureHeader), r.Header.Get(TimestampHeader),
		r.Method, r.URL.Path, body, s.now(), s.legacyReports)
	if err != nil {
		// Logged with the reason and the path, which is where an
		// operator debugging a workflow's first attempt actually looks.
		log.Printf("checkpost: refusing %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "checkpost: the request is not correctly signed\n", http.StatusUnauthorized)
		return nil, false
	}
	if legacy {
		log.Printf("checkpost: accepted %s %s signed without its method and path -- the reporter "+
			"is on the old signing scheme and should move to scripts/checkpost-report.sh's current one",
			r.Method, r.URL.Path)
	}
	return body, true
}

// shortSHA is a commit in the seven characters a person reads, for log
// lines only -- everything stored and looked up is the full sha.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// PruneEvery runs the store's pruning on a ticker until ctx-less stop is
// closed, which is the whole of this service's background work.
//
// A ticker rather than pruning on every write: expiry is a week away and
// a sweep is a rewrite of the file, so doing it on the request path
// would put an unbounded cost on whichever reporter happened to be
// unlucky.
func (s *Server) PruneEvery(d time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := s.store.Prune(); err != nil {
				log.Printf("checkpost: pruning: %v", err)
			}
		}
	}
}

// Repo splits an "owner/name" into its halves, for a caller holding one
// as a single string. Here rather than in the caller because Verdict.Repo
// is this package's own spelling and it should own the parse.
func Repo(full string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(full, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}
