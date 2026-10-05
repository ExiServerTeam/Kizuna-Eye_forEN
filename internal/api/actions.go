package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"Kizuna-Eye/pkg/alert"
)

// AlertActioner is implemented by providers that can locate an alert by ID
// and record the outcome of a one-click remediation (task 2).
// The dashboard's alert engine satisfies it.
type AlertActioner interface {
	// FindAlertByID returns the alert with the given ID, or nil.
	FindAlertByID(id string) *alert.HistoryEntry
	// RecordAction appends an audit entry to the alert history.
	RecordAction(a *alert.HistoryEntry, action, detail, result string)
}

// actionHelperPath is the sudo helper installed by install.sh. It validates
// the action name and arguments and is the only privileged command the
// dashboard may run for one-click remediation (sudoers grants it NOPASSWD).
var actionHelperPath = "/usr/local/bin/kizuna-action.sh"

// allowedActions is the closed set the UI may request. Keeping it a map (not
// a slice) makes the membership test O(1) and the intent obvious: any name
// not listed here is rejected before a process is ever spawned.
var allowedActions = map[string]bool{
	"block_ip":     true,
	"unblock_ip":   true,
	"delete_cron":  true,
	"kill_process": true,
	"kill_port":    true,
	"restore_file": true,
}

// pidRe / ipRe validate the secondary argument for actions that take one.
var (
	pidRe  = regexp.MustCompile(`^[0-9]{1,10}$`)
	ipRe   = regexp.MustCompile(`^[0-9a-fA-F:.]{3,45}$`)
	portRe = regexp.MustCompile(`^[0-9]{1,5}$`)
)

// ActionHandler serves POST /api/alerts/{id}/action.
type ActionHandler struct {
	provider AlertActioner
}

// NewActionHandler creates an ActionHandler.
func NewActionHandler(provider AlertActioner) *ActionHandler {
	return &ActionHandler{provider: provider}
}

// RegisterRoutes registers the action route.
func (h *ActionHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/alerts/{id}/action", h.handleAction)
}

type actionRequest struct {
	Action string `json:"action"`
	// Target is the optional action argument (IP, PID, cron base name, or
	// file path). For kill_process the dashboard fills it from the alert's
	// process hint when the client omits it.
	Target string `json:"target"`
	Backup string `json:"backup"`
}

// handleAction validates the request, runs the sudo helper, and records the
// result in the alert history. The helper is the actual privilege boundary;
// this handler only maps an alert to the helper arguments.
func (h *ActionHandler) handleAction(w http.ResponseWriter, r *http.Request) {
	if h.provider == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "action support is not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing alert id")
		return
	}
	var req actionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !allowedActions[req.Action] {
		writeJSONError(w, http.StatusBadRequest, "unsupported action")
		return
	}
	a := h.provider.FindAlertByID(id)
	if a == nil {
		writeJSONError(w, http.StatusNotFound, "alert not found")
		return
	}

	args, err := buildActionArgs(req.Action, req, a)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	target := ""
	if len(args) > 0 {
		target = args[len(args)-1]
	}
	cmd := exec.CommandContext(ctx, "sudo", append([]string{"-n", actionHelperPath, req.Action}, args...)...)
	out, runErr := cmd.CombinedOutput()
	resultText := strings.TrimSpace(string(out))

	result := "success"
	detail := ""
	if runErr != nil {
		result = "failed"
		// ヘルパーは機械可読な理由（"no process is listening on port N"
		// など）を返すので、UI にそのまま出すと意味が伝わらない。
		// 人間が次に何をすればよいか分かる文に変換する。
		detail = humanizeActionError(req.Action, target, resultText, runErr)
	}

	// 実行結果をアラート履歴に記録する（監査証跡）。
	h.provider.RecordAction(a, req.Action, target, result+" "+detail)

	status := http.StatusOK
	if runErr != nil {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]interface{}{
		"status": result,
		"action": req.Action,
		"target": target,
		"detail": detail,
	})
}

