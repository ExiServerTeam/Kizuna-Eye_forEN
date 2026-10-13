package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"Kizuna-Eye/pkg/fsutil"
	"Kizuna-Eye/pkg/module"
)

const (
	maxQueueSize     = 1000
	maxFailTrackers  = 10000
	maxLoginTrackers = 10000
	maxWatchFileSize = 512 * 1024 * 1024
	maxLinesPerScan  = 100000
)

const (
	// failureDedupWindow is the grace period in which repeated auth lines for
	// the same user@source (e.g. "Invalid user" followed by "Failed password")
	// are treated as one attempt.
	failureDedupWindow = 3 * time.Second
	// maxUsernamesInMessage caps how many usernames an enumeration alert lists.
	maxUsernamesInMessage = 5
)

var (
	reSSHLogin    = regexp.MustCompile(`Accepted (?:password|publickey|keyboard-interactive/\S+) for (\S+) from ([0-9a-fA-F:.]+)`)
	reSSHFailed   = regexp.MustCompile(`Failed (?:password|publickey) for (?:invalid user )?(\S+) from ([0-9a-fA-F:.]+)`)
	reInvalidUser = regexp.MustCompile(`Invalid user (\S+) from ([0-9a-fA-F:.]+)`)
	reAuthClosed  = regexp.MustCompile(`Connection closed by authenticating user (\S+) ([0-9a-fA-F:.]+)`)
	reSudo        = regexp.MustCompile(`sudo:\s+(\S+) : .*USER=(\S+) ; COMMAND=(.+)$`)
	reSudoFail    = regexp.MustCompile(`sudo:.*authentication failure;.*user=(\S+)`)
	reAccount     = regexp.MustCompile(`\b(useradd|usermod|userdel|groupadd|groupdel|passwd|chpasswd)\b`)
	reDpkgInstall = regexp.MustCompile(`^[0-9-]+ [0-9:]+ (install|upgrade|remove) (\S+)`)
	reAptInstall  = regexp.MustCompile(`^(Install|Upgrade|Remove): (.+)$`)
	reYumInstall  = regexp.MustCompile(`(Installed|Updated|Erased|Obsoleted):\s+(\S+)`)
)

type lineSource struct {
	path   string
	offset int64
	// dev / ino は読み取り対象の同一性。ログローテーションで別ファイルに
	// 差し替わったことを offset だけでは検出できないため保持する（F-5）。
	dev uint64
	ino uint64
}

type Monitor struct {
	cfg     *SecurityConfig
	fileLog *FileLogger
	logger  module.Logger

	onBlockCandidate func(ip string, count int)

	loginBaselinePath string

	mu            sync.Mutex
	sources       map[string]*lineSource
	queue         []module.SecurityEvent
	failCounts    map[string]*failTracker
	loginCounts   map[string]*loginTracker
	loginKnownIPs map[string]bool

	// spoof holds recent auth lines whose journald origin is not the real
	// sshd (e.g. logger -t sshd). A matching auth.log line is forged (V5).
	spoof       *spoofCache
	spoofCancel chan struct{}

	// ignoredSudo remembers sudo commands that were deliberately not alerted
	// (the plugin's own read-only helper). A sync.Map is used so the zero
	// value is usable: tests build Monitor literals without a constructor.
	ignoredSudo sync.Map
}

type failTracker struct {
	first            time.Time
	notifiedCritical bool
	lastKey          string
	lastSeen         time.Time

	// recent / sustained hold failure timestamps. The first implementation
	// compared only against tr.first ("within BurstWindow of the first
	// failure"), so a rapid burst that followed a stale probe was discarded
	// when the window reset, and even a steady ~1 attempt per 12s pace (5 per
	// 60s average) never escalated. Counting the last BurstWindow / the longer
	// sustained window fixes both (attack A-4 "SSH 失敗連続").
	recent    []time.Time
	sustained []time.Time
	// users remembers when each username was first seen from this source.
	// Rotating usernames keeps the per-attempt rate below FailedBurst, so the
	// number of distinct names is a separate signal (username enumeration).
	users map[string]time.Time
}

