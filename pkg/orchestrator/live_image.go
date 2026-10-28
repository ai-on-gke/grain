package orchestrator

import (
	"strings"
	"sync/atomic"
)

// SandboxImageSetter is a sandbox backend whose image can be replaced
// while the daemon runs: the daemon's liveConfig hands it
// model.Config.SandboxImage (or the stock image again, when that is
// cleared) so a custom image named in Settings reaches the next sandbox
// created rather than the next restart. A sandbox already running keeps
// the image it was created from.
//
// The granule backend does not implement it: it asks
// GranuleConfig.SandboxImage on each create instead.
type SandboxImageSetter interface {
	SetImage(image string)
	CurrentImage() string
}

// liveImage is a backend's sandbox image as SetImage last replaced it,
// read without the backend's own mutex so a create in flight never waits
// on a reconcile tick (or the other way round).
type liveImage struct{ v atomic.Pointer[string] }

// get is the image last set, or fallback -- the backend's configured
// image -- when nothing has been.
func (l *liveImage) get(fallback string) string {
	if p := l.v.Load(); p != nil {
		return *p
	}
	return fallback
}

// set replaces the image. An empty one is ignored: every backend refuses
// to create a sandbox from no image, so "" is never a meaningful answer
// to hand one.
func (l *liveImage) set(image string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	l.v.Store(&image)
}

// SandboxInstanceSetter is a Kubernetes sandbox backend whose
// k8s.InstanceLabel value can be replaced while the daemon runs: the
// daemon's liveConfig hands it the deployment's effective id again when
// a system admin edits deployment_id in Settings, so the next pod built
// is labelled -- and the next sweep lists -- under the new one. A pod
// already running keeps the label it was created with.
type SandboxInstanceSetter interface {
	SetInstance(id string)
	CurrentInstance() string
}

// liveInstance is a backend's instance label as SetInstance last
// replaced it, read without the backend's mutex for liveImage's reason.
// Unlike an image, an empty one is a meaningful answer: no label at all.
type liveInstance struct{ v atomic.Pointer[string] }

func (l *liveInstance) get(fallback string) string {
	if p := l.v.Load(); p != nil {
		return *p
	}
	return fallback
}

func (l *liveInstance) set(id string) {
	id = strings.TrimSpace(id)
	l.v.Store(&id)
}
