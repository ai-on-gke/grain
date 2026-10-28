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

package statemachine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// MemoryStore is a concurrency-safe Store for tests and embedded machines.
// Production engines should use a transactional durable implementation.
type MemoryStore struct {
	mu          sync.Mutex
	instances   map[string]Instance
	transitions map[string][]Transition
	events      map[string]map[string]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{instances: make(map[string]Instance), transitions: make(map[string][]Transition),
		events: make(map[string]map[string]bool)}
}

func (s *MemoryStore) Create(_ context.Context, instance Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.instances[instance.ID]; exists {
		return fmt.Errorf("machine instance %q already exists", instance.ID)
	}
	s.instances[instance.ID] = cloneInstance(instance)
	return nil
}

func (s *MemoryStore) Load(_ context.Context, id string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, ok := s.instances[id]
	if !ok {
		return Instance{}, ErrInstanceNotFound
	}
	return cloneInstance(instance), nil
}

func (s *MemoryStore) Append(_ context.Context, expected uint64, tr Transition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, ok := s.instances[tr.InstanceID]
	if !ok {
		return ErrInstanceNotFound
	}
	if instance.Sequence != expected {
		return fmt.Errorf("stale machine sequence: have %d, expected %d", instance.Sequence, expected)
	}
	if tr.Sequence != expected+1 {
		return errors.New("transition sequence is not the next sequence")
	}
	if tr.Definition != instance.Definition {
		return errors.New("transition changes the pinned definition")
	}
	if s.events[tr.InstanceID][tr.Event.ID] {
		return fmt.Errorf("event %q already applied to machine instance %q", tr.Event.ID, tr.InstanceID)
	}
	instance.Sequence, instance.State = tr.Sequence, cloneJSON(tr.State)
	instance.Accepts = slices.Clone(tr.Accepts)
	instance.Lifecycle = tr.Lifecycle
	instance.Attention = tr.Attention
	instance.Presentation = tr.Presentation
	instance.Inputs = cloneInputs(tr.Inputs)
	s.instances[tr.InstanceID] = instance
	tr.State, tr.Effects, tr.Accepts = cloneJSON(tr.State), cloneEffects(tr.Effects), slices.Clone(tr.Accepts)
	tr.Inputs = cloneInputs(tr.Inputs)
	s.transitions[tr.InstanceID] = append(s.transitions[tr.InstanceID], tr)
	if s.events[tr.InstanceID] == nil {
		s.events[tr.InstanceID] = make(map[string]bool)
	}
	s.events[tr.InstanceID][tr.Event.ID] = true
	return nil
}

func (s *MemoryStore) Applied(_ context.Context, instance, eventID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events[instance][eventID], nil
}

func (s *MemoryStore) Transitions(id string) []Transition {
	s.mu.Lock()
	defer s.mu.Unlock()
	source := s.transitions[id]
	result := make([]Transition, len(source))
	for i, tr := range source {
		tr.State, tr.Effects, tr.Accepts = cloneJSON(tr.State), cloneEffects(tr.Effects), slices.Clone(tr.Accepts)
		tr.Inputs = cloneInputs(tr.Inputs)
		result[i] = tr
	}
	return result
}

func cloneInstance(instance Instance) Instance {
	instance.State, instance.Accepts = cloneJSON(instance.State), slices.Clone(instance.Accepts)
	instance.Inputs = cloneInputs(instance.Inputs)
	return instance
}
