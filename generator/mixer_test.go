package generator

import (
	"testing"
	"time"
)

// mockScenarioFunc is a minimal scenario function for testing.
func mockScenarioFunc(targetBytes uint64, targetIP string, targetPort int) (uint64, error) {
	return targetBytes, nil
}

func makeTestScenarios() []Scenario {
	return []Scenario{
		{Name: "udp", Func: mockScenarioFunc},
		{Name: "tcp-rst", Func: mockScenarioFunc},
		{Name: "dns", Func: mockScenarioFunc},
		{Name: "http-rst", Func: mockScenarioFunc},
	}
}

func TestFilterScenarios_All(t *testing.T) {
	all := makeTestScenarios()
	result := FilterScenarios("all", all)
	if len(result) != len(all) {
		t.Errorf("FilterScenarios(\"all\") = %d scenarios, want %d", len(result), len(all))
	}
}

func TestFilterScenarios_Empty(t *testing.T) {
	all := makeTestScenarios()
	result := FilterScenarios("", all)
	if len(result) != len(all) {
		t.Errorf("FilterScenarios(\"\") = %d scenarios, want %d", len(result), len(all))
	}
}

func TestFilterScenarios_Single(t *testing.T) {
	all := makeTestScenarios()
	result := FilterScenarios("udp", all)
	if len(result) != 1 {
		t.Fatalf("FilterScenarios(\"udp\") = %d scenarios, want 1", len(result))
	}
	if result[0].Name != "udp" {
		t.Errorf("got scenario %q, want \"udp\"", result[0].Name)
	}
}

func TestFilterScenarios_CommaSeparated(t *testing.T) {
	all := makeTestScenarios()
	result := FilterScenarios("udp,dns", all)
	if len(result) != 2 {
		t.Fatalf("FilterScenarios(\"udp,dns\") = %d scenarios, want 2", len(result))
	}
	if result[0].Name != "udp" || result[1].Name != "dns" {
		t.Errorf("got %q, %q, want \"udp\", \"dns\"", result[0].Name, result[1].Name)
	}
}

func TestFilterScenarios_UnknownFallback(t *testing.T) {
	all := makeTestScenarios()
	result := FilterScenarios("nonexistent", all)
	if len(result) != 1 {
		t.Fatalf("expected fallback to 1 scenario, got %d", len(result))
	}
	if result[0].Name != "udp" {
		t.Errorf("fallback should be first scenario (udp), got %q", result[0].Name)
	}
}

func TestFilterScenarios_PartialMatch(t *testing.T) {
	all := makeTestScenarios()
	result := FilterScenarios("udp,nonexistent", all)
	if len(result) != 1 {
		t.Fatalf("expected 1 matching scenario, got %d", len(result))
	}
	if result[0].Name != "udp" {
		t.Errorf("got %q, want \"udp\"", result[0].Name)
	}
}

func TestScenarioByName(t *testing.T) {
	all := makeTestScenarios()

	tests := []struct {
		name     string
		lookup   string
		wantOk   bool
		wantName string
	}{
		{"found", "udp", true, "udp"},
		{"found-tcp", "tcp-rst", true, "tcp-rst"},
		{"not-found", "quic", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := ScenarioByName(tt.lookup, all)
			if ok != tt.wantOk {
				t.Errorf("ScenarioByName(%q) ok = %v, want %v", tt.lookup, ok, tt.wantOk)
			}
			if ok && s.Name != tt.wantName {
				t.Errorf("ScenarioByName(%q).Name = %q, want %q", tt.lookup, s.Name, tt.wantName)
			}
		})
	}
}

func TestMixer_Rotation(t *testing.T) {
	scenarios := makeTestScenarios()
	mixer := NewMixer(scenarios, 50*time.Millisecond)
	mixer.Start()
	defer mixer.Stop()

	// Wait for at least 3× the rotation interval to ensure at least one rotation.
	time.Sleep(160 * time.Millisecond)

	current := mixer.Current()
	if current.Name == "" {
		t.Error("mixer.Current() returned empty scenario name")
	}
}

func TestMixer_CurrentBounds(t *testing.T) {
	scenarios := makeTestScenarios()
	mixer := NewMixer(scenarios, time.Hour) // won't rotate during test
	mixer.Start()
	defer mixer.Stop()

	current := mixer.Current()
	found := false
	for _, s := range scenarios {
		if s.Name == current.Name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("mixer current (%q) not in registered scenarios", current.Name)
	}
}

func TestMixer_All(t *testing.T) {
	scenarios := makeTestScenarios()
	mixer := NewMixer(scenarios, time.Hour)
	all := mixer.All()
	if len(all) != len(scenarios) {
		t.Errorf("mixer.All() = %d scenarios, want %d", len(all), len(scenarios))
	}
}

func TestMixer_SingleScenario(t *testing.T) {
	scenarios := []Scenario{{Name: "udp", Func: mockScenarioFunc}}
	mixer := NewMixer(scenarios, 50*time.Millisecond)
	mixer.Start() // should no-op since len <= 1
	mixer.Stop()  // safe to call even if not started

	current := mixer.Current()
	if current.Name != "udp" {
		t.Errorf("expected \"udp\", got %q", current.Name)
	}
}

func TestFormatScenarioNames(t *testing.T) {
	tests := []struct {
		scenarios []Scenario
		want      string
	}{
		{nil, "none"},
		{[]Scenario{}, "none"},
		{[]Scenario{{Name: "udp"}}, "udp"},
		{[]Scenario{{Name: "udp"}, {Name: "dns"}}, "udp, dns"},
	}

	for _, tt := range tests {
		got := formatScenarioNames(tt.scenarios)
		if got != tt.want {
			t.Errorf("formatScenarioNames(%v) = %q, want %q", tt.scenarios, got, tt.want)
		}
	}
}

func TestSplitComma(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"udp", []string{"udp"}},
		{"udp,dns", []string{"udp", "dns"}},
		{"udp, dns,http-rst", []string{"udp", "dns", "http-rst"}},
	}

	for _, tt := range tests {
		got := splitComma(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitComma(%q) len = %d, want %d", tt.input, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitComma(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}
