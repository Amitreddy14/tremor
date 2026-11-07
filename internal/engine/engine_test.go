package engine

import (
	"errors"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestKillCount(t *testing.T) {
	tests := []struct {
		name             string
		total, requested int
		policy           SafetyPolicy
		want             int
		wantErr          bool
	}{
		{"one of three", 3, 1, SafetyPolicy{MaxAffectedPercent: 50}, 1, false},
		{"capped by percent", 3, 5, SafetyPolicy{MaxAffectedPercent: 50}, 1, false},
		{"four pods half", 4, 5, SafetyPolicy{MaxAffectedPercent: 50}, 2, false},
		{"zero requested means one", 5, 0, SafetyPolicy{MaxAffectedPercent: 50}, 1, false},
		{"default percent", 4, 4, SafetyPolicy{}, 2, false},
		{"single pod refused", 1, 1, SafetyPolicy{MaxAffectedPercent: 50}, 0, true},
		{"single pod allowed", 1, 1, SafetyPolicy{MaxAffectedPercent: 50, AllowTotalOutage: true}, 1, false},
		{"spare one at 100 percent", 3, 3, SafetyPolicy{MaxAffectedPercent: 100}, 2, false},
		{"total outage at 100 percent", 3, 3, SafetyPolicy{MaxAffectedPercent: 100, AllowTotalOutage: true}, 3, false},
		{"two pods one kill", 2, 1, SafetyPolicy{MaxAffectedPercent: 50}, 1, false},
		{"percent above 100 clamps", 3, 3, SafetyPolicy{MaxAffectedPercent: 500, AllowTotalOutage: true}, 3, false},
		{"no pods", 0, 1, SafetyPolicy{}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := KillCount(tt.total, tt.requested, tt.policy)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrNoSafeTarget) {
				t.Fatalf("err = %v, want ErrNoSafeTarget", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPick(t *testing.T) {
	in := []string{"c", "a", "b", "d"}
	orig := append([]string(nil), in...)

	a := Pick(in, 2, rand.New(rand.NewSource(7)))
	b := Pick(in, 2, rand.New(rand.NewSource(7)))
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same seed gave %v and %v", a, b)
	}
	if len(a) != 2 {
		t.Fatalf("len = %d, want 2", len(a))
	}
	if !reflect.DeepEqual(in, orig) {
		t.Fatalf("input mutated: %v", in)
	}
	if got := Pick(in, 10, rand.New(rand.NewSource(1))); len(got) != 4 {
		t.Fatalf("over-asking returned %d, want 4", len(got))
	}
}

func TestValidateSelector(t *testing.T) {
	if err := ValidateSelector(nil); err == nil {
		t.Fatal("empty selector accepted")
	}
	if err := ValidateSelector(map[string]string{"app": "x"}); err != nil {
		t.Fatalf("valid selector rejected: %v", err)
	}
}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	cfg := Config{RecoveryTimeout: 60 * time.Second, BaselineTimeout: 30 * time.Second}
	healthy := Observation{Now: now, StartedAt: now, TotalPods: 3, ReadyPods: 3}

	tests := []struct {
		name       string
		phase      Phase
		obs        Observation
		cfg        Config
		wantNext   Phase
		wantAction Action
		wantRecov  time.Duration
	}{
		{"pending moves to baseline", PhasePending, healthy, cfg, PhaseBaselineCheck, ActionNone, 0},
		{"empty phase moves to baseline", "", healthy, cfg, PhaseBaselineCheck, ActionNone, 0},
		{"healthy baseline plans", PhaseBaselineCheck, healthy, cfg, PhaseInjecting, ActionPlan, 0},
		{"no pods waits", PhaseBaselineCheck, Observation{Now: now, StartedAt: now}, cfg, PhaseBaselineCheck, ActionNone, 0},
		{
			"unhealthy baseline times out", PhaseBaselineCheck,
			Observation{Now: now.Add(31 * time.Second), StartedAt: now, TotalPods: 3, ReadyPods: 2},
			cfg, PhaseFailed, ActionNone, 0,
		},
		{"dry run completes", PhaseBaselineCheck, healthy, Config{BaselineTimeout: 30 * time.Second, DryRun: true}, PhaseCompleted, ActionDryRun, 0},
		{"injecting injects", PhaseInjecting, healthy, cfg, PhaseRecovering, ActionInject, 0},
		{
			"recovering waits while pod missing", PhaseRecovering,
			Observation{Now: now.Add(5 * time.Second), InjectedAt: now, TotalPods: 2, ReadyPods: 2, BaselineReady: 3},
			cfg, PhaseRecovering, ActionNone, 0,
		},
		{
			"recovering waits while affected pod present", PhaseRecovering,
			Observation{Now: now.Add(2 * time.Second), InjectedAt: now, TotalPods: 3, ReadyPods: 3, BaselineReady: 3, AffectedStillPresent: true},
			cfg, PhaseRecovering, ActionNone, 0,
		},
		{
			"recovered completes with elapsed time", PhaseRecovering,
			Observation{Now: now.Add(7 * time.Second), InjectedAt: now, TotalPods: 3, ReadyPods: 3, BaselineReady: 3},
			cfg, PhaseCompleted, ActionNone, 7 * time.Second,
		},
		{
			"recovery timeout fails", PhaseRecovering,
			Observation{Now: now.Add(61 * time.Second), InjectedAt: now, TotalPods: 3, ReadyPods: 2, BaselineReady: 3},
			cfg, PhaseFailed, ActionNone, 0,
		},
		{
			"recovered beats timeout", PhaseRecovering,
			Observation{Now: now.Add(90 * time.Second), InjectedAt: now, TotalPods: 3, ReadyPods: 3, BaselineReady: 3},
			cfg, PhaseCompleted, ActionNone, 90 * time.Second,
		},
		{"terminal stays", PhaseCompleted, healthy, cfg, PhaseCompleted, ActionNone, 0},
		{"abort wins", PhaseRecovering, healthy, Config{Abort: true, RecoveryTimeout: time.Minute}, PhaseAborted, ActionNone, 0},
		{"abort ignored once terminal", PhaseFailed, healthy, Config{Abort: true}, PhaseFailed, ActionNone, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.phase, tt.obs, tt.cfg)
			if d.Next != tt.wantNext || d.Action != tt.wantAction || d.RecoveryTime != tt.wantRecov {
				t.Fatalf("got next=%s action=%d recovery=%s; want next=%s action=%d recovery=%s",
					d.Next, d.Action, d.RecoveryTime, tt.wantNext, tt.wantAction, tt.wantRecov)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	for _, p := range []Phase{PhaseCompleted, PhaseFailed, PhaseAborted} {
		if !IsTerminal(p) {
			t.Errorf("%s should be terminal", p)
		}
	}
	for _, p := range []Phase{"", PhasePending, PhaseBaselineCheck, PhaseInjecting, PhaseRecovering} {
		if IsTerminal(p) {
			t.Errorf("%s should not be terminal", p)
		}
	}
}
