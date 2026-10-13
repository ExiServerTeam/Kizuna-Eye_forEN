package main

// 一時運用プローブ。KIZUNA_FIM_PROBE=1 のときだけ動き、実ルート（/tmp など）を
// 新しいコードで走査して「再起動後にどの警告が出るか」を事前に確認する。
// 通知は emitFn のローカル関数に落ちるだけで、エージェントやダッシュボードには
// 送られない。ベースラインも t.TempDir() に書くので本番状態は触らない。

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"Kizuna-Eye/pkg/module"
)

func TestFIMLiveProbeRealRoots(t *testing.T) {
	if os.Getenv("KIZUNA_FIM_PROBE") != "1" {
		t.Skip("probe disabled (set KIZUNA_FIM_PROBE=1)")
	}
	roots := strings.Split(os.Getenv("KIZUNA_FIM_ROOTS"), ",")
	ignore := strings.Split(os.Getenv("KIZUNA_FIM_IGNORE"), ",")
	maxDepth, _ := strconv.Atoi(os.Getenv("KIZUNA_FIM_MAX_DEPTH"))
	if maxDepth == 0 {
		maxDepth = 3
	}
	maxDirs, _ := strconv.Atoi(os.Getenv("KIZUNA_FIM_MAX_DIRS"))
	if maxDirs == 0 {
		maxDirs = 1024
	}

	var events []string
	f := NewFIMDirWatcher(roots, ignore, filepath.Join(t.TempDir(), "probe.json"), 3600, 4096, 4096, nil,
		func(ev module.SecurityEvent) { events = append(events, ev.Level+" | "+ev.Title+" | "+ev.Message) },
		nil, WithFIMDirMaxDepth(maxDepth), WithFIMDirMaxDirs(maxDirs, nil))

	// 1 回目は静かにベースラインを記録する。
	f.Check()
	f.mu.Lock()
	n := len(f.baseline)
	f.mu.Unlock()

	// 2 回目で「走査できていない範囲」があれば warning が出る（状態変化時のみ）。
	f.Check()
	f.mu.Lock()
	n2, degraded := len(f.baseline), f.degraded
	f.mu.Unlock()

	t.Logf("baseline=%d entries (2nd scan=%d) degraded=%v depth=%d dirsCap=%d",
		n, n2, degraded, maxDepth, maxDirs)
	sort.Strings(events)
	for _, e := range events {
		t.Logf("EVENT: %s", e)
	}
}
