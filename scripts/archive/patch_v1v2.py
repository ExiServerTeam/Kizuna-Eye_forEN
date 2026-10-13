#!/usr/bin/env python3
"""V1-A + V2-B wiring (single-tab indentation)."""
import shutil
import sys

DIR = "/samba/share/Kizuna-Security/plugin"
CFG = DIR + "/config.go"
PLUGIN = DIR + "/plugin.go"
MSG = DIR + "/messages.go"
dry = "--dry-run" in sys.argv


def read(p):
    with open(p, encoding="utf-8") as f:
        return f.read()


def write(p, s):
    with open(p, "w", encoding="utf-8") as f:
        f.write(s)


def rep(src, old, new, label):
    n = src.count(old)
    if n != 1:
        raise SystemExit("ERROR: anchor %s count=%d" % (label, n))
    return src.replace(old, new, 1)


# ---- config.go ----
cfg = read(CFG)
cfg = rep(cfg,
          "\tSudoIgnoreCommands []string\n}",
          "\tSudoIgnoreCommands []string\n"
          "\n"
          "\t// V1-A: HMAC key for the security-log hash chain. Empty keeps the\n"
          "\t// legacy keyless chain.\n"
          "\tChainKeyPath string\n"
          "\t// V1-A/V2-B: how often (seconds) to verify the chain / alert history.\n"
          "\tIntegrityCheckInterval int\n"
          "\t// V2-B: alert_history.jsonl consistency (written by the dashboard).\n"
          "\tAlertHistoryPath      string\n"
          "\tAlertHistoryStatePath string\n}",
          "cfg-struct")
cfg = rep(cfg,
          "\t\tSudoIgnoreCommands: []string{\"/usr/sbin/smartctl\", \"smartctl\"},\n\t}",
          "\t\tSudoIgnoreCommands: []string{\"/usr/sbin/smartctl\", \"smartctl\"},\n"
          "\t\tChainKeyPath:            \"/samba/share/Kizuna-Eye/keys/chain.key\",\n"
          "\t\tIntegrityCheckInterval:  300,\n"
          "\t\tAlertHistoryPath:        \"/samba/share/Kizuna-Eye/logs/alert_history.jsonl\",\n"
          "\t\tAlertHistoryStatePath:   \"./logs/kizuna-security-alertstate.json\",\n\t}",
          "cfg-default")
cfg = rep(cfg,
          "\tif v, ok := raw[\"sudo_ignore_commands\"].([]interface{}); ok {\n\t\tc.SudoIgnoreCommands = toStringList(v)\n\t}\n",
          "\tif v, ok := raw[\"sudo_ignore_commands\"].([]interface{}); ok {\n\t\tc.SudoIgnoreCommands = toStringList(v)\n\t}\n"
          "\tif v, ok := raw[\"chain_key_path\"].(string); ok {\n\t\tc.ChainKeyPath = v\n\t}\n"
          "\tif v, ok := raw[\"integrity_check_interval_sec\"].(float64); ok {\n\t\tc.IntegrityCheckInterval = int(v)\n\t}\n"
          "\tif v, ok := raw[\"alert_history_path\"].(string); ok && strings.TrimSpace(v) != \"\" {\n\t\tc.AlertHistoryPath = v\n\t}\n"
          "\tif v, ok := raw[\"alert_history_state_path\"].(string); ok && strings.TrimSpace(v) != \"\" {\n\t\tc.AlertHistoryStatePath = v\n\t}\n",
          "cfg-parse")
cfg = rep(cfg,
          "\tif c.SUIDScanInterval > 86400 {\n\t\tc.SUIDScanInterval = 86400\n\t}\n",
          "\tif c.SUIDScanInterval > 86400 {\n\t\tc.SUIDScanInterval = 86400\n\t}\n"
          "\tif c.IntegrityCheckInterval < 30 {\n\t\tc.IntegrityCheckInterval = 300\n\t}\n"
          "\tif c.IntegrityCheckInterval > 86400 {\n\t\tc.IntegrityCheckInterval = 86400\n\t}\n",
          "cfg-validate")

# ---- plugin.go ----
pl = read(PLUGIN)
pl = rep(pl,
         "\tfl, err := NewFileLogger(cfg.LogPath)\n",
         "\tfl, err := NewFileLoggerKeyed(cfg.LogPath, cfg.ChainKeyPath)\n",
         "plugin-logger")
