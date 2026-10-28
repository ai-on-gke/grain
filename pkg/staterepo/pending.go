package staterepo

// pending.go answers the one question a writer that has the daemon
// stopped has to ask before it writes: would the next start import
// settings out of this repository, over what is about to go into the
// database?
//
// It exists for `grain repo-case` (pkg/cli/repocase.go), which rewrites
// the settings tables in place on a stopped deployment. The repository is
// authoritative for those tables, deliberately and for good reasons --
// so a rewrite made while the repository holds a commit this host has not
// taken up is a rewrite the next start reverses, in exactly those tables
// and in no others. Nothing in Load can see the difference between that
// and any other merged change arriving; the only place it is visible is
// before the write, which is here.
//
// Nothing here fetches, and nothing here touches the working tree. A
// caller asking this is on its way to write to the database, and a pull
// that fast-forwarded the tree underneath it would answer the question by
// making it true.

import "context"

// PendingImport is what a start on this host would take out of the state
// repository, as far as it can be told without fetching.
type PendingImport struct {
	// Pending is the whole answer: a start would import out of the
	// repository, replacing this database's settings tables with what the
	// dump holds -- or, on a host that has never loaded this working
	// tree, replacing every table.
	Pending bool
	// Commit is the commit that would be imported, where there is one to
	// name: the working tree's HEAD, or the tip origin reports for a
	// commit this clone has not fetched. "" for a working tree with no
	// commits in it at all.
	Commit string
	// OnRemote says that what is pending is on the remote rather than in
	// this working tree -- a pull request merged since this host last
	// pulled -- which the next start's own Pull brings down before it
	// imports.
	OnRemote bool
	// RemoteUnknown says the remote is configured and could not be asked:
	// a network blip, a credential that has expired. Pending is then only
	// what the working tree alone says, and a merge waiting on the remote
	// is neither ruled in nor out. A local-only deployment leaves this
	// false -- there is no remote to have failed to reach.
	RemoteUnknown bool
}

// PendingImport reports whether Load would import anything if this host
// started now.
//
// It asks the same two things Load asks, in the same order.
//
// The marker first, against the working tree: a HEAD that is not the
// commit this host recorded as loaded is a change already sitting here
// waiting to be imported, and no marker at all is a tree this host has
// never loaded, where the import is the whole repository rather than the
// settings out of it. Both are pending. A repository with no dump in it
// is neither, whatever the marker says: a start seeds that one from the
// database rather than importing from it.
//
// Then the remote, in one ls-remote and without fetching an object: a
// merged pull request that this host has not pulled yet is as pending as
// one it has, because the start that would undo the caller's write pulls
// before it decides anything.
func (r *Repo) PendingImport(ctx context.Context) (PendingImport, error) {
	head, err := r.Head(ctx)
	if err != nil {
		return PendingImport{}, err
	}
	marker, err := r.loadedHead(ctx)
	if err != nil {
		return PendingImport{}, err
	}
	if HasDump(r.Dir()) && (marker == "" || marker != head) {
		return PendingImport{Pending: true, Commit: head}, nil
	}
	remote := r.remoteState(ctx)
	if remote.known && remote.ahead {
		return PendingImport{Pending: true, Commit: remote.head, OnRemote: true}, nil
	}
	return PendingImport{RemoteUnknown: r.cfg.Remote != "" && !remote.known}, nil
}
