package engine

import (
	"errors"
	"math/rand"
	"reflect"
	"strings"
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

func TestDecideWithProbe(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	cfg := Config{RecoveryTimeout: 60 * time.Second, BaselineTimeout: 30 * time.Second}

	t.Run("baseline waits for probe", func(t *testing.T) {
		d := Decide(PhaseBaselineCheck, Observation{Now: now, StartedAt: now, TotalPods: 3, ReadyPods: 3, ProbeEnabled: true}, cfg)
		if d.Next != PhaseBaselineCheck || d.Action != ActionNone {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("baseline proceeds when probe healthy", func(t *testing.T) {
		d := Decide(PhaseBaselineCheck, Observation{Now: now, StartedAt: now, TotalPods: 3, ReadyPods: 3, ProbeEnabled: true, ProbeHealthy: true}, cfg)
		if d.Next != PhaseInjecting || d.Action != ActionPlan {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("baseline fails on timeout with probe unhealthy", func(t *testing.T) {
		d := Decide(PhaseBaselineCheck, Observation{Now: now.Add(31 * time.Second), StartedAt: now, TotalPods: 3, ReadyPods: 3, ProbeEnabled: true}, cfg)
		if d.Next != PhaseFailed {
			t.Fatalf("got %+v", d)
		}
	})

	injected := now
	t.Run("pods recovered but probe not stable keeps waiting and records pod time", func(t *testing.T) {
		d := Decide(PhaseRecovering, Observation{
			Now: now.Add(4 * time.Second), InjectedAt: injected,
			TotalPods: 3, ReadyPods: 3, BaselineReady: 3, ProbeEnabled: true,
		}, cfg)
		if d.Next != PhaseRecovering || d.RecoveryTime != 4*time.Second || d.PodsRecoveredAt.IsZero() {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("probe stable later completes with earlier pod time", func(t *testing.T) {
		d := Decide(PhaseRecovering, Observation{
			Now: now.Add(9 * time.Second), InjectedAt: injected,
			TotalPods: 3, ReadyPods: 3, BaselineReady: 3,
			PodsRecoveredAt: now.Add(4 * time.Second),
			ProbeEnabled:    true, ProbeStable: true,
		}, cfg)
		if d.Next != PhaseCompleted || d.RecoveryTime != 4*time.Second {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("probe stable but pods not recovered keeps waiting", func(t *testing.T) {
		d := Decide(PhaseRecovering, Observation{
			Now: now.Add(3 * time.Second), InjectedAt: injected,
			TotalPods: 2, ReadyPods: 2, BaselineReady: 3, ProbeEnabled: true, ProbeStable: true,
		}, cfg)
		if d.Next != PhaseRecovering || !d.PodsRecoveredAt.IsZero() {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("timeout while probe unstable fails", func(t *testing.T) {
		d := Decide(PhaseRecovering, Observation{
			Now: now.Add(61 * time.Second), InjectedAt: injected,
			TotalPods: 3, ReadyPods: 3, BaselineReady: 3, ProbeEnabled: true,
		}, cfg)
		if d.Next != PhaseFailed {
			t.Fatalf("got %+v", d)
		}
	})
}

func TestDecideSustainedFault(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	cfg := Config{RecoveryTimeout: 60 * time.Second, BaselineTimeout: 30 * time.Second}
	injected := now.Add(-45 * time.Second) // outage ran for 45s
	released := now.Add(-5 * time.Second)  // and was released 5s ago

	t.Run("injecting moves to holding", func(t *testing.T) {
		d := Decide(PhaseInjecting, Observation{Now: now, Sustained: true}, cfg)
		if d.Next != PhaseHolding || d.Action != ActionInject {
			t.Fatalf("got %s/%d, want Holding/Inject", d.Next, d.Action)
		}
	})
	t.Run("one-shot fault skips holding", func(t *testing.T) {
		d := Decide(PhaseInjecting, Observation{Now: now}, cfg)
		if d.Next != PhaseRecovering || d.Action != ActionInject {
			t.Fatalf("got %s/%d, want Recovering/Inject", d.Next, d.Action)
		}
	})
	t.Run("holding waits", func(t *testing.T) {
		d := Decide(PhaseHolding, Observation{Now: now, InjectedAt: injected, Sustained: true}, cfg)
		if d.Next != PhaseHolding || d.Action != ActionNone || d.RequeueAfter == 0 {
			t.Fatalf("got %s/%d requeue=%s, want Holding/None with a requeue", d.Next, d.Action, d.RequeueAfter)
		}
	})
	t.Run("holding releases when done", func(t *testing.T) {
		d := Decide(PhaseHolding, Observation{Now: now, InjectedAt: injected, Sustained: true, FaultDone: true}, cfg)
		if d.Next != PhaseRecovering || d.Action != ActionRevert {
			t.Fatalf("got %s/%d, want Recovering/Revert", d.Next, d.Action)
		}
	})
	t.Run("holding is never subject to the recovery timeout", func(t *testing.T) {
		// 45s held with a 10s timeout must not fail: the timeout only covers recovery.
		short := Config{RecoveryTimeout: 10 * time.Second}
		d := Decide(PhaseHolding, Observation{Now: now, InjectedAt: injected, Sustained: true}, short)
		if d.Next != PhaseHolding {
			t.Fatalf("got %s, want Holding", d.Next)
		}
	})
	t.Run("recovery is timed from the release", func(t *testing.T) {
		o := Observation{Now: now, InjectedAt: injected, RevertedAt: released, Sustained: true, TotalPods: 1, ReadyPods: 1, BaselineReady: 1}
		d := Decide(PhaseRecovering, o, cfg)
		if d.Next != PhaseCompleted || d.RecoveryTime != 5*time.Second {
			t.Fatalf("got %s recovery=%s, want Completed 5s (not 45s)", d.Next, d.RecoveryTime)
		}
	})
	t.Run("recovery timeout counts from the release", func(t *testing.T) {
		// 45s since injection would exceed 30s, but only 5s since release.
		short := Config{RecoveryTimeout: 30 * time.Second}
		o := Observation{Now: now, InjectedAt: injected, RevertedAt: released, Sustained: true, TotalPods: 1, ReadyPods: 0, BaselineReady: 1}
		if d := Decide(PhaseRecovering, o, short); d.Next != PhaseRecovering {
			t.Fatalf("got %s, want still Recovering", d.Next)
		}
		o.RevertedAt = now.Add(-31 * time.Second)
		if d := Decide(PhaseRecovering, o, short); d.Next != PhaseFailed {
			t.Fatalf("got %s, want Failed after 31s", d.Next)
		}
	})
	t.Run("abort while holding", func(t *testing.T) {
		d := Decide(PhaseHolding, Observation{Now: now, Sustained: true}, Config{Abort: true})
		if d.Next != PhaseAborted {
			t.Fatalf("got %s, want Aborted", d.Next)
		}
	})
}

func TestFaultClassification(t *testing.T) {
	for _, tc := range []struct {
		fault                  string
		sustained, removesPods bool
	}{
		{FaultPodKill, false, true},
		{FaultDependencyOutage, true, true},
		{FaultCPUStress, true, false},
		{FaultMemoryStress, true, false},
		{FaultNetworkLatency, true, false},
		{FaultNetworkLoss, true, false},
		{FaultNodeDrain, false, true},
		{"Bogus", false, false},
	} {
		if got := IsSustained(tc.fault); got != tc.sustained {
			t.Errorf("IsSustained(%s) = %v, want %v", tc.fault, got, tc.sustained)
		}
		if got := RemovesPods(tc.fault); got != tc.removesPods {
			t.Errorf("RemovesPods(%s) = %v, want %v", tc.fault, got, tc.removesPods)
		}
	}
}

func TestNormalizeFault(t *testing.T) {
	tests := []struct {
		name    string
		in      FaultParams
		want    FaultParams
		wantErr string
	}{
		{"pod kill as is", FaultParams{Type: FaultPodKill, Count: 2}, FaultParams{Type: FaultPodKill, Count: 2}, ""},
		{"cpu defaults", FaultParams{Type: FaultCPUStress}, FaultParams{Type: FaultCPUStress, Duration: 30 * time.Second, CPUWorkers: 1}, ""},
		{"memory defaults", FaultParams{Type: FaultMemoryStress}, FaultParams{Type: FaultMemoryStress, Duration: 30 * time.Second, MemoryMiB: 256}, ""},
		{"cpu explicit", FaultParams{Type: FaultCPUStress, Count: 1, Duration: time.Minute, CPUWorkers: 4}, FaultParams{Type: FaultCPUStress, Count: 1, Duration: time.Minute, CPUWorkers: 4}, ""},
		{"outage confirmed", FaultParams{Type: FaultDependencyOutage, Duration: 45 * time.Second, AllowTotalOutage: true}, FaultParams{Type: FaultDependencyOutage, Duration: 45 * time.Second, AllowTotalOutage: true}, ""},
		{"outage needs confirmation", FaultParams{Type: FaultDependencyOutage, Duration: time.Minute}, FaultParams{}, "allowTotalOutage"},
		{"outage rejects count", FaultParams{Type: FaultDependencyOutage, Count: 1, AllowTotalOutage: true}, FaultParams{}, "count does not apply"},
		{"unknown type", FaultParams{Type: "Bogus"}, FaultParams{}, "unsupported fault type"},
		{"negative count", FaultParams{Type: FaultPodKill, Count: -1}, FaultParams{}, "negative"},
		{"duration too short", FaultParams{Type: FaultCPUStress, Duration: time.Millisecond}, FaultParams{}, "duration must be between"},
		{"duration too long", FaultParams{Type: FaultCPUStress, Duration: time.Hour}, FaultParams{}, "duration must be between"},
		{"pod kill rejects duration", FaultParams{Type: FaultPodKill, Duration: time.Minute}, FaultParams{}, "duration does not apply"},
		{"cpu workers too many", FaultParams{Type: FaultCPUStress, CPUWorkers: 1000}, FaultParams{}, "cpuWorkers must be between"},
		{"cpu workers on memory", FaultParams{Type: FaultMemoryStress, CPUWorkers: 2}, FaultParams{}, "cpuWorkers does not apply"},
		{"memory too big", FaultParams{Type: FaultMemoryStress, MemoryMiB: 1 << 20}, FaultParams{}, "memoryMiB must be between"},
		{"memory on cpu", FaultParams{Type: FaultCPUStress, MemoryMiB: 64}, FaultParams{}, "memoryMiB does not apply"},
		{"latency defaults", FaultParams{Type: FaultNetworkLatency}, FaultParams{Type: FaultNetworkLatency, Duration: 30 * time.Second, Latency: 200 * time.Millisecond}, ""},
		{"latency explicit", FaultParams{Type: FaultNetworkLatency, Count: 1, Latency: time.Second, Jitter: 100 * time.Millisecond, Duration: time.Minute}, FaultParams{Type: FaultNetworkLatency, Count: 1, Latency: time.Second, Jitter: 100 * time.Millisecond, Duration: time.Minute}, ""},
		{"latency too high", FaultParams{Type: FaultNetworkLatency, Latency: time.Minute}, FaultParams{}, "latency must be between"},
		{"jitter above latency", FaultParams{Type: FaultNetworkLatency, Latency: 100 * time.Millisecond, Jitter: time.Second}, FaultParams{}, "jitter must be between"},
		{"latency on loss", FaultParams{Type: FaultNetworkLoss, Latency: time.Second}, FaultParams{}, "latency does not apply"},
		{"jitter on cpu", FaultParams{Type: FaultCPUStress, Jitter: time.Second}, FaultParams{}, "jitter does not apply"},
		{"loss defaults", FaultParams{Type: FaultNetworkLoss}, FaultParams{Type: FaultNetworkLoss, Duration: 30 * time.Second, LossPercent: 20}, ""},
		{"loss explicit", FaultParams{Type: FaultNetworkLoss, LossPercent: 100, Duration: 5 * time.Second}, FaultParams{Type: FaultNetworkLoss, LossPercent: 100, Duration: 5 * time.Second}, ""},
		{"loss too high", FaultParams{Type: FaultNetworkLoss, LossPercent: 101}, FaultParams{}, "lossPercent must be between"},
		{"loss negative", FaultParams{Type: FaultNetworkLoss, LossPercent: -5}, FaultParams{}, "lossPercent must be between"},
		{"loss on latency", FaultParams{Type: FaultNetworkLatency, LossPercent: 5}, FaultParams{}, "lossPercent does not apply"},
		{"drain as is", FaultParams{Type: FaultNodeDrain}, FaultParams{Type: FaultNodeDrain}, ""},
		{"drain rejects count", FaultParams{Type: FaultNodeDrain, Count: 1}, FaultParams{}, "count does not apply"},
		{"drain rejects duration", FaultParams{Type: FaultNodeDrain, Duration: time.Minute}, FaultParams{}, "duration does not apply"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeFault(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNeedsReleaseAndIsNetwork(t *testing.T) {
	for f, want := range map[string]bool{
		FaultPodKill: false, FaultDependencyOutage: true, FaultCPUStress: true, FaultMemoryStress: true,
		FaultNetworkLatency: true, FaultNetworkLoss: true, FaultNodeDrain: true, "Bogus": false,
	} {
		if got := NeedsRelease(f); got != want {
			t.Errorf("NeedsRelease(%s) = %v, want %v", f, got, want)
		}
	}
	if !IsNetwork(FaultNetworkLatency) || !IsNetwork(FaultNetworkLoss) || IsNetwork(FaultNodeDrain) {
		t.Error("IsNetwork misclassifies a fault")
	}
}

func TestPickDrainNode(t *testing.T) {
	spare := SafetyPolicy{MaxAffectedPercent: 100}
	tests := []struct {
		name    string
		loads   []NodeLoad
		total   int
		policy  SafetyPolicy
		want    string
		wantErr bool
	}{
		{"most pods wins", []NodeLoad{{"w1", 1}, {"w2", 2}}, 3, spare, "w2", false},
		{"tie goes to lowest name", []NodeLoad{{"w2", 1}, {"w1", 1}}, 2, spare, "w1", false},
		{"never drains every pod", []NodeLoad{{"w1", 2}}, 2, spare, "", true},
		{"total outage allowed", []NodeLoad{{"w1", 2}}, 2, SafetyPolicy{MaxAffectedPercent: 100, AllowTotalOutage: true}, "w1", false},
		{"percent cap skips big node", []NodeLoad{{"w1", 2}, {"w2", 1}}, 4, SafetyPolicy{MaxAffectedPercent: 25}, "w2", false},
		{"default cap is half", []NodeLoad{{"w1", 3}, {"w2", 2}, {"w3", 1}}, 6, SafetyPolicy{}, "w1", false},
		{"default cap blocks majority", []NodeLoad{{"w1", 3}}, 4, SafetyPolicy{}, "", true},
		{"ignores unscheduled and empty", []NodeLoad{{"", 2}, {"w1", 0}, {"w2", 1}}, 3, spare, "w2", false},
		{"no nodes", nil, 3, spare, "", true},
		{"no pods", []NodeLoad{{"w1", 1}}, 0, spare, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PickDrainNode(tt.loads, tt.total, tt.policy)
			if tt.wantErr {
				if !errors.Is(err, ErrNoSafeTarget) {
					t.Fatalf("err = %v, want ErrNoSafeTarget", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestRevertedAtOnlyStartsClockForSustainedFaults(t *testing.T) {
	now := time.Now()
	obs := Observation{
		Now: now, InjectedAt: now.Add(-100 * time.Second), RevertedAt: now.Add(-1 * time.Second),
		TotalPods: 2, ReadyPods: 1, BaselineReady: 2, AffectedStillPresent: true,
	}
	cfg := Config{RecoveryTimeout: 90 * time.Second}

	// Not sustained (node drain): the clock runs from injection, so 100s > 90s.
	if d := Decide(PhaseRecovering, obs, cfg); d.Next != PhaseFailed {
		t.Fatalf("non-sustained: got %s, want Failed", d.Next)
	}
	// Sustained: the clock runs from the release 1s ago.
	obs.Sustained = true
	if d := Decide(PhaseRecovering, obs, cfg); d.Next != PhaseRecovering {
		t.Fatalf("sustained: got %s, want Recovering", d.Next)
	}
}

func TestNamespaceAllowed(t *testing.T) {
	if !NamespaceAllowed("any", nil) {
		t.Fatal("empty allowlist permits all")
	}
	if !NamespaceAllowed("demo", []string{"a", "demo"}) || NamespaceAllowed("prod", []string{"demo"}) {
		t.Fatal("exact match only")
	}
}

func TestSelectorsOverlap(t *testing.T) {
	a := map[string]string{"app": "shop"}
	cases := []struct {
		b    map[string]string
		want bool
	}{
		{map[string]string{"app": "shop"}, true},
		{map[string]string{"app": "shop", "tier": "web"}, true},
		{map[string]string{"tier": "web"}, true}, // could match the same pod
		{map[string]string{"app": "mongo"}, false},
	}
	for _, tc := range cases {
		if got := SelectorsOverlap(a, tc.b); got != tc.want {
			t.Errorf("%v: got %v", tc.b, got)
		}
	}
}

func TestValidateProbeURL(t *testing.T) {
	ok := []string{
		"http://shop.demo.svc.cluster.local/healthz",
		"http://shop.demo.svc:8080/healthz",
		"https://Shop.demo.svc/",
	}
	for _, u := range ok {
		if err := ValidateProbeURL(u, "demo", nil); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	bad := []string{
		"ftp://shop.demo.svc/",
		"http://shop.other.svc/",
		"http://169.254.169.254/latest/meta-data",
		"http://localhost:8080/",
		"http://example.com/",
		"http://user:pw@shop.demo.svc/",
		"http://[::1]:80/",
		"shop.demo.svc",
		"http:///x",
	}
	for _, u := range bad {
		if err := ValidateProbeURL(u, "demo", nil); err == nil {
			t.Errorf("%s should be rejected", u)
		}
	}
	allow := []string{"example.com", "*.internal.test", "10.0.0.5"}
	for _, u := range []string{"http://example.com/x", "https://a.internal.test/", "http://10.0.0.5:9/"} {
		if err := ValidateProbeURL(u, "demo", allow); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	if err := ValidateProbeURL("http://evilinternal.test/", "demo", allow); err == nil {
		t.Error("wildcard must match a dot boundary")
	}
}

func TestBaselineBlocked(t *testing.T) {
	now := time.Now()
	o := Observation{Now: now, StartedAt: now, TotalPods: 3, ReadyPods: 3, Blocked: "experiment x is active"}
	c := Config{BaselineTimeout: time.Minute}
	d := Decide(PhaseBaselineCheck, o, c)
	if d.Next != PhaseBaselineCheck || d.Action != ActionNone {
		t.Fatalf("should wait: %+v", d)
	}
	o.Now = now.Add(2 * time.Minute)
	if d := Decide(PhaseBaselineCheck, o, c); d.Next != PhaseFailed {
		t.Fatalf("should fail after timeout: %+v", d)
	}
	o.Blocked = ""
	if d := Decide(PhaseBaselineCheck, Observation{Now: now, StartedAt: now, TotalPods: 3, ReadyPods: 3}, c); d.Next != PhaseInjecting {
		t.Fatalf("unblocked proceeds: %+v", d)
	}
}