pl = rep(pl,
         "func (p *SecurityPlugin) Init(ctx context.Context) error {\n\tp.ctx, p.cancel = context.WithCancel(ctx)\n\tif p.logger != nil {\n\t\tp.logger.Info(\"Kizuna-Security プラグイン初期化\")\n\t}\n\treturn nil\n}",
         "func (p *SecurityPlugin) Init(ctx context.Context) error {\n"
         "\tp.ctx, p.cancel = context.WithCancel(ctx)\n"
         "\tgo p.integrityLoop()\n"
         "\tif p.logger != nil {\n"
         "\t\tp.logger.Info(\"Kizuna-Security プラグイン初期化\")\n"
         "\t}\n"
         "\treturn nil\n}",
         "plugin-init")
pl = pl.rstrip("\n") + "\n\n" + (
    "// integrityLoop periodically verifies the security-log hash chain and the\n"
    "// alert_history consistency. It reads the interval from the live config so a\n"
    "// config change takes effect without a restart.\n"
    "func (p *SecurityPlugin) integrityLoop() {\n"
    "\tfirst := time.NewTimer(30 * time.Second)\n"
    "\tdefer first.Stop()\n"
    "\tselect {\n"
    "\tcase <-p.ctx.Done():\n"
    "\t\treturn\n"
    "\tcase <-first.C:\n"
    "\t\tp.runIntegrityChecks()\n"
    "\t}\n"
    "\tfor {\n"
    "\t\tp.mu.RLock()\n"
    "\t\tinterval := 300\n"
    "\t\tif p.config != nil && p.config.IntegrityCheckInterval > 0 {\n"
    "\t\t\tinterval = p.config.IntegrityCheckInterval\n"
    "\t\t}\n"
    "\t\tp.mu.RUnlock()\n"
    "\t\tt := time.NewTimer(time.Duration(interval) * time.Second)\n"
    "\t\tselect {\n"
    "\t\tcase <-p.ctx.Done():\n"
    "\t\t\tt.Stop()\n"
    "\t\t\treturn\n"
    "\t\tcase <-t.C:\n"
    "\t\t\tp.runIntegrityChecks()\n"
    "\t\t}\n"
    "\t}\n"
    "}\n"
)

# ---- messages.go ----
msg = read(MSG)
lines = msg.split("\n")
out = []
ja_done = en_done = False
for line in lines:
    out.append(line)
    if '"cron.unreadable.msg"' in line:
        is_ja = any('\u3040' <= ch <= '\u30ff' or '\u4e00' <= ch <= '\u9fff' for ch in line)
        indent = line[:len(line) - len(line.lstrip())]
        if is_ja and not ja_done:
            out.append(indent + '"integrity.chain.title": "セキュリティログの改ざんを検知",')
            out.append(indent + '"integrity.chain.msg":   "セキュリティログ %s のハッシュチェーンが壊れています: %s",')
            out.append(indent + '"integrity.alerthist.title": "アラート履歴の改ざんを検知",')
            out.append(indent + '"integrity.alerthist.msg":   "アラート履歴 %s が不正です: %s",')
            ja_done = True
        elif (not is_ja) and not en_done:
            out.append(indent + '"integrity.chain.title": "Security log tampering detected",')
            out.append(indent + '"integrity.chain.msg":   "Hash chain of security log %s is broken: %s",')
            out.append(indent + '"integrity.alerthist.title": "Alert history tampering detected",')
            out.append(indent + '"integrity.alerthist.msg":   "Alert history %s is invalid: %s",')
            en_done = True
if not ja_done or not en_done:
    raise SystemExit("ERROR: messages anchor not found (ja=%s en=%s)" % (ja_done, en_done))
msg = "\n".join(out)

if dry:
    print("DRY-RUN OK: config.go + plugin.go + messages.go patches matched")
    sys.exit(0)

for p in (CFG, PLUGIN, MSG):
    shutil.copy2(p, p + ".prepatch-v1v2")
write(CFG, cfg)
write(PLUGIN, pl)
write(MSG, msg)
print("PATCHED: config.go, plugin.go, messages.go")
