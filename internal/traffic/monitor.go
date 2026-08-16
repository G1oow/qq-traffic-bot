package traffic

import (
	"sort"
	"time"
)

type Point struct {
	At    time.Time
	Bytes uint64
}

type Alert struct {
	IP    string
	Bytes uint64
	At    time.Time
}

type Monitor struct {
	window    time.Duration
	threshold uint64
	cooldown  time.Duration
	points    map[string][]Point
	totals    map[string]uint64
	lastAlert map[string]time.Time
}

func NewMonitor(window time.Duration, threshold uint64, cooldown time.Duration) *Monitor {
	return &Monitor{
		window:    window,
		threshold: threshold,
		cooldown:  cooldown,
		points:    make(map[string][]Point),
		totals:    make(map[string]uint64),
		lastAlert: make(map[string]time.Time),
	}
}

func (m *Monitor) Restore(points map[string][]Point, lastAlert map[string]time.Time, now time.Time) {
	for ip, values := range points {
		for _, point := range values {
			if point.At.After(now.Add(-m.window)) {
				m.points[ip] = append(m.points[ip], point)
				m.totals[ip] += point.Bytes
			}
		}
		sort.Slice(m.points[ip], func(i, j int) bool {
			return m.points[ip][i].At.Before(m.points[ip][j].At)
		})
	}
	for ip, at := range lastAlert {
		m.lastAlert[ip] = at
	}
}

func (m *Monitor) Add(now time.Time, deltas map[string]uint64, evaluate bool) []Alert {
	for ip, bytes := range deltas {
		if bytes == 0 {
			continue
		}
		m.points[ip] = append(m.points[ip], Point{At: now, Bytes: bytes})
		m.totals[ip] += bytes
	}

	cutoff := now.Add(-m.window)
	alerts := make([]Alert, 0)
	for ip, values := range m.points {
		first := 0
		for first < len(values) && !values[first].At.After(cutoff) {
			m.totals[ip] -= values[first].Bytes
			first++
		}
		if first > 0 {
			values = append([]Point(nil), values[first:]...)
			m.points[ip] = values
		}
		if len(values) == 0 {
			delete(m.points, ip)
			delete(m.totals, ip)
			delete(m.lastAlert, ip)
			continue
		}

		total := m.totals[ip]
		if total < m.threshold {
			delete(m.lastAlert, ip)
			continue
		}
		if !evaluate {
			continue
		}
		last := m.lastAlert[ip]
		if last.IsZero() || now.Sub(last) >= m.cooldown {
			m.lastAlert[ip] = now
			alerts = append(alerts, Alert{IP: ip, Bytes: total, At: now})
		}
	}
	return alerts
}

func (m *Monitor) LastAlerts() map[string]time.Time {
	result := make(map[string]time.Time, len(m.lastAlert))
	for ip, at := range m.lastAlert {
		result[ip] = at
	}
	return result
}