type loginTracker struct {
	first time.Time
	count int
	// lastNotifiedAt / lastNotifiedCount rate limit the "success burst" alert
	// per source (audit finding F-6): the operator's own SSH sessions (one per
	// tool call) reach the threshold every window and re-fired the same
	// critical, drowning the failure signal. A repeat is only sent after the
	// cool-down AND when the count at least doubles.
	lastNotifiedAt    time.Time
	lastNotifiedCount int
}

type loginState struct {
	KnownIPs []string `json:"known_ips"`
}

func NewMonitor(cfg *SecurityConfig, fileLog *FileLogger, logger module.Logger) *Monitor {
	m := &Monitor{
		cfg:               cfg,
		fileLog:           fileLog,
		logger:            logger,
		sources:           make(map[string]*lineSource),
		failCounts:        make(map[string]*failTracker),
		loginCounts:       make(map[string]*loginTracker),
		loginKnownIPs:     make(map[string]bool),
		loginBaselinePath: cfg.SSHLoginBaselinePath,
		spoof:             newSpoofCache(),
	}
	m.loadLoginState()
	return m
}

// tr localizes a notification message using the configured language.
func (m *Monitor) tr(key string, args ...interface{}) string {
	return msg(m.cfg.Language, key, args...)
}

// emitI18n emits a security event with BOTH language renderings stored, so
// the dashboard can show the log and alert history in the selected UI
// language without re-deriving the string from a key (task 9). Title/Message
// hold the configured language (for notifications); TitleEN/MessageEN hold
// English.
func (m *Monitor) emitI18n(category, level, titleKey, msgKey string, fields module.SecurityEvent, args ...interface{}) {
	titleJa := msg("ja", titleKey)
	msgJa := msg("ja", msgKey, args...)
	titleEn := msg("en", titleKey)
	msgEn := msg("en", msgKey, args...)

	localizedTitle, localizedMsg := titleJa, msgJa
	if m.cfg.Language == "en" {
		localizedTitle, localizedMsg = titleEn, msgEn
	}

	event := fields
	event.Category = category
	event.Level = level
	event.Title = localizedTitle
	event.Message = localizedMsg
	event.TitleEN = titleEn
	event.MessageEN = msgEn
	m.emit(event)
}

func (m *Monitor) Scan() {
	now := time.Now()
	for _, path := range m.cfg.WatchFiles {
		m.scanFile(path, now)
	}
}

// Stop terminates the journald spoof watch, if running.
func (m *Monitor) Stop() {
	if m.spoofCancel != nil {
		close(m.spoofCancel)
		m.spoofCancel = nil
	}
}

func (m *Monitor) scanFile(path string, now time.Time) {
	m.mu.Lock()
	src, ok := m.sources[path]
	if !ok {
		var size int64
		var dev, ino uint64
		if info, err := os.Stat(path); err == nil {
			size = info.Size()
			dev, ino = fileIdentity(info)
		}
		src = &lineSource{path: path, offset: size, dev: dev, ino: ino}
		m.sources[path] = src
	}
	startOffset := src.offset
	knownDev, knownIno := src.dev, src.ino
	m.mu.Unlock()

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if m.logger != nil {
			m.logger.Debug("Kizuna-Security: ログを開けません %s: %v", path, err)
		}
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return
	}
	if !info.Mode().IsRegular() {
		return
	}
	dev, ino := fileIdentity(info)
	rotated := knownIno != 0 && (dev != knownDev || ino != knownIno)
	if rotated || info.Size() < startOffset {
		// ログローテーション（別 inode への差し替え）または truncate。
		// 新しいファイルの先頭から読み直して取りこぼしを防ぐ（F-5）。
		startOffset = 0
	}
	readEnd := info.Size()
	if readEnd-startOffset > maxWatchFileSize {
		readEnd = startOffset + maxWatchFileSize
	}
	if _, err := f.Seek(startOffset, 0); err != nil {
		return
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	newOffset := startOffset
	lines := 0
	for {
		if newOffset >= readEnd {
			break
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}
		newOffset += int64(len(line))
		lines++
		text := strings.TrimRight(string(line), "\r\n")
		m.classify(path, text, now)
		if lines >= maxLinesPerScan {
			break
		}
	}

	m.mu.Lock()
	if s, ok := m.sources[path]; ok {
		s.offset = newOffset
		s.dev = dev
		s.ino = ino
	}
	m.mu.Unlock()
}

