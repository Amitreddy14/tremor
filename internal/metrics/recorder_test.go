package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNilRecorderIsSafe(t *testing.T) {
	var r *Recorder
	one := 1.0
	r.FaultInjected("PodKill")
	r.ExperimentFinished("PodKill", "completed")
	r.SetPhase("ns", "x", "Recovering")
	r.SetSteadyState("ns", "x", true)
	r.ClearSteadyState("ns", "x")
	r.RecordRecovery("ns", "x", "PodKill", 1, &one, &one)
	r.RunStoreWrite("ok")
	r.Forget("ns", "x")
}

func TestCounters(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.ExperimentFinished("PodKill", "completed")
	r.ExperimentFinished("PodKill", "completed")
	r.ExperimentFinished("PodKill", "failed")
	r.FaultInjected("PodKill")

	if got := testutil.ToFloat64(r.experiments.WithLabelValues("PodKill", "completed")); got != 2 {
		t.Errorf("completed = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.experiments.WithLabelValues("PodKill", "failed")); got != 1 {
		t.Errorf("failed = %v, want 1", got)
	}
	if got := testutil.ToFloat64(r.faults.WithLabelValues("PodKill")); got != 1 {
		t.Errorf("faults = %v, want 1", got)
	}
}

func TestRunStoreWrites(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.RunStoreWrite("ok")
	r.RunStoreWrite("ok")
	r.RunStoreWrite("error")

	if got := testutil.ToFloat64(r.storeWrites.WithLabelValues("ok")); got != 2 {
		t.Errorf("ok = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.storeWrites.WithLabelValues("error")); got != 1 {
		t.Errorf("error = %v, want 1", got)
	}
}

func TestSetPhaseIsOneHot(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.SetPhase("demo", "kill", "Injecting")
	r.SetPhase("demo", "kill", "Recovering")

	for _, p := range phases {
		want := 0.0
		if p == "Recovering" {
			want = 1
		}
		if got := testutil.ToFloat64(r.phase.WithLabelValues("demo", "kill", p)); got != want {
			t.Errorf("phase %s = %v, want %v", p, got, want)
		}
	}
}

func TestRecordRecovery(t *testing.T) {
	r := New(prometheus.NewRegistry())
	user, avail := 7.5, 0.88
	r.RecordRecovery("demo", "kill", "PodKill", 4.2, &user, &avail)

	if got := testutil.ToFloat64(r.lastRecovery.WithLabelValues("demo", "kill", "PodKill", "pod")); got != 4.2 {
		t.Errorf("pod gauge = %v, want 4.2", got)
	}
	if got := testutil.ToFloat64(r.lastRecovery.WithLabelValues("demo", "kill", "PodKill", "user")); got != 7.5 {
		t.Errorf("user gauge = %v, want 7.5", got)
	}
	if got := testutil.ToFloat64(r.lastAvail.WithLabelValues("demo", "kill")); got != 0.88 {
		t.Errorf("availability = %v, want 0.88", got)
	}
	// One pod and one user observation, so two histogram series.
	if got := testutil.CollectAndCount(r.recovery); got != 2 {
		t.Errorf("recovery series = %d, want 2", got)
	}
}

func TestRecordRecoveryWithoutProbe(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.RecordRecovery("demo", "kill", "PodKill", 3, nil, nil)

	if got := testutil.CollectAndCount(r.recovery); got != 1 {
		t.Errorf("recovery series = %d, want 1 (pod only)", got)
	}
	if got := testutil.CollectAndCount(r.lastAvail); got != 0 {
		t.Errorf("availability series = %d, want 0", got)
	}
}

func TestSteadyStateAndForget(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.SetSteadyState("demo", "kill", true)
	r.SetSteadyState("demo", "kill", false)
	if got := testutil.ToFloat64(r.steadyState.WithLabelValues("demo", "kill")); got != 0 {
		t.Errorf("steady state = %v, want 0", got)
	}

	user := 1.0
	r.SetPhase("demo", "kill", "Completed")
	r.RecordRecovery("demo", "kill", "PodKill", 1, &user, &user)
	r.SetPhase("demo", "other", "Completed")

	r.Forget("demo", "kill")
	if got := testutil.CollectAndCount(r.steadyState); got != 0 {
		t.Errorf("steady state series after Forget = %d, want 0", got)
	}
	if got := testutil.CollectAndCount(r.lastRecovery); got != 0 {
		t.Errorf("last recovery series after Forget = %d, want 0", got)
	}
	if got := testutil.CollectAndCount(r.lastAvail); got != 0 {
		t.Errorf("availability series after Forget = %d, want 0", got)
	}
	if got := testutil.CollectAndCount(r.phase); got != len(phases) {
		t.Errorf("phase series = %d, want %d (only the other experiment)", got, len(phases))
	}
}
