package model

import "strings"

// DerivedFrom is the task t was filed from, "" for one grain or a person
// filed on its own account: the task whose run proposed it
// (LinkProposedBy -- reviews and pull-request triggers carry it too), the
// task whose change it ports (LinkPortOf), or else the task its WorkOrigin
// names, when that is another task's tree. A task with none is the root
// of its own requester chain; local policy takes an empty Creator as
// grain's own only on such a root (policy.RequesterOf).
func (t Task) DerivedFrom() string {
	for _, kind := range []LinkKind{LinkProposedBy, LinkPortOf} {
		for _, l := range t.Links {
			if l.Kind == kind && strings.TrimSpace(l.Target) != "" {
				return strings.TrimSpace(l.Target)
			}
		}
	}
	if id := t.WorkOrigin.Task(); id != "" && id != t.ID {
		return id
	}
	return ""
}

// InheritRequester records on derived the requester of the task it was
// filed from: source's Creator, which for a derived source is already
// its own root's. A task an
// external requester's work went on to file -- by proposal, port, review,
// repair or trigger -- is that requester's, and local policy judges it
// so; left empty it would read as grain's own and skip the approval an
// external request needs (audit 1's GSA1-02).
//
// What it does not carry is approval: whatever approved source approved
// that task, and a derived task of an external requester needs its own.
func InheritRequester(derived *Task, source Task) {
	derived.Creator = strings.TrimSpace(source.Creator)
}
