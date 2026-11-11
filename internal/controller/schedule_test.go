package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
)

// scheduleClock freezes the schedule controller's clock (unix nanoseconds; 0
// means real time). A frozen clock keeps a test from crossing a minute
// boundary halfway through and seeing a second run become due.
var scheduleClock atomic.Int64

// freezeClock sets the schedule clock to the start of the current minute plus
// d, and restores real time when the test ends.
func freezeClock(t *testing.T, d time.Duration) {
	t.Helper()
	scheduleClock.Store(time.Now().Truncate(time.Minute).Add(d).UnixNano())
	t.Cleanup(func() { scheduleClock.Store(0) })
}

// cleanupSchedule removes a schedule at the end of a test so it stops
// reacting to later tests' clocks.
func cleanupSchedule(t *testing.T, c client.Client, name string) {
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), &tremorv1.ChaosSchedule{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}})
	})
}

func newSchedule(name, app, cron string) *tremorv1.ChaosSchedule {
	e := newExperiment("unused", app)
	e.Spec.Safety.DryRun = true // schedule tests are about scheduling, not faults
	return &tremorv1.ChaosSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: tremorv1.ChaosScheduleSpec{
			Schedule: cron,
			Template: tremorv1.ChaosExperimentTemplate{
				Labels: map[string]string{"team": "test"},
				Spec:   e.Spec,
			},
		},
	}
}

func getSchedule(t *testing.T, c client.Client, name string) tremorv1.ChaosSchedule {
	t.Helper()
	var s tremorv1.ChaosSchedule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNamespace, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func scheduleExperiments(t *testing.T, c client.Client, name string) []tremorv1.ChaosExperiment {
	t.Helper()
	var l tremorv1.ChaosExperimentList
	if err := c.List(context.Background(), &l, client.InNamespace(testNamespace),
		client.MatchingLabels{tremorv1.ScheduleLabel: name}); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

// touch changes the schedule so the controller reconciles it again.
func touch(t *testing.T, c client.Client, name, val string) {
	t.Helper()
	s := getSchedule(t, c, name)
	base := s.DeepCopy()
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations["touch"] = val
	// A patch carries no resourceVersion, so it cannot lose a race with the
	// controller's own status writes.
	if err := c.Patch(context.Background(), &s, client.MergeFrom(base)); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleCreatesExperiments(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "sched-app")
	}
	s := newSchedule("every-minute", "sched-app", "* * * * *")
	if err := c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	cleanupSchedule(t, c, "every-minute")

	// Nothing is due yet; the next run is planned.
	eventually(t, 10*time.Second, "next run planned", func() bool {
		return getSchedule(t, c, "every-minute").Status.NextScheduleTime != nil
	})
	if n := len(scheduleExperiments(t, c, "every-minute")); n != 0 {
		t.Fatalf("expected no experiment before the first run, got %d", n)
	}

	// Two minutes pass: a run is due.
	freezeClock(t, 2*time.Minute+10*time.Second) // the run at +2m is 10s old
	touch(t, c, "every-minute", "1")

	eventually(t, 15*time.Second, "an experiment to be created", func() bool {
		return len(scheduleExperiments(t, c, "every-minute")) == 1
	})
	exp := scheduleExperiments(t, c, "every-minute")[0]
	if exp.Labels["team"] != "test" || exp.Labels[tremorv1.ScheduleLabel] != "every-minute" {
		t.Fatalf("labels not applied: %v", exp.Labels)
	}
	if len(exp.OwnerReferences) != 1 || exp.OwnerReferences[0].Kind != "ChaosSchedule" {
		t.Fatalf("experiment should be owned by the schedule: %v", exp.OwnerReferences)
	}
	eventually(t, 15*time.Second, "status to record the run", func() bool {
		st := getSchedule(t, c, "every-minute").Status
		return st.LastExperiment == exp.Name && st.LastScheduleTime != nil
	})
	// The dry-run experiment finishes; the schedule must not start a second one
	// for the same run.
	eventually(t, 20*time.Second, "dry run to complete", func() bool {
		return phaseOf(c, exp.Name) == tremorv1.PhaseCompleted
	})
	touch(t, c, "every-minute", "2")
	time.Sleep(2 * time.Second)
	if n := len(scheduleExperiments(t, c, "every-minute")); n != 1 {
		t.Fatalf("the same run must not create another experiment, got %d", n)
	}
}

func TestScheduleSuspendedCreatesNothing(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	s := newSchedule("suspended", "sched-suspended", "* * * * *")
	s.Spec.Suspend = true
	if err := c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	cleanupSchedule(t, c, "suspended")
	freezeClock(t, 5*time.Minute)
	touch(t, c, "suspended", "1")
	eventually(t, 10*time.Second, "suspended message", func() bool {
		return getSchedule(t, c, "suspended").Status.Message == "suspended"
	})
	if n := len(scheduleExperiments(t, c, "suspended")); n != 0 {
		t.Fatalf("suspended schedule created %d experiment(s)", n)
	}
}

func TestScheduleInvalidCronIsReported(t *testing.T) {
	c := startEnv(t)
	s := newSchedule("bad-cron", "sched-bad", "not a cron")
	if err := c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "invalid schedule message", func() bool {
		m := getSchedule(t, c, "bad-cron").Status.Message
		return len(m) > 0 && m != "suspended"
	})
}