// fileIdentity はファイルのデバイス番号と inode を返す（取得できない場合は 0）。
// offset だけではローテーションを検出できない（新ファイルが旧 offset より
// 大きい場合に取りこぼす）ため、同一性で判定する（F-5）。
func fileIdentity(info os.FileInfo) (uint64, uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino)
}

func (m *Monitor) classify(path, line string, now time.Time) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}

	if strings.Contains(path, "auth.log") || strings.Contains(path, "secure") {
		// 偽装行（journald の _COMM が sshd 以外、または _UID が 0 以外）は
		// spoofed_log として記録し、ssh_login / ssh_failed の集計には
		// 流さない。logger -t sshd で偽の「Accepted password」を注入しても
		// ログイン成功のカウントには入らず、バースト判定を誤発火させない。
		// 偽装行はここで return するので、以降の reSudo / reSSHLogin /
		// reSSHFailed などのマッチはすべてスキップされる（二重記録なし）。
		if m.spoof != nil && m.spoof.contains(trimmed) {
			m.emitI18n("spoofed_log", "critical", "spoofed.title", "spoofed.msg",
				module.SecurityEvent{Source: path, Timestamp: now}, trimmed)
			return
		}
		if mm := reSudo.FindStringSubmatch(trimmed); mm != nil {
			actor := mm[1]
			target := mm[2]
			command := strings.TrimSpace(mm[3])
			if m.isIgnoredSudo(command) {
				return
			}
			m.emitI18n("sudo", "warning", "sudo.title", "sudo.msg",
				module.SecurityEvent{Source: path, Actor: actor, Timestamp: now}, actor, target, command)
			return
		}

		if mm := reSSHLogin.FindStringSubmatch(trimmed); mm != nil {
			user, ip := mm[1], mm[2]
			// 監視端末など、既知のホストからの反復ログインは「ログイン成功の
			// 急増」critical を誤発火させ、ログイン集計も汚す。allowlist に
			// 載っている IP の ssh_login は記録もカウントもしない（タスク7）。
			if m.isLoginAllowed(ip) {
				return
			}
			m.emitI18n("ssh_login", "info", "ssh_login.title", "ssh_login.msg",
				module.SecurityEvent{
					Source:     path,
					Actor:      user,
					IP:         ip,
					Timestamp:  now,
					Command:    fmt.Sprintf("ssh %s@<host> (from %s)", user, ip),
					DetectFile: "monitor.go",
					DetectLine: 289,
					RelatedLog: trimmed,
				}, user, ip)
			m.checkLoginAnomaly(user, ip, now)
			return
		}

		if mm := reSSHFailed.FindStringSubmatch(trimmed); mm != nil {
			m.emitSSHFailure(path, mm[1], mm[2], now)
			return
		}
		if mm := reInvalidUser.FindStringSubmatch(trimmed); mm != nil {
			m.emitSSHFailure(path, mm[1], mm[2], now)
			return
		}
		if mm := reAuthClosed.FindStringSubmatch(trimmed); mm != nil {
			m.emitSSHFailure(path, mm[1], mm[2], now)
			return
		}

		if mm := reSudoFail.FindStringSubmatch(trimmed); mm != nil {
			actor := strings.TrimSpace(mm[1])
			m.emitI18n("sudo", "warning", "sudo.fail.title", "sudo.fail.msg",
				module.SecurityEvent{Source: path, Actor: actor, Timestamp: now}, actor)
			return
		}

		if reAccount.MatchString(trimmed) {
			m.emit(module.SecurityEvent{
				Category:  "account",
				Level:     "warning",
				Title:     m.tr("account.title"),
				Message:   trimmed,
				Source:    path,
				Timestamp: now,
				TitleEN:   msg("en", "account.title"),
				MessageEN: trimmed,
			})
			return
		}
	}

	if strings.Contains(path, "dpkg.log") {
		if mm := reDpkgInstall.FindStringSubmatch(trimmed); mm != nil {
			action := mm[1]
			pkg := mm[2]
			titleKey, verbKey := "install.title", "install.verb.install"
			switch action {
			case "upgrade":
				titleKey, verbKey = "install.upgrade.title", "install.verb.upgrade"
			case "remove":
				titleKey, verbKey = "install.remove.title", "install.verb.remove"
			}
			m.emitInstall(path, now, titleKey, pkg, verbKey)
			return
		}
	}

	if strings.Contains(path, "apt/history.log") || strings.Contains(path, "apt/history") {
		if mm := reAptInstall.FindStringSubmatch(trimmed); mm != nil {
			action := mm[1]
			pkgs := strings.TrimSpace(mm[2])
			if len(pkgs) > 200 {
				pkgs = pkgs[:200] + "..."
			}
			m.emitI18n("install", "warning", "apt.title", "apt.msg",
				module.SecurityEvent{Source: path, Timestamp: now, MessageEN: action + ": " + pkgs}, action, pkgs)
			return
		}
	}

	if strings.Contains(path, "yum.log") || strings.Contains(path, "dnf.log") || strings.Contains(path, "dnf.rpm.log") {
		if mm := reYumInstall.FindStringSubmatch(trimmed); mm != nil {
			action := mm[1]
			pkg := mm[2]
			titleKey, verbKey := "install.title", "install.verb.install"
			switch action {
			case "Updated":
				titleKey, verbKey = "install.upgrade.title", "install.verb.upgrade"
			case "Erased", "Obsoleted":
				titleKey, verbKey = "install.remove.title", "install.verb.remove"
			}
			m.emitInstall(path, now, titleKey, pkg, verbKey)
			return
		}
	}
}

