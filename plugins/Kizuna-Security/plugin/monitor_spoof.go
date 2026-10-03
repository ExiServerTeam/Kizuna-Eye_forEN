package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// spoofCache keeps recent (message -> seenAt) of auth lines that journald
// attributes to a non-sshd origin (e.g. the logger command). A matching
// auth.log line is a forged entry (V5: log injection).
const (
	spoofMaxEntries = 100
	spoofTTL        = 5 * time.Minute
)

type spoofCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func newSpoofCache() *spoofCache {
	return &spoofCache{entries: make(map[string]time.Time)}
}

// add records a forged message. It also prunes expired entries and bounds
// the map size (oldest evicted) so a flood cannot grow it without limit.
func (c *spoofCache) add(msg string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, t := range c.entries {
		if now.Sub(t) > spoofTTL {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= spoofMaxEntries {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, t := range c.entries {
			if first || t.Before(oldest) {
				oldest, oldestKey, first = t, k, false
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[msg] = now
}

// contains reports whether line was recently seen as a forged auth line.
// The journald MESSAGE is the body only, while the auth.log line carries a
// timestamp/host/tag prefix, so match by substring.
func (c *spoofCache) contains(line string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for msg, t := range c.entries {
		if now.Sub(t) > spoofTTL {
			delete(c.entries, msg)
			continue
		}
		if msg != "" && strings.Contains(line, msg) {
			return true
		}
	}
	return false
}

// startSpoofWatch tails journald and records auth-facility messages whose
// origin is not the real sshd (SYSLOG_IDENTIFIER says sshd but _COMM is not
// sshd / _UID is not 0). If journalctl is unavailable, the watch is disabled
// and the monitor keeps working with the plain auth.log parse.
func (m *Monitor) startSpoofWatch(ctx context.Context) {
	// Configure may run before Init, so ctx can be nil here. Never pass a
	// nil context to exec.CommandContext (it panics).
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := exec.LookPath("journalctl"); err != nil {
		if m.logger != nil {
			m.logger.Info("Kizuna-Security: journalctl 未検出のため偽装ログ検知は無効")
		}
		return
	}
	m.spoofCancel = make(chan struct{})
	go m.spoofWatchLoop(ctx)
}

func (m *Monitor) spoofWatchLoop(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "journalctl", "-o", "json", "-f", "-n", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("Kizuna-Security: journalctl 起動失敗: %v", err)
		}
		return
	}
	if err := cmd.Start(); err != nil {
		if m.logger != nil {
			m.logger.Warn("Kizuna-Security: journalctl 起動失敗: %v", err)
		}
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		case <-m.spoofCancel:
			return
		default:
		}
		var e struct {
			Message          string `json:"MESSAGE"`
			Comm             string `json:"_COMM"`
			UID              string `json:"_UID"`
			SyslogIdentifier string `json:"SYSLOG_IDENTIFIER"`
			SyslogFacility   string `json:"SYSLOG_FACILITY"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		// Only auth-facility messages are relevant to auth.log.
		// 4 = auth, 10 = authpriv.
		if e.SyslogFacility != "4" && e.SyslogFacility != "10" {
			continue
		}
		// Only entries that CLAIM to be sshd (SYSLOG_IDENTIFIER=sshd) but
		// whose real origin is not the privileged sshd process are forgeries
		// (e.g. logger -t sshd). Legitimate sudo/systemd auth entries have a
		// different SYSLOG_IDENTIFIER and must NOT be flagged.
		if e.SyslogIdentifier == "sshd" && (e.Comm != "sshd" || e.UID != "0") {
			if e.Message != "" {
				m.spoof.add(e.Message, time.Now())
			}
		}
	}
}