func TestScheduleMissedRunPastDeadlineIsSkipped(t *testing.T) {
	c := startEnv(t)
	s := newSchedule("late", "sched-late", "* * * * *")
	dl := int64(30)
	s.Spec.StartingDeadlineSeconds = &dl
	if err := c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "next run planned", func() bool {
		return getSchedule(t, c, "late").Status.NextScheduleTime != nil
	})
	cleanupSchedule(t, c, "late")
	// An hour passes with the operator "down": the latest run (at +1h) is 45s
	// old, past the 30s deadline.
	freezeClock(t, time.Hour+45*time.Second)
	touch(t, c, "late", "1")
	eventually(t, 10*time.Second, "skip message", func() bool {
		return getSchedule(t, c, "late").Status.LastScheduleTime != nil
	})
	if n := len(scheduleExperiments(t, c, "late")); n != 0 {
		t.Fatalf("a run past its deadline must be skipped, got %d experiment(s)", n)
	}
}

func TestSchedulePrunesHistory(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		newReadyPod(t, c, "sched-pruned")
	}
	s := newSchedule("pruned", "sched-pruned", "0 0 1 1 *")
	keep := int32(1)
	s.Spec.SuccessfulRunsHistoryLimit = &keep
	if err := c.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got := getSchedule(t, c, "pruned")
	// Three finished experiments that belong to the schedule.
	for _, n := range []string{"pruned-a", "pruned-b", "pruned-c"} {
		e := newExperiment(n, "sched-pruned")
		e.Labels = map[string]string{tremorv1.ScheduleLabel: "pruned"}
		e.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: tremorv1.GroupVersion.String(), Kind: "ChaosSchedule", Name: got.Name, UID: got.UID,
		}}
		e.Spec.Safety.DryRun = true
		if err := c.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // distinct creation timestamps
	}
	for _, n := range []string{"pruned-a", "pruned-b", "pruned-c"} {
		n := n
		eventually(t, 30*time.Second, n+" to finish or be pruned", func() bool {
			p := phaseOf(c, n)
			return p == tremorv1.PhaseCompleted || p == ""
		})
	}
	touch(t, c, "pruned", "1")
	eventually(t, 20*time.Second, "history pruned to the limit", func() bool {
		return len(scheduleExperiments(t, c, "pruned")) == 1
	})
	// The newest survives.
	if rest := scheduleExperiments(t, c, "pruned"); rest[0].Name != "pruned-c" {
		t.Fatalf("expected the newest to remain, got %s", rest[0].Name)
	}
}
