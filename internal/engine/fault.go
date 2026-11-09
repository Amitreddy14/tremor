package engine

import (
	"fmt"
	"time"
)

// Fault type names, matching the api/v1alpha1 FaultType values.
const (
	FaultPodKill          = "PodKill"
	FaultDependencyOutage = "DependencyOutage"
	FaultCPUStress        = "CPUStress"
	FaultMemoryStress     = "MemoryStress"
)

// Limits and defaults for sustained faults.
const (
	DefaultFaultDuration = 30 * time.Second
	MinFaultDuration     = time.Second
	MaxFaultDuration     = 10 * time.Minute

	DefaultCPUWorkers = 1
	MaxCPUWorkers     = 64
	DefaultMemoryMiB  = 256
	MaxMemoryMiB      = 16384
)

// IsSustained reports whether a fault stays active for a duration and is then
// released. Sustained faults pass through the Holding phase.
func IsSustained(faultType string) bool {
	switch faultType {
	case FaultDependencyOutage, FaultCPUStress, FaultMemoryStress:
		return true
	}
	return false
}

// RemovesPods reports whether the fault deletes the targeted pods, so that a
// target is recovered only once its pods are gone and replaced. Stress faults
// leave the pods in place.
func RemovesPods(faultType string) bool {
	return faultType == FaultPodKill || faultType == FaultDependencyOutage
}

// FaultParams are the fault parameters the engine validates, as plain values.
type FaultParams struct {
	Type       string
	Count      int
	Duration   time.Duration
	CPUWorkers int
	MemoryMiB  int
	// AllowTotalOutage mirrors safety.allowTotalOutage.
	AllowTotalOutage bool
}

// NormalizeFault validates p and fills in defaults. Parameters that do not
// apply to the fault type are rejected so that typos do not silently do nothing.
func NormalizeFault(p FaultParams) (FaultParams, error) {
	switch p.Type {
	case FaultPodKill, FaultDependencyOutage, FaultCPUStress, FaultMemoryStress:
	default:
		return p, fmt.Errorf("unsupported fault type %q", p.Type)
	}

	if p.Count < 0 {
		return p, fmt.Errorf("fault.params.count must not be negative")
	}
	if p.Type == FaultDependencyOutage && p.Count != 0 {
		return p, fmt.Errorf("fault.params.count does not apply to %s: it always affects the whole workload", p.Type)
	}

	if IsSustained(p.Type) {
		if p.Duration == 0 {
			p.Duration = DefaultFaultDuration
		}
		if p.Duration < MinFaultDuration || p.Duration > MaxFaultDuration {
			return p, fmt.Errorf("fault.params.duration must be between %s and %s, got %s", MinFaultDuration, MaxFaultDuration, p.Duration)
		}
	} else if p.Duration != 0 {
		return p, fmt.Errorf("fault.params.duration does not apply to %s", p.Type)
	}

	if p.Type == FaultCPUStress {
		if p.CPUWorkers == 0 {
			p.CPUWorkers = DefaultCPUWorkers
		}
		if p.CPUWorkers < 1 || p.CPUWorkers > MaxCPUWorkers {
			return p, fmt.Errorf("fault.params.cpuWorkers must be between 1 and %d, got %d", MaxCPUWorkers, p.CPUWorkers)
		}
	} else if p.CPUWorkers != 0 {
		return p, fmt.Errorf("fault.params.cpuWorkers does not apply to %s", p.Type)
	}

	if p.Type == FaultMemoryStress {
		if p.MemoryMiB == 0 {
			p.MemoryMiB = DefaultMemoryMiB
		}
		if p.MemoryMiB < 1 || p.MemoryMiB > MaxMemoryMiB {
			return p, fmt.Errorf("fault.params.memoryMiB must be between 1 and %d, got %d", MaxMemoryMiB, p.MemoryMiB)
		}
	} else if p.MemoryMiB != 0 {
		return p, fmt.Errorf("fault.params.memoryMiB does not apply to %s", p.Type)
	}

	// An outage takes the whole workload down by definition, so it must be
	// acknowledged explicitly.
	if p.Type == FaultDependencyOutage && !p.AllowTotalOutage {
		return p, fmt.Errorf("%s takes the whole target workload down: set safety.allowTotalOutage: true to confirm", p.Type)
	}
	return p, nil
}