// emitInstall emits a package install/upgrade/remove event with both
// languages (task 9). verbKey is the message verb (install/upgrade/remove).
func (m *Monitor) emitInstall(path string, now time.Time, titleKey, pkg, verbKey string) {
	m.emitI18n("install", "warning", titleKey, "install.msg",
		module.SecurityEvent{Source: path, Timestamp: now}, pkg, msg(m.cfg.Language, verbKey))
}

func (m *Monitor) checkLoginAnomaly(user, ip string, now time.Time) {
	m.mu.Lock()
	isNew := !m.loginKnownIPs[ip]
	if isNew {
		if len(m.loginKnownIPs) >= maxLoginTrackers {
			m.evictOldestLoginTrackerLocked(now)
		}
		m.loginKnownIPs[ip] = true
	}

	window := time.Duration(m.cfg.SSHLoginWindow) * time.Second
	burst := m.cfg.SSHLoginBurst
	if burst < 1 {
		burst = 10
	}
	tr, ok := m.loginCounts[ip]
	if !ok {
		tr = &loginTracker{first: now}
		if len(m.loginCounts) >= maxLoginTrackers {
			m.evictOldestLoginTrackerLocked(now)
		}
		m.loginCounts[ip] = tr
	} else if window > 0 && now.Sub(tr.first) > window {
		// 窓をまたいだら回数だけリセットする。通知履歴（lastNotified*）は
		// 冷却・倍増判定のために残す。
		tr.first = now
		tr.count = 0
	}
	tr.count++
	count := tr.count

	notifyBurst := false
	if count >= burst && count > tr.lastNotifiedCount {
		cooled := tr.lastNotifiedAt.IsZero() || window <= 0 || now.Sub(tr.lastNotifiedAt) >= window
		doubled := tr.lastNotifiedCount == 0 || count >= 2*tr.lastNotifiedCount
		if cooled && doubled {
			notifyBurst = true
			tr.lastNotifiedAt = now
			tr.lastNotifiedCount = count
		}
	}
	m.mu.Unlock()

	if isNew {
		m.emitI18n("ssh_login", "warning", "ssh_login.unknown.title", "ssh_login.unknown.msg",
			module.SecurityEvent{Actor: user, IP: ip, Timestamp: now}, user, ip)
	}
	if notifyBurst {
		titleJa := msg("ja", "ssh_login.burst.title")
		bodyJa := msg("ja", "ssh_login.burst.msg", ip, count)
		titleEn := msg("en", "ssh_login.burst.title")
		bodyEn := msg("en", "ssh_login.burst.msg", ip, count)
		if !isNew {
			// 既知 IP（運用端末・監視ツール）からの反復接続でも同じ急増が起きる
			// ことを伝え、本物の失敗と取り違えられないようにする。
			bodyJa += msg("ja", "ssh_login.burst.known.note")
			bodyEn += msg("en", "ssh_login.burst.known.note")
		}
		localTitle, localBody := titleJa, bodyJa
		if m.cfg.Language == "en" {
			localTitle, localBody = titleEn, bodyEn
		}
		m.emit(module.SecurityEvent{
			Category:  "ssh_login",
			Level:     "critical",
			Title:     localTitle,
			Message:   localBody,
			TitleEN:   titleEn,
			MessageEN: bodyEn,
			Actor:     user,
			IP:        ip,
			Timestamp: now,
		})
	}

	m.saveLoginState()
}

