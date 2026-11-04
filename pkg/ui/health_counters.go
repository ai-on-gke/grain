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

package ui

import (
	"log"
	"maps"
	"sort"
	"sync"
)

// HealthCounters is one section of in-process counters a layer built on
// grain registered with RegisterHealthCounters: what it has counted since
// this daemon started, keyed however that layer chose to key them.
type HealthCounters struct {
	Name     string           `json:"name"`
	Counters map[string]int64 `json:"counters"`
}

type healthCounterSource struct {
	id   uint64
	name string
	read func() map[string]int64
}

var healthCounterRegistry struct {
	sync.Mutex
	next    uint64
	sources []healthCounterSource
}

// RegisterHealthCounters adds a section named name to System → Health,
// GET /api/sandboxes (and so `grain health`, and read_deployment_health),
// its counters read from f each time one of them is read. f must be cheap
// and safe to call from any goroutine: it is called on every read, under
// no lock of grain's. The counters are the caller's own, in-process and
// read-only here -- grain stores none of them, so they start again from
// whatever f says when the daemon restarts.
//
// It is the seam a layer that imports grain has for counting things grain
// itself knows nothing about; core never registers one of its own. The
// function it returns removes the section again, and calling it twice is
// harmless. Two registrations under one name are two sections.
func RegisterHealthCounters(name string, f func() map[string]int64) (unregister func()) {
	if f == nil {
		return func() {}
	}
	r := &healthCounterRegistry
	r.Lock()
	r.next++
	id := r.next
	r.sources = append(r.sources, healthCounterSource{id: id, name: name, read: f})
	r.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.Lock()
			defer r.Unlock()
			for i, s := range r.sources {
				if s.id == id {
					r.sources = append(r.sources[:i:i], r.sources[i+1:]...)
					return
				}
			}
		})
	}
}

// healthCounterSections reads every registered source, sorted by name
// (and, under one name, in the order they were registered). A source that
// panics is logged and left out rather than taking the health read with
// it. nil when nothing is registered, so the field is absent from the
// response on a deployment with no layer counting anything.
func healthCounterSections() []HealthCounters {
	r := &healthCounterRegistry
	r.Lock()
	sources := append([]healthCounterSource(nil), r.sources...)
	r.Unlock()
	if len(sources) == 0 {
		return nil
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].name < sources[j].name })
	out := make([]HealthCounters, 0, len(sources))
	for _, s := range sources {
		counters, ok := readHealthCounters(s)
		if !ok {
			continue
		}
		out = append(out, HealthCounters{Name: s.name, Counters: counters})
	}
	return out
}

func readHealthCounters(s healthCounterSource) (counters map[string]int64, ok bool) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("ui: health counters %q panicked: %v", s.name, p)
			counters, ok = nil, false
		}
	}()
	// A copy, so that what is marshalled is the response's own; f is
	// still the one that has to hand back a map nothing else is writing.
	counters = maps.Clone(s.read())
	if counters == nil {
		counters = map[string]int64{}
	}
	return counters, true
}
