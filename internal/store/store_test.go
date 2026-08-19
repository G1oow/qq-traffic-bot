package store

import (
	"context"
	"testing"
	"time"

	"qqtrafficbot/internal/traffic"
)

func TestRecordQueryAndPendingAlerts(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	deltas := map[string]uint64{"192.0.2.1": 2048}
	counters := map[string]uint64{"192.0.2.1": 2048}
	if err := st.Record(ctx, deltas, counters, now, true); err != nil {
		t.Fatal(err)
	}

	result, err := st.Query(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result["192.0.2.1"] != 2048 {
		t.Fatalf("期望 2048，实际 %d", result["192.0.2.1"])
	}

	// 第二次 Query 应能命中缓存的日库并返回相同结果
	result2, err := st.Query(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result2["192.0.2.1"] != 2048 {
		t.Fatalf("二次查询期望 2048，实际 %d", result2["192.0.2.1"])
	}

	// owner 绑定与读取
	if got, _ := st.Owner(ctx); got != "" {
		t.Fatalf("初始 owner 应为空，实际 %q", got)
	}
	if err := st.BindOwner(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Owner(ctx); got != "user-1" {
		t.Fatalf("owner 期望 user-1，实际 %q", got)
	}
	// 重复绑定不应覆盖（ON CONFLICT DO NOTHING）
	_ = st.BindOwner(ctx, "user-2")
	if got, _ := st.Owner(ctx); got != "user-1" {
		t.Fatalf("重复绑定后 owner 应仍为 user-1，实际 %q", got)
	}

	// 消息去重
	if seen, _ := st.MarkMessageSeen(ctx, "m1", now); !seen {
		t.Fatalf("首次标记 m1 应返回 true")
	}
	if seen, _ := st.MarkMessageSeen(ctx, "m1", now.Add(time.Second)); seen {
		t.Fatalf("重复标记 m1 应返回 false")
	}

	// 报警排队与读取
	alertAt := now.Add(time.Minute)
	if err := st.QueueAlert(ctx, traffic.Alert{IP: "10.0.0.1", Bytes: 5000, At: alertAt}); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingAlerts(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].IP != "10.0.0.1" || pending[0].Bytes != 5000 {
		t.Fatalf("pending 读取异常: %#v", pending)
	}
	if err := st.MarkAlertSent(ctx, pending[0].ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	pending2, _ := st.PendingAlerts(ctx, now.Add(time.Hour))
	if len(pending2) != 0 {
		t.Fatalf("已发送报警不应再出现，实际 %d 条", len(pending2))
	}
}

func TestSaveAndLoadAlertTimes(t *testing.T) {
	dir := t.TempDir()
	st, _ := New(dir, time.UTC)
	defer st.Close()
	ctx := context.Background()

	alerts := map[string]time.Time{
		"10.0.0.1": time.Unix(1700000000, 0),
		"10.0.0.2": time.Unix(1700000060, 0),
	}
	if err := st.SaveAlertTimes(ctx, alerts); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadAlertTimes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded["10.0.0.1"] != alerts["10.0.0.1"] || loaded["10.0.0.2"] != alerts["10.0.0.2"] {
		t.Fatalf("报警时间读取不符: %#v", loaded)
	}
}

func TestLoadRecentAndRestoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	st, _ := New(dir, time.UTC)
	defer st.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	// 写入两次采集，间隔 10 秒
	if err := st.Record(ctx, map[string]uint64{"192.0.2.1": 100}, map[string]uint64{"192.0.2.1": 100}, now, true); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(ctx, map[string]uint64{"192.0.2.1": 200}, map[string]uint64{"192.0.2.1": 300}, now.Add(10*time.Second), true); err != nil {
		t.Fatal(err)
	}
	recent, err := st.LoadRecent(ctx, now.Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || len(recent["192.0.2.1"]) != 2 {
		t.Fatalf("期望 1 IP / 2 点，实际 %#v", recent)
	}
}
