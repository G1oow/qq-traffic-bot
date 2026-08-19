package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeDeltas(t *testing.T) {
	baseline := map[string]uint64{"10.0.0.1": 100, "10.0.0.2": 50, "10.0.0.5": 70}
	counters := map[string]uint64{
		"10.0.0.1": 250,
		"10.0.0.2": 30,
		"10.0.0.3": 80,
		"10.0.0.4": 50,
		"10.0.0.5": 70,
	}
	got := computeDeltas(baseline, counters)
	if got["10.0.0.1"] != 150 {
		t.Errorf("delta 1 = %d，期望 150", got["10.0.0.1"])
	}
	if got["10.0.0.2"] != 30 {
		t.Errorf("counter reset delta = %d，期望 30", got["10.0.0.2"])
	}
	if got["10.0.0.3"] != 80 {
		t.Errorf("新增 IP delta = %d，期望 80", got["10.0.0.3"])
	}
	if got["10.0.0.4"] != 50 {
		t.Errorf("新增 IP 从 0 起 delta = %d，期望 50", got["10.0.0.4"])
	}
	if _, ok := got["10.0.0.5"]; ok {
		t.Errorf("零增量 IP 不应出现")
	}
}

func TestLoadEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# 注释行
APPID="123456"
SECRET=abcdef

  SPACED = spaced-value
PLAIN=plain-value
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if got["APPID"] != "123456" {
		t.Errorf("APPID = %q，期望去掉双引号后的 123456", got["APPID"])
	}
	if got["SPACED"] != "spaced-value" {
		t.Errorf("SPACED = %q，期望 spaced-value", got["SPACED"])
	}
	if got["PLAIN"] != "plain-value" {
		t.Errorf("PLAIN = %q，期望 plain-value", got["PLAIN"])
	}
}
