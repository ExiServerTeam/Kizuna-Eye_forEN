package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

const maxBlockedIPs = 10000

var (
	sudoPath        = resolveBin("/usr/bin/sudo", "sudo")
	iptablesPath    = resolveBin("/usr/sbin/iptables", "iptables")
	nftPath         = resolveBin("/usr/sbin/nft", "nft")
	firewallCmdPath = resolveBin("/usr/bin/firewall-cmd", "firewall-cmd")
)

func resolveBin(preferred, fallback string) string {
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}
	return fallback
}

// ============================================================
// firewall backend abstraction
// ============================================================

type firewall interface {
	Name() string
	ApplyBlock(ip string) error
	RemoveBlock(ip string) error
}

type iptablesFirewall struct{}

func (f *iptablesFirewall) Name() string { return "iptables" }

func (f *iptablesFirewall) ApplyBlock(ip string) error {
	if !isValidIPArg(ip) {
		return fmt.Errorf("invalid ip: %s", ip)
	}
	return exec.Command(sudoPath, "-n", iptablesPath, "-I", "INPUT", "-s", ip, "-j", "DROP").Run()
}

func (f *iptablesFirewall) RemoveBlock(ip string) error {
	if !isValidIPArg(ip) {
		return fmt.Errorf("invalid ip: %s", ip)
	}
	return exec.Command(sudoPath, "-n", iptablesPath, "-D", "INPUT", "-s", ip, "-j", "DROP").Run()
}

type nftablesFirewall struct {
	mu    sync.Mutex
	ready bool
}

func (f *nftablesFirewall) Name() string { return "nftables" }

