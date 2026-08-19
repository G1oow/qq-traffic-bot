package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"qqtrafficbot/internal/nft"
	"qqtrafficbot/internal/qqbot"
	"qqtrafficbot/internal/report"
	"qqtrafficbot/internal/store"
	"qqtrafficbot/internal/traffic"
)

const (
	collectInterval = 5 * time.Second
	alertWindow     = 5 * time.Minute
	alertThreshold  = uint64(7 << 30) // 7 GiB：30 MB/s 带宽的 5 分钟理论上限约 8.8 GiB，留约 20% 余量后取整，持续 ≥85% 带宽 5 分钟才报警
	alertCooldown   = 30 * time.Minute
	alertPushEvery  = 60 * time.Second
	cleanupEvery    = 24 * time.Hour
)

type App struct {
	collector *nft.Collector
	store     *store.Store
	monitor   *traffic.Monitor
	client    *qqbot.Client
}

func runCollector(ctx context.Context, app *App) {
	baseline, baselineAt, err := app.store.LoadSnapshot(ctx)
	if err != nil {
		slog.Error("加载基线失败", "error", err)
	} else if !baselineAt.IsZero() {
		slog.Info("已加载流量基线", "at", baselineAt, "ips", len(baseline))
	}

	if recent, err := app.store.LoadRecent(ctx, time.Now().Add(-alertWindow)); err != nil {
		slog.Error("加载近窗数据失败", "error", err)
	} else {
		alertTimes, err := app.store.LoadAlertTimes(ctx)
		if err != nil {
			slog.Error("加载报警冷却失败", "error", err)
		}
		app.monitor.Restore(recent, alertTimes, time.Now())
	}

	firstRun := baselineAt.IsZero()
	ticker := time.NewTicker(collectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			baseline, firstRun = collectOnce(ctx, app, baseline, firstRun)
		}
	}
}

func collectOnce(ctx context.Context, app *App, baseline map[string]uint64, firstRun bool) (map[string]uint64, bool) {
	now := time.Now()
	counters, err := app.collector.Snapshot(ctx)
	if err != nil {
		slog.Error("采集 nftables 失败", "error", err)
		return baseline, firstRun
	}

	if firstRun {
		if err := app.store.Record(ctx, nil, counters, now, false); err != nil {
			slog.Error("保存基线失败", "error", err)
		}
		slog.Info("已建立流量基线", "ips", len(counters))
		return counters, false
	}

	deltas := computeDeltas(baseline, counters)
	alerts := app.monitor.Add(now, deltas, true)
	for _, alert := range alerts {
		slog.Warn("触发流量报警", "ip", alert.IP, "bytes", alert.Bytes)
		if err := app.store.QueueAlert(ctx, alert); err != nil {
			slog.Error("排队报警失败", "error", err)
		}
	}
	if err := app.store.Record(ctx, deltas, counters, now, true); err != nil {
		slog.Error("写入流量数据失败", "error", err)
	}
	return counters, false
}

func computeDeltas(baseline, counters map[string]uint64) map[string]uint64 {
	var deltas map[string]uint64
	for ip, cur := range counters {
		prev := baseline[ip]
		var d uint64
		if cur >= prev {
			d = cur - prev
		} else {
			d = cur
		}
		if d == 0 {
			continue
		}
		if deltas == nil {
			deltas = make(map[string]uint64)
		}
		deltas[ip] = d
	}
	return deltas
}

func runAlertPusher(ctx context.Context, app *App) {
	ticker := time.NewTicker(alertPushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pushAlerts(ctx, app)
		}
	}
}

func pushAlerts(ctx context.Context, app *App) {
	owner, err := app.store.Owner(ctx)
	if err != nil {
		slog.Error("查询 owner 失败", "error", err)
		return
	}
	if owner == "" {
		return
	}
	pending, err := app.store.PendingAlerts(ctx, time.Now())
	if err != nil {
		slog.Error("查询待发报警失败", "error", err)
		return
	}
	for _, item := range pending {
		content := fmt.Sprintf("⚠️ 流量报警\nIP：%s\n窗口流量：%s\n触发时间：%s",
			item.IP, report.FormatBytes(item.Bytes), item.OccurredAt.In(time.Local).Format("2006-01-02 15:04:05"))
		if err := app.client.SendActive(ctx, owner, content); err != nil {
			slog.Error("推送报警失败", "error", err, "ip", item.IP)
			return
		}
		if err := app.store.MarkAlertSent(ctx, item.ID, time.Now()); err != nil {
			slog.Error("标记报警已发失败", "error", err)
		}
	}
}

func runCleanup(ctx context.Context, app *App) {
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := app.store.Cleanup(time.Now()); err != nil {
				slog.Error("清理旧流量数据失败", "error", err)
			}
		}
	}
}
