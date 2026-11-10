package cpa

import (
	"testing"
	"time"
)

func TestWeightedFillSplitsPlanAndStudio(t *testing.T) {
	p := NewPlugin(nil)
	created := 0
	for i := 0; i < 40; i++ {
		if p.shouldRouteToStudio("step-5-preview") {
			created++
		}
	}
	// Default weights are plan 3 : studio 1, so studio gets about 25%.
	if created < 6 || created > 14 {
		t.Fatalf("studio picks = %d of 40, want about 10", created)
	}
}

func TestPlanBlockShiftsAllTrafficToStudio(t *testing.T) {
	p := NewPlugin(nil)
	p.markModelBlocked("step-5-preview", time.Now().Add(time.Minute))
	for i := 0; i < 10; i++ {
		if !p.shouldRouteToStudio("step-5-preview") {
			t.Fatalf("plan blocked but attempt %d still routed to plan", i)
		}
	}
}

func TestStudioBlockShiftsTrafficBackToPlan(t *testing.T) {
	p := NewPlugin(nil)
	p.markStudioBlocked("step-5-preview")
	for i := 0; i < 10; i++ {
		if p.shouldRouteToStudio("step-5-preview") {
			t.Fatalf("studio blocked but attempt %d still routed to studio", i)
		}
	}
}
