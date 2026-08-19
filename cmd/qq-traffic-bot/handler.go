package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"qqtrafficbot/internal/qqbot"
	"qqtrafficbot/internal/report"
	"qqtrafficbot/internal/store"
)

const (
	infoWindow    = time.Hour
	reportWindow  = 6 * time.Hour
	reportMinimum = uint64(1 << 20)
	reportTopN    = 15
)

const helpText = `QQ 流量监控机器人 · 指令列表
/help    显示本帮助
/info    查询过去 1 小时流量
/report  查询过去 6 小时流量

首个发送指令的用户将被绑定为主动报警接收人。`

func messageHandler(ctx context.Context, msg qqbot.Message, st *store.Store, client *qqbot.Client) {
	now := time.Now()
	seen, err := st.MarkMessageSeen(ctx, msg.ID, now)
	if err != nil {
		slog.Error("消息去重失败", "error", err)
		return
	}
	if !seen {
		return
	}

	owner, err := st.Owner(ctx)
	if err != nil {
		slog.Error("查询 owner 失败", "error", err)
		return
	}
	if owner == "" {
		if err := st.BindOwner(ctx, msg.Author.OpenID()); err != nil {
			slog.Error("绑定 owner 失败", "error", err)
		} else {
			slog.Info("已绑定主动报警接收人", "open_id", msg.Author.OpenID())
		}
	}

	content := strings.TrimSpace(msg.Content)
	var reply string
	switch content {
	case "/help":
		reply = helpText
	case "/info":
		reply = buildReport(ctx, st, "近1小时", infoWindow)
	case "/report":
		reply = buildReport(ctx, st, "近6小时", reportWindow)
	default:
		return
	}
	if err := client.SendReply(ctx, msg.Author.OpenID(), msg.ID, reply); err != nil {
		slog.Error("回复消息失败", "error", err)
	}
}

func buildReport(ctx context.Context, st *store.Store, title string, window time.Duration) string {
	end := time.Now()
	start := end.Add(-window)
	values, err := st.Query(ctx, start, end)
	if err != nil {
		slog.Error("流量查询失败", "error", err)
		return fmt.Sprintf("查询失败：%v", err)
	}
	return report.Build(title, start, values, reportMinimum, reportTopN)
}
