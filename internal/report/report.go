package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const barWidth = 12

type Entry struct {
	IP    string
	Bytes uint64
}

func Build(title string, start time.Time, values map[string]uint64, minimum uint64, topN int) string {
	entries := make([]Entry, 0, len(values))
	var total uint64
	for ip, bytes := range values {
		if bytes < minimum {
			continue
		}
		entries = append(entries, Entry{IP: ip, Bytes: bytes})
		total += bytes
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Bytes == entries[j].Bytes {
			return entries[i].IP < entries[j].IP
		}
		return entries[i].Bytes > entries[j].Bytes
	})

	var b strings.Builder
	fmt.Fprintf(&b, "📊 流量汇报 · %s\n", title)
	b.WriteString("━━━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "📦 总流量：%s\n", FormatBytes(total))
	fmt.Fprintf(&b, "🌐 活跃IP：%d 个\n", len(entries))
	b.WriteString("━━━━━━━━━━━━━━\n")

	if len(entries) == 0 {
		fmt.Fprintf(&b, "\n暂无达到 %s 的活跃 IP\n", FormatBytes(minimum))
	} else {
		shown := len(entries)
		if shown > topN {
			shown = topN
		}
		for i := 0; i < shown; i++ {
			writeEntry(&b, rankLabel(i), entries[i].IP, entries[i].Bytes, total)
		}
		if len(entries) > shown {
			var other uint64
			for _, entry := range entries[shown:] {
				other += entry.Bytes
			}
			writeEntry(&b, "📎 其他", fmt.Sprintf("其余 %d 个 IP", len(entries)-shown), other, total)
		}
	}
	fmt.Fprintf(&b, "\n🕒 统计起点 %s", start.Format("2006-01-02 15:04:05"))
	return b.String()
}

func rankLabel(index int) string {
	switch index {
	case 0:
		return "🥇"
	case 1:
		return "🥈"
	case 2:
		return "🥉"
	default:
		return fmt.Sprintf("#%d", index+1)
	}
}

func writeEntry(b *strings.Builder, rank, label string, bytes, total uint64) {
	percentage := 0.0
	if total > 0 {
		percentage = float64(bytes) / float64(total) * 100
	}
	fmt.Fprintf(b, "\n%s  %s · %.1f%%\n", rank, FormatBytes(bytes), percentage)
	b.WriteString(progressBar(percentage))
	b.WriteByte('\n')
	b.WriteString(label)
	b.WriteByte('\n')
}

func progressBar(percentage float64) string {
	filled := int(math.Round(percentage / 100 * barWidth))
	if percentage > 0 && filled == 0 {
		filled = 1
	}
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("▓", filled) + strings.Repeat("░", barWidth-filled)
}

func FormatBytes(bytes uint64) string {
	const (
		kiB = uint64(1 << 10)
		miB = uint64(1 << 20)
		giB = uint64(1 << 30)
	)
	switch {
	case bytes >= giB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(giB))
	case bytes >= miB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(miB))
	case bytes >= kiB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kiB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
