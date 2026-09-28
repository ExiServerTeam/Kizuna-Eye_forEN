package alert

import (
	"testing"
	"time"

	"Kizuna-Eye/pkg/notify"
	"Kizuna-Eye/pkg/status"
)

func TestAlertsHaveIcons(t *testing.T) {
	cfg := Config{
		MemoryWarn: 80, MemoryCritical: 90,
		DiskFreeWarn: 20, DiskFreeCritical: 10,
		CPUTempWarn: 70, CPUTempCritical: 85,
		HoldDuration: time.Millisecond, RecoveryHold: time.Millisecond, Cooldown: 0,
		NotifyRecovery: true,
	}
	e := NewEngine(cfg, nil, nil)
	now := time.Now()
	later := now.Add(2 * time.Millisecond)

	assertIcon := func(name string, a *notify.Alert) {
		if a == nil {
			t.Errorf("%s: no alert fired", name)
			return
		}
		if a.Icon == "" {
			t.Errorf("%s: alert has no icon: %+v", name, a)
		}
	}

	// memory: first sets hold start, second fires
	e.evalMemory(95, now)
	assertIcon("memory", e.evalMemory(95, later))

	// disk free
	e.evalDiskFree(5, 100, now)
	assertIcon("disk", e.evalDiskFree(5, 100, later))

	// cpu temp
	e.evalCPUTemp(90, now)
	assertIcon("cpu_temp", e.evalCPUTemp(90, later))

	// disk health
	disks := []status.DiskInfo{{Path: "/dev/sda", Health: "FAILED"}}
	e.evalDiskHealth(disks, now)
	assertIcon("disk_health", e.evalDiskHealth(disks, later))
}

func TestIconForLevel(t *testing.T) {
	cases := map[notify.Level]string{
		notify.LevelCritical: "🚨",
		notify.LevelWarning:  "⚠️",
		notify.LevelSuccess:  "✅",
		notify.LevelInfo:     "ℹ️",
	}
	for lv, want := range cases {
		if got := iconForLevel(lv); got != want {
			t.Errorf("iconForLevel(%s) = %s, want %s", lv, got, want)
		}
	}
}
