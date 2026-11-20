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

package sqlite_test

import (
	"github.com/ai-on-gke/grain/pkg/model/gateobservationtest"
	"testing"
)

func TestGateObservationsPersistWithoutLifecycleEffects(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.Run(t, s, db)
}

func TestGateObservationReviewRegressions(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunReviewRegressions(t, s, db)
}

func TestHealthGateObservations(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunHealth(t, s, db)
}

func TestHealthGateSourceOrdering(t *testing.T) {
	s, _, _ := openStore(t)
	gateobservationtest.RunHealthSourceOrdering(t, s)
}

func TestHealthGateSourceBounds(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunHealthSourceBounds(t, s, db)
}

func TestGateOutcomeObservations(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunOutcomes(t, s, db)
}

func TestGateChecksPersistWithoutLifecycleEffects(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunChecks(t, s, db)
}

func TestReviewApprovalGateObservations(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunChangeGates(t, s, db)
}

func TestReviewApprovalScopeOverflow(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunChangeScopeOverflow(t, s, db)
}

func TestQualificationObservationsPersistWithoutLifecycleEffects(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunQualifications(t, s, db)
}

func TestObservationFamilyTransitions(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunObservationFamilies(t, s, db)
}

func TestShadowQualificationLifecycleMatrix(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunShadowQualification(t, s, db)
}

func TestShadowQualificationWideGraph(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunWideShadowGraph(t, s, db)
}

func TestShadowQualificationCacheSafety(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunShadowCacheSafety(t, s, db)
}

func TestGateObservationReadOnlyPrecheck(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunReadOnlyPrecheck(t, s, db)
}

func TestPRGateObservationHistoryBudget(t *testing.T) {
	s, db, _ := openStore(t)
	gateobservationtest.RunPRHistoryBudget(t, s, db)
}