func (m *Monitor) evictOldestLoginTrackerLocked(now time.Time) {
	window := time.Duration(m.cfg.SSHLoginWindow) * time.Second
	for ip, tr := range m.loginCounts {
		if now.Sub(tr.first) > window {
			delete(m.loginCounts, ip)
		}
	}
	if len(m.loginCounts) < maxLoginTrackers {
		return
	}
	var oldestIP string
	var oldest time.Time
	first := true
	for ip, tr := range m.loginCounts {
		if first || tr.first.Before(oldest) {
			oldest = tr.first
			oldestIP = ip
			first = false
		}
	}
	if oldestIP != "" {
		delete(m.loginCounts, oldestIP)
	}
}

func (m *Monitor) loadLoginState() {
	if m.loginBaselinePath == "" {
		return
	}
	data, err := os.ReadFile(m.loginBaselinePath)
	if err != nil {
		return
	}
	var st loginState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	for _, ip := range st.KnownIPs {
		m.loginKnownIPs[ip] = true
	}
}

func (m *Monitor) saveLoginState() {
	if m.loginBaselinePath == "" {
		return
	}
	m.mu.Lock()
	ips := make([]string, 0, len(m.loginKnownIPs))
	for ip := range m.loginKnownIPs {
		ips = append(ips, ip)
	}
	m.mu.Unlock()
	sort.Strings(ips)

	data, err := json.MarshalIndent(loginState{KnownIPs: ips}, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(m.loginBaselinePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0700)
	}
	// fsutil: 一時ファイル→fsync→rename（L-12）。
	_ = fsutil.WriteFileAtomic(m.loginBaselinePath, data, 0600)
}

