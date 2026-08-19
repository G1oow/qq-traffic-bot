package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"

	"qqtrafficbot/internal/nft"
	"qqtrafficbot/internal/qqbot"
	"qqtrafficbot/internal/store"
	"qqtrafficbot/internal/traffic"
)

func main() {
	if err := run(); err != nil {
		slog.Error("退出", "error", err)
		os.Exit(1)
	}
}

func run() error {
	check := flag.Bool("check", false, "仅检查 nftables 读取与本地存储，不启动机器人")
	dataDir := flag.String("data", "data", "数据目录")
	envPath := flag.String("env", ".env", ".env 文件路径")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, stop := signal.NotifyContext(context.Background(), stopSignals...)
	defer stop()

	st, err := store.New(*dataDir, time.Local)
	if err != nil {
		return fmt.Errorf("打开存储: %w", err)
	}
	defer st.Close()

	collector := nft.NewCollector()

	if *check {
		return runCheck(ctx, collector, st)
	}

	appID, secret, err := resolveEnv(*envPath)
	if err != nil {
		return fmt.Errorf("加载凭证: %w", err)
	}
	if appID == "" || secret == "" {
		return errors.New("缺少 APPID 或 SECRET")
	}

	monitor := traffic.NewMonitor(alertWindow, alertThreshold, alertCooldown)
	client := qqbot.New(appID, secret)
	app := &App{collector: collector, store: st, monitor: monitor, client: client}

	if err := st.Cleanup(time.Now()); err != nil {
		slog.Error("启动清理失败", "error", err)
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); runCollector(ctx, app) }()
	go func() { defer wg.Done(); runAlertPusher(ctx, app) }()
	go func() { defer wg.Done(); runCleanup(ctx, app) }()

	handler := func(ctx context.Context, msg qqbot.Message) {
		messageHandler(ctx, msg, st, client)
	}

	err = client.Run(ctx, handler)
	stop()
	wg.Wait()
	if err != nil {
		return fmt.Errorf("QQ 客户端: %w", err)
	}
	return nil
}

func runCheck(ctx context.Context, collector *nft.Collector, st *store.Store) error {
	counters, err := collector.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("nftables 读取: %w", err)
	}
	fmt.Printf("nftables 采集成功，共 %d 个 IP\n", len(counters))
	for ip, bytes := range counters {
		fmt.Printf("  %s  %d bytes\n", ip, bytes)
	}
	fmt.Println("本地存储初始化成功")
	return nil
}
