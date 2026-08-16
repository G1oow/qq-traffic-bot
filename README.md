# QQ Sing-box 流量监控

一个运行在 Linux VPS 上的 Go 单进程服务。它读取 nftables 中 `perip4/hitv4` 和 `perip6/hitv6` 的逐 IP 计数，通过 QQ 开放平台官方 WebSocket Gateway 接收 C2C 指令，并在流量异常时主动报警。

## 功能

- 每 5 秒采集一次 IPv4/IPv6 流量计数。
- `/info` 查询过去 1 小时流量。
- `/report` 查询过去 6 小时流量。
- 单 IP 在滚动 5 分钟内达到 40 GiB 时报警，同一 IP 30 分钟内只提醒一次。
- 报告仅显示查询窗口内流量达到 1 MiB 的 IP，展示 Top 15，其余 IP 合并统计。
- 首个发送 C2C 消息的用户会被绑定为主动报警接收人。
- SQLite 数据默认保留 7 天；流量库超过 500 MiB 时自动清理至 450 MiB 以下。

## 前置条件

- Debian、Ubuntu 或其他使用 `systemd` 的 Linux VPS，支持 `amd64` 或 `arm64`。
- 已安装并启用 nftables。
- 已存在以下带 `counter` 的 nftables set：
  - IPv4：family `ip`、table `perip4`、set `hitv4`
  - IPv6：family `ip6`、table `perip6`、set `hitv6`
- 已存在 `nft-perip.service`，用于在本服务启动前恢复上述 nftables 规则。
- 已在 QQ 开放平台创建机器人，并取得 `APPID` 与 `SECRET`。

可先在 VPS 上验证 nftables 数据是否可读：

```bash
sudo nft -j list set ip perip4 hitv4
sudo nft -j list set ip6 perip6 hitv6
```

## VPS 一键部署

仓库为私有时，先在 VPS 配置有权限的 GitHub SSH key，然后执行：

```bash
git clone git@github.com:G1oow/qq-traffic-bot.git
cd qq-traffic-bot
sudo bash deploy/install.sh
```

脚本会完成以下操作：

- 安装构建所需的基础依赖；系统 Go 版本不足时临时下载 Go 1.25.0。
- 检查 `nft-perip.service` 和两组 nftables set。
- 构建并安装程序到 `/opt/qq-traffic-bot/qq-traffic-bot`。
- 交互式读取 QQ `APPID` 和 `SECRET`，写入权限为 `600` 的 `/opt/qq-traffic-bot/.env`。
- 安装、启用并启动 `qq-traffic-bot.service`。

已有配置会自动复用。也可以在自动化环境中通过环境变量传入凭证：

```bash
sudo --preserve-env=APPID,SECRET bash deploy/install.sh
```

部署完成后，向机器人发送 `/help`。首次发送者会成为主动报警接收人。

## 更新

在原 checkout 中拉取新代码并再次运行部署脚本：

```bash
git pull --ff-only
sudo bash deploy/install.sh
```

## 运维

```bash
# 查看状态
sudo systemctl status qq-traffic-bot

# 实时日志
sudo journalctl -u qq-traffic-bot -f

# 重启服务
sudo systemctl restart qq-traffic-bot

# 停止服务
sudo systemctl stop qq-traffic-bot

# 仅检查 nftables 读取和本地存储
cd /opt/qq-traffic-bot
sudo ./qq-traffic-bot -check
```

## 本地开发

需要 Go 1.25 或更高版本：

```powershell
go test ./...
go build ./cmd/qq-traffic-bot
```

本地运行时，在项目根目录创建 `.env`：

```dotenv
APPID=QQ机器人AppID
SECRET=QQ机器人AppSecret
```

`.env`、运行数据和编译产物均已加入 `.gitignore`。

## 数据目录

- `/opt/qq-traffic-bot/data/state.db`：采集基线、5 分钟滚动窗口、报警状态和接收人绑定。
- `/opt/qq-traffic-bot/data/traffic-YYYY-MM-DD.db`：按分钟和 IP 聚合的流量。
