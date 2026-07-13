package generator

import (
	"log/slog"
	mathrand "math/rand/v2"
	"strings"
	"sync/atomic"
	"time"
)

// Scenario describes a single traffic generation function.
type Scenario struct {
	Name string
	Func func(targetBytes uint64, targetIP string, targetPort int) (uint64, error)
}

// Mixer rotates between multiple traffic scenarios to avoid detection
// by making the traffic profile look more diverse and natural.
type Mixer struct {
	scenarios  []Scenario
	currentIdx atomic.Int64
	interval   time.Duration
	stopCh     chan struct{}
}

// NewMixer creates a scenario mixer that rotates through the given
// scenarios every `interval`.
func NewMixer(scenarios []Scenario, interval time.Duration) *Mixer {
	return &Mixer{
		scenarios: scenarios,
		interval:  interval,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the rotation timer.
func (m *Mixer) Start() {
	if len(m.scenarios) <= 1 {
		return // No rotation needed
	}
	go m.rotationLoop()
}

// Stop stops the rotation timer.
func (m *Mixer) Stop() {
	close(m.stopCh)
}

// Current returns the currently active scenario.
func (m *Mixer) Current() Scenario {
	idx := int(m.currentIdx.Load())
	if idx >= len(m.scenarios) {
		idx = 0
	}
	return m.scenarios[idx]
}

// All returns all registered scenarios.
func (m *Mixer) All() []Scenario {
	return m.scenarios
}

func (m *Mixer) rotationLoop() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// Start with a random scenario
	m.currentIdx.Store(int64(mathrand.IntN(len(m.scenarios))))
	slog.Debug("mixer: started", "interval", m.interval)

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			// Rotate to next scenario
			next := (int(m.currentIdx.Load()) + 1) % len(m.scenarios)
			m.currentIdx.Store(int64(next))
			slog.Info("mixer: switched scenario",
				"scenario", m.scenarios[next].Name)
		}
	}
}

// DefaultScenarios returns the standard set of traffic generation scenarios.
func DefaultScenarios() []Scenario {
	return []Scenario{
		{
			Name: "udp",
			Func: func(targetBytes uint64, targetIP string, targetPort int) (uint64, error) {
				return generateUDP(targetBytes, targetIP, targetPort)
			},
		},
		{
			Name: "tcp-rst",
			Func: func(targetBytes uint64, targetIP string, targetPort int) (uint64, error) {
				return generateTCPRST(targetBytes, targetIP, targetPort)
			},
		},
		{
			Name: "dns",
			Func: func(targetBytes uint64, _ string, _ int) (uint64, error) {
				return generateDNSQuerySpam(targetBytes, nil)
			},
		},
	}
}

// ScenarioByName returns a scenario function by name.
func ScenarioByName(name string, scenarios []Scenario) (Scenario, bool) {
	for _, s := range scenarios {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}

// FilterScenarios returns only the scenarios whose names match the
// given comma-separated list. If the list is empty or contains "all",
// all scenarios are returned.
func FilterScenarios(names string, all []Scenario) []Scenario {
	if names == "" || names == "all" {
		scenarios := make([]Scenario, len(all))
		copy(scenarios, all)
		return scenarios
	}

	var result []Scenario
	for _, name := range splitComma(names) {
		if s, ok := ScenarioByName(name, all); ok {
			result = append(result, s)
		} else {
			slog.Warn("mixer: unknown scenario, skipping", "scenario", name)
		}
	}

	// Fallback to first scenario if nothing matched
	if len(result) == 0 && len(all) > 0 {
		result = append(result, all[0])
	}
	return result
}

func splitComma(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if trimmed := strings.TrimSpace(s[start:i]); trimmed != "" {
				result = append(result, trimmed)
			}
			start = i + 1
		}
	}
	if trimmed := strings.TrimSpace(s[start:]); trimmed != "" {
		result = append(result, trimmed)
	}
	return result
}
