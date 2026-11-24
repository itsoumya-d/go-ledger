package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrAllRegionsDown = errors.New("resilience: all regions are down")
	ErrChaosInjected  = errors.New("resilience: chaos fault injected")
)

type RegionStatus struct {
	RegionID            string        `json:"region_id"`
	Priority            int           `json:"priority"`
	Healthy             bool          `json:"healthy"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	LastHeartbeat       time.Time     `json:"last_heartbeat"`
	LastLatency         time.Duration `json:"last_latency"`
}

type FailoverCoordinator struct {
	mu           sync.RWMutex
	regions      map[string]*RegionStatus
	failureLimit int
	latencyLimit time.Duration
	activeRegion string
	history      []string
}

func NewFailoverCoordinator(regions []string, failureLimit int, latencyLimit time.Duration) *FailoverCoordinator {
	fc := &FailoverCoordinator{
		regions:      make(map[string]*RegionStatus),
		failureLimit: failureLimit,
		latencyLimit: latencyLimit,
		history:      make([]string, 0),
	}
	for i, r := range regions {
		fc.regions[r] = &RegionStatus{RegionID: r, Priority: i, Healthy: true, LastHeartbeat: time.Now()}
	}
	if len(regions) > 0 {
		fc.activeRegion = regions[0]
	}
	return fc
}

func (fc *FailoverCoordinator) RecordHeartbeat(region string, latency time.Duration, success bool) bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	status, exists := fc.regions[region]
	if !exists {
		return false
	}
	status.LastHeartbeat, status.LastLatency = time.Now(), latency
	if !success || latency > fc.latencyLimit {
		status.ConsecutiveFailures++
		if status.ConsecutiveFailures >= fc.failureLimit && status.Healthy {
			status.Healthy = false
			fc.history = append(fc.history, fmt.Sprintf("tripped %s", region))
			if fc.activeRegion == region {
				fc.reevaluateActiveLocked()
			}
		}
	} else {
		status.ConsecutiveFailures = 0
		if !status.Healthy {
			status.Healthy = true
			fc.history = append(fc.history, fmt.Sprintf("recovered %s", region))
			fc.reevaluateActiveLocked()
		}
	}
	return status.Healthy
}

func (fc *FailoverCoordinator) reevaluateActiveLocked() {
	var bestRegion string
	bestPriority := 999999
	for r, s := range fc.regions {
		if s.Healthy && s.Priority < bestPriority {
			bestPriority, bestRegion = s.Priority, r
		}
	}
	if bestRegion != "" && bestRegion != fc.activeRegion {
		fc.history = append(fc.history, fmt.Sprintf("switched %s -> %s", fc.activeRegion, bestRegion))
		fc.activeRegion = bestRegion
	} else if bestRegion == "" {
		fc.activeRegion = ""
	}
}

func (fc *FailoverCoordinator) ActiveRegion() string {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.activeRegion
}

func (fc *FailoverCoordinator) ExecuteWithFailover(ctx context.Context, fn func(region string) error) (string, error) {
	fc.mu.RLock()
	primary := fc.activeRegion
	fc.mu.RUnlock()
	if primary == "" {
		return "", ErrAllRegionsDown
	}
	if err := fn(primary); err == nil {
		return primary, nil
	}
	fc.RecordHeartbeat(primary, 0, false)
	fc.mu.RLock()
	secondary := fc.activeRegion
	fc.mu.RUnlock()
	if secondary == "" || secondary == primary {
		return "", fmt.Errorf("primary %s failed and no healthy secondary", primary)
	}
	if errSec := fn(secondary); errSec != nil {
		return "", fmt.Errorf("failover to %s failed: %w", secondary, errSec)
	}
	return secondary, nil
}

type ChaosInjector struct {
	mu          sync.RWMutex
	latency     time.Duration
	forcedError error
	active      bool
}

func NewChaosInjector() *ChaosInjector { return &ChaosInjector{} }

func (ci *ChaosInjector) Arm(latency time.Duration, forcedError error) {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	ci.latency, ci.forcedError, ci.active = latency, forcedError, true
}

func (ci *ChaosInjector) Disarm() {
	ci.mu.Lock()
	defer ci.mu.Unlock()
	ci.active, ci.forcedError = false, nil
}

func (ci *ChaosInjector) Execute(action func() error) error {
	ci.mu.RLock()
	active, latency, forcedErr := ci.active, ci.latency, ci.forcedError
	ci.mu.RUnlock()
	if !active {
		return action()
	}
	if latency > 0 {
		time.Sleep(latency)
	}
	if forcedErr != nil {
		return forcedErr
	}
	return action()
}
