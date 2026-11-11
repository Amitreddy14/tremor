package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
)

func TestProbeURLOutsideAllowlistFails(t *testing.T) {
	c := startEnv(t)
	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "guard-probe")
	}
	exp := newExperiment("guard-probe", "guard-probe")
	exp.Spec.SteadyState = steadyState("http://169.254.169.254/latest/meta-data")
	if err := c.Create(context.Background(), exp); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "experiment to fail", func() bool {
		return phaseOf(c, "guard-probe") == tremorv1.PhaseFailed
	})
	got := getExperiment(t, c, "guard-probe")
	if !strings.Contains(got.Status.Message, "probe host") {
		t.Fatalf("message should explain the probe host rule: %q", got.Status.Message)
	}
	if got.Status.InjectedAt != nil {
		t.Fatal("nothing may be injected after a rejected probe url")
	}
}

func TestOverlappingExperimentWaitsForTheRunningOne(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "guard-overlap")
	}
	// The first holds its fault for a while (a sustained outage would need a
	// workload, so a long recovery wait on a pod kill is used instead: the
	// replacement pod never appears in envtest).
	first := newExperiment("overlap-first", "guard-overlap")
	if err := c.Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "first to be recovering", func() bool {
		return phaseOf(c, "overlap-first") == tremorv1.PhaseRecovering
	})

	second := newExperiment("overlap-second", "guard-overlap")
	if err := c.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "second to report it is blocked", func() bool {
		m := getExperiment(t, c, "overlap-second").Status.Message
		return strings.Contains(m, "overlap-first")
	})
	if p := phaseOf(c, "overlap-second"); p != tremorv1.PhaseBaselineCheck {
		t.Fatalf("second must wait in BaselineCheck, is %s", p)
	}
	if getExperiment(t, c, "overlap-second").Status.InjectedAt != nil {
		t.Fatal("second must not inject while the first is running")
	}
}
