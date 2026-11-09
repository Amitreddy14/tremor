// Package metrics exposes Tremor's Prometheus metrics.
//
// All Recorder methods are safe to call on a nil *Recorder, so code paths that
// run without metrics (unit tests, tools) need no special casing.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// phases are the label values of tremor_experiment_phase.
var phases = []string{"Pending", "BaselineCheck", "Injecting", "Holding", "Recovering", "Completed", "Failed", "Aborted"}

// Recorder owns every Tremor metric.
type Recorder struct {
	experiments  *prometheus.CounterVec
	faults       *prometheus.CounterVec
	recovery     *prometheus.HistogramVec
	phase        *prometheus.GaugeVec
	steadyState  *prometheus.GaugeVec
	lastRecovery *prometheus.GaugeVec
	lastAvail    *prometheus.GaugeVec
	storeWrites  *prometheus.CounterVec
}

// New creates a Recorder and registers its metrics with reg. The operator
// passes controller-runtime's registry so they appear on its /metrics endpoint.
func New(reg prometheus.Registerer) *Recorder {
	r := &Recorder{
		experiments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tremor_experiments_total",
			Help: "Finished experiments by fault type and result (completed, failed, aborted, dry_run).",
		}, []string{"type", "result"}),
		faults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tremor_faults_injected_total",
			Help: "Faults injected, by fault type.",
		}, []string{"type"}),
		recovery: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "tremor_recovery_seconds",
			Help:    "Time from fault injection to recovery. kind=pod: all pods Ready; kind=user: steady-state probe stable.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300},
		}, []string{"type", "kind"}),
		phase: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tremor_experiment_phase",
			Help: "1 for the experiment's current phase, 0 for the others.",
		}, []string{"namespace", "experiment", "phase"}),
		steadyState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tremor_steady_state_ok",
			Help: "1 if the latest steady-state probe passed, 0 if it failed. Present only while an experiment runs.",
		}, []string{"namespace", "experiment"}),
		lastRecovery: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tremor_last_recovery_seconds",
			Help: "Recovery time of the experiment's last completed run (kind as in tremor_recovery_seconds).",
		}, []string{"namespace", "experiment", "type", "kind"}),
		lastAvail: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "tremor_last_availability_ratio",
			Help: "Share of passing steady-state probes between injection and recovery in the last completed run.",
		}, []string{"namespace", "experiment"}),
		storeWrites: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tremor_run_store_writes_total",
			Help: "Attempts to save a finished run to the history store, by result (ok, error).",
		}, []string{"result"}),
	}
	reg.MustRegister(r.experiments, r.faults, r.recovery, r.phase, r.steadyState, r.lastRecovery, r.lastAvail, r.storeWrites)
	return r
}

// FaultInjected counts one injection of faultType.
func (r *Recorder) FaultInjected(faultType string) {
	if r == nil {
		return
	}
	r.faults.WithLabelValues(faultType).Inc()
}

// ExperimentFinished counts a finished experiment.
func (r *Recorder) ExperimentFinished(faultType, result string) {
	if r == nil {
		return
	}
	r.experiments.WithLabelValues(faultType, result).Inc()
}

// SetPhase marks phase as the experiment's current phase.
func (r *Recorder) SetPhase(namespace, experiment, phase string) {
	if r == nil {
		return
	}
	for _, p := range phases {
		v := 0.0
		if p == phase {
			v = 1
		}
		r.phase.WithLabelValues(namespace, experiment, p).Set(v)
	}
}

// SetSteadyState records the latest probe outcome.
func (r *Recorder) SetSteadyState(namespace, experiment string, ok bool) {
	if r == nil {
		return
	}
	v := 0.0
	if ok {
		v = 1
	}
	r.steadyState.WithLabelValues(namespace, experiment).Set(v)
}

// ClearSteadyState removes the live probe gauge once an experiment ends.
func (r *Recorder) ClearSteadyState(namespace, experiment string) {
	if r == nil {
		return
	}
	r.steadyState.DeletePartialMatch(prometheus.Labels{"namespace": namespace, "experiment": experiment})
}

// RecordRecovery records a completed run. userSeconds and availability are nil
// when the experiment had no steady-state probe.
func (r *Recorder) RecordRecovery(namespace, experiment, faultType string, podSeconds float64, userSeconds, availability *float64) {
	if r == nil {
		return
	}
	r.recovery.WithLabelValues(faultType, "pod").Observe(podSeconds)
	r.lastRecovery.WithLabelValues(namespace, experiment, faultType, "pod").Set(podSeconds)
	if userSeconds != nil {
		r.recovery.WithLabelValues(faultType, "user").Observe(*userSeconds)
		r.lastRecovery.WithLabelValues(namespace, experiment, faultType, "user").Set(*userSeconds)
	}
	if availability != nil {
		r.lastAvail.WithLabelValues(namespace, experiment).Set(*availability)
	}
}

// RunStoreWrite counts one attempt to save a run; result is "ok" or "error".
func (r *Recorder) RunStoreWrite(result string) {
	if r == nil {
		return
	}
	r.storeWrites.WithLabelValues(result).Inc()
}

// Forget drops every per-experiment series, used when an experiment is deleted.
func (r *Recorder) Forget(namespace, experiment string) {
	if r == nil {
		return
	}
	match := prometheus.Labels{"namespace": namespace, "experiment": experiment}
	r.phase.DeletePartialMatch(match)
	r.steadyState.DeletePartialMatch(match)
	r.lastRecovery.DeletePartialMatch(match)
	r.lastAvail.DeletePartialMatch(match)
}
