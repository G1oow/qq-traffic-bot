package report

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFiltersAndAggregates(t *testing.T) {
	start := time.Date(2026, 8, 16, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	values := map[string]uint64{
		"192.0.2.1": 2 << 30,
		"192.0.2.2": 1 << 30,
		"192.0.2.3": 512,
		"192.0.2.4": 2 << 20,
	}
	got := Build("近1小时", start, values, 1<<20, 2)
	for _, want := range []string{
		"总流量：3.00 GB",
		"活跃IP：3 个",
		"192.0.2.1",
		"192.0.2.2",
		"其余 1 个 IP",
		"统计起点 2026-08-16 10:00:00",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "192.0.2.3") {
		t.Fatalf("small scan should be filtered:\n%s", got)
	}
}