// humanizeActionError turns the helper's stderr (or a generic exec error)
// into a message an operator can act on. The raw text is kept as a suffix so
// nothing is lost for debugging, but the leading sentence explains the cause.
func humanizeActionError(action, target, helperOut string, runErr error) string {
	out := strings.TrimSpace(helperOut)
	switch {
	case strings.Contains(out, "no process is listening on port"):
		return fmt.Sprintf("ポート %s で待ち受けているプロセスは既に終了しています（対処の必要はありません）。", target)
	case strings.Contains(out, "no process could be signalled"):
		return fmt.Sprintf("ポート %s のプロセスを停止できませんでした（権限不足か、既に終了しています）。", target)
	case strings.Contains(out, "port out of range"), strings.Contains(out, "invalid port"):
		return "ポート番号が不正です。"
	case strings.Contains(out, "invalid pid"):
		return "PID が不正です。"
	case strings.Contains(out, "invalid ip"):
		return "IP アドレスが不正です。"
	case strings.Contains(out, "invalid cron name"):
		return "cron 名が不正です。"
	case strings.Contains(out, "backup missing"):
		return fmt.Sprintf("バックアップファイルが見つかりません: %s", target)
	case strings.Contains(out, "path not allowed"), strings.Contains(out, "backup path not allowed"):
		return "許可されていないパスです。"
	}
	if out != "" {
		return fmt.Sprintf("対処に失敗しました: %s", out)
	}
	return fmt.Sprintf("対処に失敗しました: %v", runErr)
}

// buildActionArgs maps an action + request to the helper's argument list,
// rejecting anything that does not match the strict per-action format.
func buildActionArgs(action string, req actionRequest, a *alert.HistoryEntry) ([]string, error) {
	switch action {
	case "block_ip", "unblock_ip":
		target := strings.TrimSpace(req.Target)
		if target == "" {
			target = extractIPFromMessage(a)
		}
		if !ipRe.MatchString(target) || strings.ContainsAny(target, " \t;") {
			return nil, errAction("invalid IP")
		}
		return []string{target}, nil
	case "delete_cron":
		target := strings.TrimSpace(req.Target)
		if target == "" {
			target = extractCronName(a)
		}
		if target == "" || strings.ContainsAny(target, "/\\ \t;") {
			return nil, errAction("invalid cron name")
		}
		return []string{target}, nil
	case "kill_process":
		target := strings.TrimSpace(req.Target)
		if !pidRe.MatchString(target) {
			return nil, errAction("invalid pid (target required)")
		}
		return []string{target}, nil
	case "kill_port":
		target := strings.TrimSpace(req.Target)
		if target == "" {
			target = extractPortFromAlert(a)
		}
		if !portRe.MatchString(target) {
			return nil, errAction("invalid port")
		}
		if n, err := strconv.Atoi(target); err != nil || n < 1 || n > 65535 {
			return nil, errAction("port out of range")
		}
		return []string{target}, nil
	case "restore_file":
		target := strings.TrimSpace(req.Target)
		if target == "" {
			target = a.Source
		}
		backup := strings.TrimSpace(req.Backup)
		if target == "" || backup == "" {
			return nil, errAction("restore_file requires target and backup")
		}
		return []string{target, backup}, nil
	}
	return nil, errAction("unsupported action")
}

func errAction(msg string) error { return &actionError{msg} }

type actionError struct{ msg string }

func (e *actionError) Error() string { return e.msg }

// ipInMessage extracts the first IPv4/IPv6-looking token from the alert
// message. The SSH alert message contains "接続元: <ip>" and the port alert
// contains an address:port, so this gives the UI a default target it can
// show for confirmation. The operator can still override it.
func extractIPFromMessage(a *alert.HistoryEntry) string {
	re := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b|\b[0-9a-fA-F:]{3,45}\b`)
	return re.FindString(a.Message)
}

// extractPortFromAlert pulls the TCP port out of a listening-port alert.
// The plugin records it in Command ("待ち受け: TCP 0.0.0.0:55555") and in
// the message ("TCP ポート 55555 が新たに..."), so either form works. The
// UI sends the port explicitly; this is a fallback for API callers.
func extractPortFromAlert(a *alert.HistoryEntry) string {
	re := regexp.MustCompile(`:(\d{1,5})\b`)
	if m := re.FindStringSubmatch(a.Command); m != nil {
		return m[1]
	}
	re2 := regexp.MustCompile(`ポート\s*(\d{1,5})`)
	if m := re2.FindStringSubmatch(a.Message); m != nil {
		return m[1]
	}
	return ""
}

// extractCronName returns the basename of the alert source when it is a
// crontab file (…/crontabs/user).
func extractCronName(a *alert.HistoryEntry) string {
	p := a.Source
	if p == "" {
		return ""
	}
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[i+1:]
}
