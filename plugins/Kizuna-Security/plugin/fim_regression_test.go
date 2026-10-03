package main

import (
	"os"
	"path/filepath"
	"testing"

	"Kizuna-Eye/pkg/module"
)

// 回帰テスト: ベースライン時に存在しなかった監視対象ファイル（例:
// /etc/ld.so.preload, /root/.ssh/authorized_keys）が後から作成されても、
// 「作成」として critical 検知されなければならない。
//
// 以前は、ハッシュできなかったファイルを「監視対象として既知」に記録して
// いなかったため、後から作られたファイルが「監視対象に新規追加された」と
// 誤判定され、無言でスキップされていた（バックドア作成の見逃し）。
func TestFIMDetectsCreateOfFileMissingAtBaseline(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ld.so.preload")
	baseline := filepath.Join(dir, "fim.json")

	var events []module.SecurityEvent
	emit := func(ev module.SecurityEvent) { events = append(events, ev) }
	fim := NewFIM([]string{target}, baseline, nil, emit)

	// 初回: ファイルが存在しないのでイベントなし。
	fim.Check()
	if len(events) != 0 {
		t.Fatalf("first run should emit nothing, got %d", len(events))
	}

	// 攻撃者がバックドアを作成 → critical を検知する。
	if err := os.WriteFile(target, []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	fim.Check()

	found := false
	for _, ev := range events {
		if ev.Category == "integrity" && ev.Level == "critical" {
			found = true
		}
	}
	if !found {
		t.Fatalf("creation of a file missing at baseline must be a critical integrity event: %+v", events)
	}
}