func (f *nftablesFirewall) ensure() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ready {
		return nil
	}
	_ = exec.Command(sudoPath, "-n", nftPath, "delete", "table", "inet", "kizuna").Run()
	cmds := [][]string{
		{"add", "table", "inet", "kizuna"},
		{"add", "set", "inet", "kizuna", "blocked4", "{", "type", "ipv4_addr", ";", "}"},
		{"add", "set", "inet", "kizuna", "blocked6", "{", "type", "ipv6_addr", ";", "}"},
		{"add", "chain", "inet", "kizuna", "input", "{", "type", "filter", "hook", "input", "priority", "-10", ";", "policy", "accept", ";", "}"},
		{"add", "rule", "inet", "kizuna", "input", "ip", "saddr", "@blocked4", "counter", "drop"},
		{"add", "rule", "inet", "kizuna", "input", "ip6", "saddr", "@blocked6", "counter", "drop"},
	}
	for _, args := range cmds {
		full := append([]string{"-n", nftPath}, args...)
		if out, err := exec.Command(sudoPath, full...).CombinedOutput(); err != nil {
			return fmt.Errorf("nft %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	f.ready = true
	return nil
}

func (f *nftablesFirewall) setFor(ip string) string {
	if net.ParseIP(ip).To4() == nil {
		return "blocked6"
	}
	return "blocked4"
}

func (f *nftablesFirewall) ApplyBlock(ip string) error {
	if !isValidIPArg(ip) {
		return fmt.Errorf("invalid ip: %s", ip)
	}
	if err := f.ensure(); err != nil {
		return err
	}
	return exec.Command(sudoPath, "-n", nftPath, "add", "element", "inet", "kizuna", f.setFor(ip), "{", ip, "}").Run()
}

func (f *nftablesFirewall) RemoveBlock(ip string) error {
	if !isValidIPArg(ip) {
		return fmt.Errorf("invalid ip: %s", ip)
	}
	return exec.Command(sudoPath, "-n", nftPath, "delete", "element", "inet", "kizuna", f.setFor(ip), "{", ip, "}").Run()
}

type firewalldFirewall struct{}

func (f *firewalldFirewall) Name() string { return "firewalld" }

func (f *firewalldFirewall) rule(ip string) string {
	return fmt.Sprintf("rule source address=\"%s\" drop", ip)
}

func (f *firewalldFirewall) ApplyBlock(ip string) error {
	if !isValidIPArg(ip) {
		return fmt.Errorf("invalid ip: %s", ip)
	}
	return exec.Command(sudoPath, "-n", firewallCmdPath, "--add-rich-rule", f.rule(ip)).Run()
}

func (f *firewalldFirewall) RemoveBlock(ip string) error {
	if !isValidIPArg(ip) {
		return fmt.Errorf("invalid ip: %s", ip)
	}
	return exec.Command(sudoPath, "-n", firewallCmdPath, "--remove-rich-rule", f.rule(ip)).Run()
}

func detectFirewall(preferred string) firewall {
	switch preferred {
	case "iptables":
		return &iptablesFirewall{}
	case "nftables":
		return &nftablesFirewall{}
	case "firewalld":
		return &firewalldFirewall{}
	}
	if _, err := exec.LookPath(firewallCmdPath); err == nil {
		if err := exec.Command(firewallCmdPath, "--state").Run(); err == nil {
			return &firewalldFirewall{}
		}
	}
	if _, err := exec.LookPath(nftPath); err == nil {
		return &nftablesFirewall{}
	}
	return &iptablesFirewall{}
}

// ============================================================
// blocker
// ============================================================

type blocker struct {
	mode      string
	duration  time.Duration
	whitelist []*net.IPNet
	lang      string
	statePath string
	logger    module.Logger
	emitFn    func(module.SecurityEvent)
	fw        firewall

	mu      sync.Mutex
	blocked map[string]time.Time
}

// blockState is the persistence format for active blocks.
type blockState struct {
	Blocks map[string]time.Time `json:"blocks"`
}

func newBlocker(cfg *SecurityConfig, logger module.Logger, emitFn func(module.SecurityEvent)) *blocker {
	b := &blocker{
		mode:      cfg.BlockMode,
		duration:  time.Duration(cfg.BlockDurationSec) * time.Second,
		lang:      cfg.Language,
		statePath: cfg.BlockStatePath,
		logger:    logger,
		emitFn:    emitFn,
		fw:        detectFirewall(cfg.FirewallBackend),
		blocked:   make(map[string]time.Time),
	}
	for _, w := range cfg.BlockWhitelist {
		if ip := net.ParseIP(w); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			b.whitelist = append(b.whitelist, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		if _, cidr, err := net.ParseCIDR(w); err == nil {
			b.whitelist = append(b.whitelist, cidr)
		}
	}
	if cfg.BlockMode == "enforce" {
		b.restoreState()
	}
	if logger != nil && cfg.BlockMode != "off" {
		logger.Info("Kizuna-Security: ブロックバックエンド=%s", b.fw.Name())
	}
	return b
}

// restoreState reloads blocks persisted before a restart so the plugin does not
// lose track of firewall rules that still exist in the kernel. Expired entries
// are dropped; remaining blocks keep their original remaining duration.
func (b *blocker) restoreState() {
	if b.statePath == "" {
		return
	}
	data, err := os.ReadFile(b.statePath)
	if err != nil {
		return
	}
	var st blockState
	if json.Unmarshal(data, &st) != nil || st.Blocks == nil {
		return
	}
	now := time.Now()
	for ip, until := range st.Blocks {
		if !until.After(now) {
			continue
		}
		b.blocked[ip] = until
		if b.logger != nil {
			b.logger.Info("Kizuna-Security: ブロック状態を復元: %s (残り %ds)", ip, int(until.Sub(now).Seconds()))
		}
		if b.emitFn != nil {
			b.emitFn(i18nEvent(b.lang, "block", "warning", "block.restored.title", "block.restored.msg",
				module.SecurityEvent{IP: ip, Timestamp: now}, ip, int(until.Sub(now).Seconds())))
		}
	}
}

// saveState persists the active blocks (best effort; failures are logged only).
func (b *blocker) saveState() {
	if b.statePath == "" {
		return
	}
	b.mu.Lock()
	st := blockState{Blocks: make(map[string]time.Time, len(b.blocked))}
	for ip, until := range b.blocked {
		st.Blocks[ip] = until
	}
	b.mu.Unlock()

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(b.statePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	// fsutil: 一時ファイル→fsync→rename（L-12）。
	_ = fsutil.WriteFileAtomic(b.statePath, data, 0600)
}

func (b *blocker) isWhitelisted(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range b.whitelist {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (b *blocker) HandleBlockCandidate(ip string, count int) {
	if b == nil || b.mode == "off" {
		return
	}
	if b.isWhitelisted(ip) {
		if b.logger != nil {
			b.logger.Info("Kizuna-Security: %s はホワイトリスト対象のためブロックしません", ip)
		}
		return
	}

	if b.mode == "dry-run" {
		b.emitFn(i18nEvent(b.lang, "block", "warning", "block.dryrun.title", "block.dryrun.msg",
			module.SecurityEvent{IP: ip, Timestamp: time.Now()}, ip, count))
		return
	}

	b.mu.Lock()
	if _, already := b.blocked[ip]; already {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	if err := b.fw.ApplyBlock(ip); err != nil {
		if b.logger != nil {
			b.logger.Warn("Kizuna-Security: %s のブロックに失敗しました (%s): %v", ip, b.fw.Name(), err)
		}
		b.emitFn(i18nEvent(b.lang, "block", "warning", "block.fail.title", "block.fail.msg",
			module.SecurityEvent{IP: ip, Timestamp: time.Now()}, ip, b.fw.Name()))
		return
	}

	b.mu.Lock()
	if len(b.blocked) >= maxBlockedIPs {
		b.evictBlockedLocked(time.Now())
	}
	b.blocked[ip] = time.Now().Add(b.duration)
	b.mu.Unlock()
	b.saveState()

	b.emitFn(i18nEvent(b.lang, "block", "critical", "block.done.title", "block.done.msg",
		module.SecurityEvent{IP: ip, Timestamp: time.Now()}, ip, int(b.duration.Seconds()), b.fw.Name(), count))
}

func (b *blocker) UnblockExpired() {
	if b == nil || b.mode != "enforce" {
		return
	}
	now := time.Now()
	b.mu.Lock()
	var expired []string
	for ip, until := range b.blocked {
		if now.After(until) {
			expired = append(expired, ip)
		}
	}
	b.mu.Unlock()

	if len(expired) == 0 {
		return
	}
	sort.Strings(expired)
	changed := false
	for _, ip := range expired {
		if err := b.fw.RemoveBlock(ip); err != nil {
			if b.logger != nil {
				b.logger.Warn("Kizuna-Security: %s のブロック解除に失敗しました: %v", ip, err)
			}
			continue
		}
		b.mu.Lock()
		delete(b.blocked, ip)
		b.mu.Unlock()
		changed = true
		if b.logger != nil {
			b.logger.Info("Kizuna-Security: %s のブロックを解除しました", ip)
		}
	}
	if changed {
		b.saveState()
	}
}

func (b *blocker) evictBlockedLocked(now time.Time) {
	for ip, until := range b.blocked {
		if now.After(until) {
			delete(b.blocked, ip)
		}
	}
	if len(b.blocked) < maxBlockedIPs {
		return
	}
	var oldestIP string
	var oldest time.Time
	first := true
	for ip, until := range b.blocked {
		if first || until.Before(oldest) {
			oldest = until
			oldestIP = ip
			first = false
		}
	}
	if oldestIP != "" {
		delete(b.blocked, oldestIP)
	}
}

func isValidIPArg(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	return net.ParseIP(s) != nil
}
