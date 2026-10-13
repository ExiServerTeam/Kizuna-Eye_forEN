package configsnap

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestSnapshotAndRestore(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "dashboard_config.json")

	writeFile(t, cfg, `{"v":1}`)
	if err := Snapshot(cfg); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	writeFile(t, cfg, `{"v":2}`)
	if err := Snapshot(cfg); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	snaps, err := List(cfg)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) < 2 {
		t.Fatalf("want >=2 snapshots, got %d", len(snaps))
	}

	// Restore the oldest snapshot (v1).
	oldest := snaps[len(snaps)-1]
	if err := Restore(cfg, oldest.Name); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile(t, cfg); got != `{"v":1}` {
		t.Errorf("after restore = %q, want v1", got)
	}

	// A restore is not a config change, so it must NOT add a timestamped
	// snapshot (repeated restores would otherwise grow the history and push the
	// real saves out of the GFS window). It only refreshes the single
	// pre-restore slot, which makes the previous restore undoable.
	snaps2, _ := List(cfg)
	if len(snaps2) != len(snaps) {
		t.Errorf("restore changed snapshot count: %d -> %d", len(snaps), len(snaps2))
	}
	if b, ok := LastPreRestore(cfg); !ok || string(b) != `{"v":2}` {
		t.Errorf("pre-restore slot = %q (ok=%v), want v2", string(b), ok)
	}
}

// Repeated restores must not grow the snapshot history.
func TestRepeatedRestoreDoesNotGrowHistory(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "dashboard_config.json")

	writeFile(t, cfg, `{"v":1}`)
	if err := Snapshot(cfg); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snaps, _ := List(cfg)
	if len(snaps) == 0 {
		t.Fatal("no snapshot to restore")
	}
	name := snaps[0].Name

	for i := 0; i < 5; i++ {
		if err := Restore(cfg, name); err != nil {
			t.Fatalf("Restore %d: %v", i, err)
		}
	}

	snaps2, _ := List(cfg)
	if len(snaps2) != len(snaps) {
		t.Errorf("5 restores grew history: %d -> %d", len(snaps), len(snaps2))
	}
}

func TestSnapshotMissingFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "agent_config.json")
	if err := Snapshot(cfg); err != nil {
		t.Fatalf("Snapshot(missing): %v", err)
	}
	snaps, err := List(cfg)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("want 0 snapshots for missing file, got %d", len(snaps))
	}
}

func TestSnapshotMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not honour Unix file modes")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "modules.json")
	writeFile(t, cfg, `[]`)
	if err := Snapshot(cfg); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snaps, _ := List(cfg)
	if len(snaps) == 0 {
		t.Fatal("no snapshot")
	}
	info, err := os.Stat(filepath.Join(snapDir(cfg), snaps[0].Name))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("snapshot mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestRestoreRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "modules.json")
	writeFile(t, cfg, `[]`)
	for _, bad := range []string{"../x.json", "a/b.json", "..", "."} {
		if err := Restore(cfg, bad); err == nil {
			t.Errorf("Restore(%q) = nil, want error", bad)
		}
	}
}

func TestPruneGFS(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "dashboard_config.json")
	writeFile(t, cfg, `{}`)

	snapDirPath := snapDir(cfg)
	if err := os.MkdirAll(snapDirPath, 0700); err != nil {
		t.Fatal(err)
	}
	base := baseName(cfg)
	now := time.Now().UTC()

	// 40 snapshots, one per day over the last 40 days.
	for i := 0; i < 40; i++ {
		ts := now.AddDate(0, 0, -i)
		name := base + "." + ts.Format(timeLayout) + ".json"
		writeFile(t, filepath.Join(snapDirPath, name), `{}`)
	}

	mu.Lock()
	err := pruneLocked(cfg, now)
	mu.Unlock()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	snaps, _ := List(cfg)
	// Must keep the newest 20 and at least the daily/weekly buckets; must drop
	// the older ones beyond the monthly window.
	if len(snaps) == 0 {
		t.Fatal("prune removed everything")
	}
	if len(snaps) > 40 {
		t.Fatalf("prune kept more than existed: %d", len(snaps))
	}
	// Newest must remain.
	if !snaps[0].Time.After(now.AddDate(0, 0, -1)) && !snaps[0].Time.Equal(now) {
		t.Errorf("newest snapshot unexpectedly removed: %v", snaps[0].Time)
	}
}