// emitSSHFailure records one failed login attempt and escalates to critical when
// the source crosses one of three thresholds:
//
//  1. burst      : FailedBurst attempts within BurstWindow (sliding),
//  2. sustained  : SSHFailedSustainedBurst attempts within the longer
//     SSHFailedSustainedWindow (low-and-slow brute force),
//  3. enumeration: SSHEnumDistinctUsers distinct usernames within SSHEnumWindow
//     (a rotating username list keeps the per-attempt rate low).
//
// Before 2026-10-04 the count was kept against the first attempt only, so a
// pacing that put the 5th attempt after BurstWindow (e.g. attack A-4's ~20-80s
// intervals) never escalated. At most one escalation is sent per source until
// it has been quiet for the sustained window, so a long campaign cannot spam.
func (m *Monitor) emitSSHFailure(path, user, ip string, now time.Time) {
	key := user + "@" + ip

	burst := m.cfg.FailedBurst
	if burst < 1 {
		burst = 5
	}
	burstWindow := time.Duration(m.cfg.BurstWindow) * time.Second
	if burstWindow <= 0 {
		burstWindow = 60 * time.Second
	}
	sustainedLimit := m.cfg.SSHFailedSustainedBurst
	if sustainedLimit < 1 {
		sustainedLimit = 15
	}
	sustainedWindow := time.Duration(m.cfg.SSHFailedSustainedWindow) * time.Second
	if sustainedWindow < burstWindow {
		sustainedWindow = burstWindow
	}
	enumLimit := m.cfg.SSHEnumDistinctUsers
	if enumLimit < 2 {
		enumLimit = 5
	}
	enumWindow := time.Duration(m.cfg.SSHEnumWindow) * time.Second
	if enumWindow <= 0 {
		enumWindow = 300 * time.Second
	}

	m.mu.Lock()
	tr, ok := m.failCounts[ip]
	if !ok {
		tr = &failTracker{first: now, users: make(map[string]time.Time)}
		if len(m.failCounts) >= maxFailTrackers {
			m.evictOldestFailTrackerLocked(now)
		}
		m.failCounts[ip] = tr
	}

	// 1回の試行が複数行（"Invalid user" と "Failed password" など）を残しても
	// 二重に数えない。
	if tr.lastKey == key && now.Sub(tr.lastSeen) <= failureDedupWindow {
		m.mu.Unlock()
		return
	}
	// 静穏期間の後の失敗は新しい攻撃とみなし、エスカレーションを再武装する。
	if !tr.lastSeen.IsZero() && now.Sub(tr.lastSeen) > sustainedWindow {
		tr.notifiedCritical = false
	}
	tr.lastKey = key
	tr.lastSeen = now

	tr.recent = appendFailureTime(tr.recent, now, burstWindow, burst)
	tr.sustained = appendFailureTime(tr.sustained, now, sustainedWindow, sustainedLimit)
	if _, seen := tr.users[user]; !seen {
		if len(tr.users) >= enumLimit {
			evictOldestUser(tr.users)
		}
		tr.users[user] = now
	}
	pruneUsers(tr.users, now, enumWindow)

	shortCount := len(tr.recent)
	sustainedCount := len(tr.sustained)
	distinct := len(tr.users)

	notifyCritical, rule, count := false, "", 0
	switch {
	case shortCount >= burst && !tr.notifiedCritical:
		notifyCritical, rule, count = true, "burst", shortCount
	case !tr.notifiedCritical && sustainedCount >= sustainedLimit:
		notifyCritical, rule, count = true, "sustained", sustainedCount
	case !tr.notifiedCritical && distinct >= enumLimit:
		notifyCritical, rule, count = true, "enum", distinct
	}
	if notifyCritical {
		tr.notifiedCritical = true
	}
	names := sortedUserNames(tr.users, maxUsernamesInMessage)
	loopback := isLoopbackIP(ip)
	m.mu.Unlock()

	if notifyCritical && m.onBlockCandidate != nil {
		m.onBlockCandidate(ip, count)
	}

	level := "warning"
	titleKey := "ssh_failed.title"
	msgKey := "ssh_failed.msg"
	var msgArgs []interface{}
	if notifyCritical {
		level = "critical"
		switch rule {
		case "sustained":
			titleKey = "ssh_failed.sustained.title"
			msgKey = "ssh_failed.sustained.msg"
			msgArgs = []interface{}{ip, int(sustainedWindow.Seconds()), count}
		case "enum":
			titleKey = "ssh_failed.enum.title"
			msgKey = "ssh_failed.enum.msg"
			msgArgs = []interface{}{ip, count, strings.Join(names, ", ")}
		default:
			titleKey = "ssh_failed.burst.title"
			msgKey = "ssh_failed.burst.msg"
			msgArgs = []interface{}{ip, count}
		}
	} else {
		msgArgs = []interface{}{safeLogValue(user, 64)}
	}

	titleJa := msg("ja", titleKey)
	bodyJa := msg("ja", msgKey, msgArgs...)
	titleEn := msg("en", titleKey)
	bodyEn := msg("en", msgKey, msgArgs...)
	if loopback {
		// ループバック経由の失敗は「ホスト上の誰か」または「127.0.0.1 への
		// トンネル」であり、接続元 IP では攻撃元を特定できない。
		bodyJa += msg("ja", "ssh_failed.loopback.note")
		bodyEn += msg("en", "ssh_failed.loopback.note")
	}

	// タスク1: 詳細表示用。検知ロジックの行番号はルール別に変える。
	detectLine := 660 // 単発失敗
	remediation := fmt.Sprintf("ユーザー %s へのログイン失敗が %s から発生しました。心当たりが無ければ fail2ban/自動ブロックの有効化、SSH の鍵認証限定化、パスワード認証の無効化を検討してください。", user, ip)
	switch rule {
	case "burst":
		detectLine = 631
		remediation = fmt.Sprintf("IP %s から短時間に %d 回のログイン失敗を検知しました（総当たり攻撃の可能性）。このIPをブロックし、SSH のパスワード認証を無効化してください。", ip, count)
	case "sustained":
		detectLine = 633
		remediation = fmt.Sprintf("IP %s から %d 秒間に %d 回のログイン失敗を検知しました（low-and-slow 攻撃）。このIPをブロックしてください。", ip, int(sustainedWindow.Seconds()), count)
	case "enum":
		detectLine = 635
		remediation = fmt.Sprintf("IP %s から %d 種類のユーザー名でログイン失敗を検知しました（ユーザー列挙の可能性）。このIPをブロックし、存在しないユーザー名への応答を遅延/非公開にしてください。", ip, count)
	}

	localTitle, localBody := titleJa, bodyJa
	if m.cfg.Language == "en" {
		localTitle, localBody = titleEn, bodyEn
	}
	m.emit(module.SecurityEvent{
		Category:    "ssh_failed",
		Level:       level,
		Title:       localTitle,
		Message:     localBody,
		TitleEN:     titleEn,
		MessageEN:   bodyEn,
		Source:      path,
		Actor:       user,
		IP:          ip,
		Timestamp:   now,
		Command:     fmt.Sprintf("ssh %s@%s", user, ip),
		DetectFile:  "monitor.go",
		DetectLine:  detectLine,
		Remediation: remediation,
		RelatedLog:  fmt.Sprintf("%s: ユーザー %s のログイン失敗 (ip=%s)", path, user, ip),
	})
}

