package controller

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	tremorv1 "github.com/Amitreddy14/tremor/api/v1alpha1"
	"github.com/Amitreddy14/tremor/internal/engine"
	"github.com/Amitreddy14/tremor/internal/fault"
	"github.com/Amitreddy14/tremor/internal/prober"
)

// observe converts cluster state into the engine's plain Observation.
func observe(exp *tremorv1.ChaosExperiment, pods []corev1.Pod, now time.Time,
	series prober.Series, probing bool, threshold int) engine.Observation {
	o := engine.Observation{
		Now:           now,
		TotalPods:     len(pods),
		BaselineReady: int(exp.Status.BaselineReadyPods),
	}
	if exp.Status.StartedAt != nil {
		o.StartedAt = exp.Status.StartedAt.Time
	}
	if exp.Status.InjectedAt != nil {
		o.InjectedAt = exp.Status.InjectedAt.Time
	}
	if exp.Status.PodsRecoveredAt != nil {
		o.PodsRecoveredAt = exp.Status.PodsRecoveredAt.Time
	}
	if exp.Status.RevertedAt != nil {
		o.RevertedAt = exp.Status.RevertedAt.Time
	}
	if probing {
		o.ProbeEnabled = true
		o.ProbeHealthy = prober.Healthy(series, threshold)
		if !o.InjectedAt.IsZero() {
			o.ProbeStable = prober.Analyze(series, o.InjectedAt, threshold).Stable
		}
	}

	present := make(map[string]struct{}, len(pods))
	for i := range pods {
		p := &pods[i]
		present[string(p.UID)] = struct{}{}
		switch {
		case !p.DeletionTimestamp.IsZero():
			o.TerminatingPods++
		case isReady(p):
			o.ReadyPods++
		}
	}
	for _, a := range exp.Status.AffectedPods {
		if _, ok := present[a.UID]; ok {
			o.AffectedStillPresent = true
			break
		}
	}
	return o
}

func configFrom(exp *tremorv1.ChaosExperiment) engine.Config {
	c := engine.Config{
		RecoveryTimeout: defaultRecoveryTimeout,
		BaselineTimeout: defaultBaselineTimeout,
		Abort:           exp.Annotations[tremorv1.AbortAnnotation] == "true",
		DryRun:          exp.Spec.Safety.DryRun,
	}
	if t := exp.Spec.RecoveryTimeout; t != nil && t.Duration > 0 {
		c.RecoveryTimeout = t.Duration
	}
	return c
}

// probeConfig converts the steady-state spec into prober settings, applying
// defaults. The bool is false when no steady state is configured.
func probeConfig(exp *tremorv1.ChaosExperiment) (prober.Config, int, bool) {
	ss := exp.Spec.SteadyState
	if ss == nil || ss.HTTP.URL == "" {
		return prober.Config{}, 0, false
	}
	interval := time.Second
	if ss.Interval != nil && ss.Interval.Duration > 0 {
		interval = ss.Interval.Duration
	}
	status := int(ss.HTTP.ExpectStatus)
	if status == 0 {
		status = 200
	}
	threshold := int(ss.SuccessThreshold)
	if threshold < 1 {
		threshold = 3
	}
	timeout := interval
	if timeout < 500*time.Millisecond {
		timeout = 500 * time.Millisecond
	}
	return prober.Config{URL: ss.HTTP.URL, ExpectStatus: status, Interval: interval, Timeout: timeout}, threshold, true
}

func isReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// faultParams converts the spec's fault parameters into the engine's plain form.
func faultParams(exp *tremorv1.ChaosExperiment) engine.FaultParams {
	p := exp.Spec.Fault.Params
	fp := engine.FaultParams{
		Type:             string(exp.Spec.Fault.Type),
		Count:            int(p.Count),
		CPUWorkers:       int(p.CPUWorkers),
		MemoryMiB:        int(p.MemoryMiB),
		AllowTotalOutage: exp.Spec.Safety.AllowTotalOutage,
	}
	if p.Duration != nil {
		fp.Duration = p.Duration.Duration
	}
	return fp
}

// faultSpec collects what an injector needs from the experiment and its status.
func faultSpec(exp *tremorv1.ChaosExperiment, fp engine.FaultParams) fault.Spec {
	s := fault.Spec{
		Namespace:  exp.Namespace,
		Targets:    toTargets(exp.Status.AffectedPods),
		Holder:     string(exp.UID),
		Duration:   fp.Duration,
		CPUWorkers: fp.CPUWorkers,
		MemoryMiB:  fp.MemoryMiB,
	}
	if w := exp.Status.Workload; w != nil {
		s.Workload = fault.Workload{Kind: w.Kind, Name: w.Name}
	}
	return s
}

func toTargets(in []tremorv1.AffectedPod) []fault.Target {
	out := make([]fault.Target, 0, len(in))
	for _, a := range in {
		out = append(out, fault.Target{Name: a.Name, UID: a.UID})
	}
	return out
}

func podNames(in []tremorv1.AffectedPod) string {
	names := make([]string, 0, len(in))
	for _, a := range in {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
