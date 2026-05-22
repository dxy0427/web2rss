package btbtla

import (
	"strings"
	"testing"
	"time"

	"web2rss/shared"
)

// 复制 Scrape 内部的解析逻辑，独立测试，避免改动业务代码结构。
func parseSeedTime(timeText string, cst *time.Location) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", timeText); err == nil {
		return t, true
	}
	trimmed := timeText
	if i := strings.Index(trimmed, " +"); i > 0 {
		trimmed = trimmed[:i]
	} else if i := strings.Index(trimmed, " -"); i > 0 {
		trimmed = trimmed[:i]
	}
	if t, err := time.ParseInLocation(shared.TimeLayout, trimmed, cst); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func TestParseSeedTime(t *testing.T) {
	cst, _ := time.LoadLocation("Asia/Shanghai")
	cases := []struct {
		in   string
		want string // RFC3339 in +08:00
	}{
		{"2026-05-19 20:30:40 +0800 CST", "2026-05-19T20:30:40+08:00"},
		{"2024-01-02 03:04:05 +0800 CST", "2024-01-02T03:04:05+08:00"},
		{"2024-01-02 03:04:05", "2024-01-02T03:04:05+08:00"}, // 旧格式 fallback
	}
	for _, c := range cases {
		got, ok := parseSeedTime(c.in, cst)
		if !ok {
			t.Errorf("parse failed: %q", c.in)
			continue
		}
		if got.In(cst).Format(time.RFC3339) != c.want {
			t.Errorf("input=%q got=%s want=%s", c.in, got.In(cst).Format(time.RFC3339), c.want)
		}
	}

	if _, ok := parseSeedTime("garbage", cst); ok {
		t.Errorf("expected failure on garbage input")
	}
}