func (m *Monitor) evictOldestFailTrackerLocked(now time.Time) {
	window := time.Duration(m.cfg.BurstWindow) * time.Second
	for ip, tr := range m.failCounts {
		if now.Sub(tr.lastSeen) > window && now.Sub(tr.first) > window {
			delete(m.failCounts, ip)
		}
	}
	if len(m.failCounts) < maxFailTrackers {
		return
	}
	var oldestIP string
	var oldest time.Time
	first := true
	for ip, tr := range m.failCounts {
		if first || tr.lastSeen.Before(oldest) {
			oldest = tr.lastSeen
			oldestIP = ip
			first = false
		}
	}
	if oldestIP != "" {
		delete(m.failCounts, oldestIP)
	}
}

// appendFailureTime appends now to ts, drops entries outside window and keeps at
// most keep entries. keep can be the threshold itself: once the threshold is
// reached the decision is made, so counting further adds nothing. That bounds
// the memory per source even if maxFailTrackers sources are hostile.
func appendFailureTime(ts []time.Time, now time.Time, window time.Duration, keep int) []time.Time {
	if keep < 1 {
		keep = 1
	}
	cut := now.Add(-window)
	drop := 0
	for drop < len(ts) && !ts[drop].After(cut) {
		drop++
	}
	if drop > 0 {
		ts = append(ts[:0], ts[drop:]...)
	}
	ts = append(ts, now)
	if len(ts) > keep {
		ts = append(ts[:0], ts[len(ts)-keep:]...)
	}
	return ts
}

// pruneUsers drops usernames whose first sighting fell outside window.
func pruneUsers(users map[string]time.Time, now time.Time, window time.Duration) {
	cut := now.Add(-window)
	for name, at := range users {
		if !at.After(cut) {
			delete(users, name)
		}
	}
}

// evictOldestUser drops the least recently seen username (memory bound).
func evictOldestUser(users map[string]time.Time) {
	var oldestName string
	var oldest time.Time
	first := true
	for name, at := range users {
		if first || at.Before(oldest) {
			oldest, oldestName, first = at, name, false
		}
	}
	if oldestName != "" {
		delete(users, oldestName)
	}
}

// sortedUserNames returns up to max usernames for a notification, sorted so the
// message is stable between runs.
func sortedUserNames(users map[string]time.Time, max int) []string {
	names := make([]string, 0, len(users))
	for name := range users {
		names = append(names, safeLogValue(name, 64))
	}
	sort.Strings(names)
	if len(names) > max {
		names = names[:max]
	}
	return names
}

