//go:build linux

package main

import (
	"path/filepath"
	"testing"
)

// 改善4: inotify 層のルート毎上限。上限に達したルートは 1 回だけコールバックし、
// それ以上 watch を張らない（黙って即時検知が止まるのを防ぐ）。
//
// linux 限定: addWatch（inotify の watch 登録）は suid_watch_linux.go にしか
// 無い。ビルドタグを付けないと macOS 等で go test ./... がビルドできず、
// 他のテストまで巻き添えで実行できなくなる。
func TestInotifyWatcherPerRootCapWarnsOnce(t *testing.T) {
	root := t.TempDir()
	var got []string
	w := newInotifyWatcher([]string{root}, "FIM", nil, nil,
		withMaxDepth(1),
		withMaxDirsPerRoot(1, func(r string, limit int) { got = append(got, r) }))
	if w.maxDepth != 1 || w.maxDirsPerRoot != 1 {
		t.Fatalf("オプションが反映されていません: depth=%d dirs=%d", w.maxDepth, w.maxDirsPerRoot)
	}

	// inotify を開かずに「既に 1 本 watch がある」状態を作り、上限判定だけを見る。
	w.mu.Lock()
	w.wdToPath[1] = root
	w.mu.Unlock()

	w.addWatch(filepath.Join(root, "sub"))
	w.addWatch(filepath.Join(root, "sub2"))
	if len(got) != 1 || got[0] != root {
		t.Fatalf("ルート毎の上限通知は 1 回だけ出るべきです: %+v", got)
	}
}
