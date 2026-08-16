package traffic

import (
	"testing"
	"time"
)

func TestMonitorRollingWindowAndCooldown(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	m := NewMonitor(5*time.Minute, 100, 30*time.Minute)

	if alerts := m.Add(now, map[string]uint64{"192.0.2.1": 60}, true); len(alerts) != 0 {
		t.Fatalf("unexpected alert: %#v", alerts)
	}
	alerts := m.Add(now.Add(time.Minute), map[string]uint64{"192.0.2.1": 40}, true)
	if len(alerts) != 1 || alerts[0].Bytes != 100 {
		t.Fatalf("alerts = %#v", alerts)
	}
	if alerts := m.Add(now.Add(2*time.Minute), map[string]uint64{"192.0.2.1": 50}, true); len(alerts) != 0 {
		t.Fatalf("cooldown failed: %#v", alerts)
	}

	// Once the old burst leaves the window, the alert state is re-armed.
	m.Add(now.Add(7*time.Minute), nil, true)
	alerts = m.Add(now.Add(8*time.Minute), map[string]uint64{"192.0.2.1": 100}, true)
	if len(alerts) != 1 {
		t.Fatalf("expected re-armed alert, got %#v", alerts)
	}
}