// safeLogValue strips control characters and truncates a value taken from the
// log before it is put into a notification. Usernames are attacker-chosen, so
// this keeps a crafted name from forging or bloating the alert.
func safeLogValue(s string, max int) string {
	clean := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		clean = append(clean, r)
	}
	if max > 0 && len(clean) > max {
		clean = append(clean[:max], '…')
	}
	return string(clean)
}

// isLoopbackIP reports whether ip is a loopback address. Failures from
// 127.0.0.1 come from the host itself (or a tunnel to it), so the recorded
// source IP cannot identify the attacker.
func isLoopbackIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

func levelRank(level string) int {
	switch strings.ToLower(level) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func (m *Monitor) emit(ev module.SecurityEvent) {
	ev.Plugin = pluginName
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	if m.fileLog != nil {
		extra := map[string]interface{}{
			"actor":   ev.Actor,
			"ip":      ev.IP,
			"source":  ev.Source,
			"message": ev.Message,
		}
		// Extra carries plugin-specific fields (dedup_key, count ...) so a
		// suppressed/deduplicated event stays aggregatable with jq.
		for k, v := range ev.Extra {
			extra[k] = v
		}
		// Both language renderings are persisted so the UI can switch the
		// log view between ja and en (task 9). Older lines lack these keys.
		if ev.TitleEN != "" {
			extra["title_en"] = ev.TitleEN
		}
		if ev.MessageEN != "" {
			extra["message_en"] = ev.MessageEN
		}
		m.fileLog.log(strings.ToUpper(ev.Level), ev.Category, ev.Title, extra)
	}

	if levelRank(ev.Level) < levelRank(m.cfg.NotifyMinimal) {
		return
	}

	m.mu.Lock()
	if len(m.queue) >= maxQueueSize {
		m.queue = m.queue[len(m.queue)-maxQueueSize+1:]
	}
	m.queue = append(m.queue, ev)
	m.mu.Unlock()
}

func (m *Monitor) Drain() []module.SecurityEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return nil
	}
	out := m.queue
	m.queue = nil
	return out
}

func (m *Monitor) Requeue(events []module.SecurityEvent) {
	if len(events) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	merged := make([]module.SecurityEvent, 0, len(events)+len(m.queue))
	merged = append(merged, events...)
	merged = append(merged, m.queue...)
	if len(merged) > maxQueueSize {
		merged = merged[len(merged)-maxQueueSize:]
	}
	m.queue = merged
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// isLoginAllowed reports whether ip is in ssh_login_allowlist, meaning its
// ssh_login events are not recorded or counted (task 7). A monitoring host or
// the operator's workstation reconnects often and would otherwise fire the
// "login burst" critical and pollute the login counters.
func (m *Monitor) isLoginAllowed(ip string) bool {
	if ip == "" || len(m.cfg.SSHLoginAllowlist) == 0 {
		return false
	}
	for _, a := range m.cfg.SSHLoginAllowlist {
		a = strings.TrimSpace(a)
		if a != "" && a == ip {
			return true
		}
	}
	return false
}

func (m *Monitor) isIgnoredSudo(command string) bool {
	cmd := strings.TrimSpace(command)
	// 自作の読み取り専用ヘルパーは「監視のために root 権限で読む」正常動作で、
	// ポーリングのたびに auth.log へ sudo 行が記録される。これを通知すると
	// ログと通知が埋まり本物の sudo が見えなくなるため、設定
	// （sudo_ignore_commands）に関わらず常に対象外にする。除外した事実は
	// 初回だけ Info に残して黙って落とさない。
	if cmd == cronSudoHelper || strings.HasPrefix(cmd, cronSudoHelper+" ") {
		if _, seen := m.ignoredSudo.LoadOrStore(cmd, true); !seen && m.logger != nil {
			m.logger.Info("Kizuna-Security: 自作ヘルパーの sudo 実行は通知対象外です: %s", cmd)
		}
		return true
	}
	for _, p := range m.cfg.SudoIgnoreCommands {
		p = strings.TrimSpace(p)
		if p != "" && strings.HasPrefix(cmd, p) {
			return true
		}
	}
	return false
}

var _ = fmt.Sprintf
