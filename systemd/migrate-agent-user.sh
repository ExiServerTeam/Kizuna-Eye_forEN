#!/bin/bash
# ============================================================
# A-4/H-1: agent を専用ユーザー kizuna-eye へ移行する（要 sudo・対話実行）
#
# 目的:
#   agent を運用アカウント `user`(uid=1000) から切り離す。uid=1000 が
#   奪われても FIM ベースライン / チェーン鍵 / アラート状態を改変できない。
#   併せて AmbientCapabilities=CAP_DAC_READ_SEARCH で /etc/shadow 等を
#   読めるようにし、A-2 の「権限不足 INFO」も解消する。
#
# 設計:
#   1. 状態ファイル (FIM/ports/SUID/cron/logins/blocks/alertstate) と chain.key は
#      /var/lib/kizuna-eye/{state,keys} (0700, kizuna-eye 所有) へ移設し、
#      modules.json の各パスを絶対パスへ書き換える。agent からは常に書き込め、
#      運用ユーザーからは読み書きできない。
#   2. 設定 (agent_config.json / modules.json) は $DATA のまま *共有* する。
#      ダッシュボードが保存のたびに書き換える唯一のファイルで、UI の設定変更
#      (プラグイン有効化・チェック間隔・トークン) を agent に届ける経路だから。
#      共有グループ kizuna-eye を作り 0640 (group read) を与える。agent 側の
#      再 chmod 0600 は fsutil.TightenSharedConfigMode が group read を保つ。
#   3. 検知ログ (kizuna-security.log) とその行数アンカー (logstate) は既定で
#      /var/lib/kizuna-eye/logs (0750, kizuna-eye:kizuna-eye) へ移設する。
#      アンカーはプラグインが log_path から導出するため、ログと必ず同じ場所に
#      置く必要がある。UI のログ一覧 (logDir=repo/logs) に残したい場合は
#      A4_SECURITY_LOG_DIR=$DIR/logs を付けて実行する（その場合アンカーを
#      運用ユーザーが削除でき、原理的に隠蔽可能になる）。
#   4. repo の logs/ は agent とダッシュボードの共有ログ置き場として setgid +
#      sticky + group write (3770) にし、agent が書くログは所有者を移す。
#
# 使い方:
#   sudo systemd/migrate-agent-user.sh [旧ユーザー]
#   （旧ユーザー省略時は sudo 実行者。状態ファイルの移行元）
#   ログ方針を変えて再適用（冪等）:
#   sudo A4_SECURITY_LOG_DIR=/path/to/Kizuna-Eye/logs systemd/migrate-agent-user.sh user
#
# 前提: systemd/kizuna-eye-agent.service が本リポジトリにあること。
#       設定変更の共有のため、実行前に tmp/_a4_prep.sh を phase1 → phase2 →
#       phase3 → phase4 の順に済ませておく（phase4 で require_signature を
#       立てた後は再起動せず、そのまま本スクリプトまで進める）。
#
# ロールバック:
#   sudo systemctl disable --now kizuna-eye-agent
#   sudo cp -a <backup>/... (下で表示する tar) を展開して chown user:user
#   従来どおり ./start.sh で起動
# ============================================================
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "❌ root で実行してください: sudo $0 [旧ユーザー]"
    exit 1
fi

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OLD_USER="${1:-${SUDO_USER:-}}"
if [ -z "$OLD_USER" ] || [ "$OLD_USER" = "root" ]; then
    echo "❌ 旧ユーザーを指定してください: sudo $0 <user>"
    exit 1
fi
OLD_HOME="$(getent passwd "$OLD_USER" | cut -d: -f6)"
SRC_DATA="$OLD_HOME/.kizuna-eye/data"
BIN_DIR="/opt/kizuna-eye/bin"

NEW_USER="kizuna-eye"
NEW_HOME="/var/lib/kizuna-eye"
SHARE_GROUP="kizuna-eye"
STATE_DIR="$NEW_HOME/state"
KEY_DIR="$NEW_HOME/keys"
REPO_LOGS="$DIR/logs"
# 検知ログ (kizuna-security.log) の置き場所。既定は保護ディレクトリ。
# UI のログ一覧に残したい場合のみ repo の logs を指定する。
A4_SECURITY_LOG_DIR="${A4_SECURITY_LOG_DIR:-$NEW_HOME/logs}"
TS="$(date +%Y%m%d_%H%M%S)"
BACKUP="/tmp/kizuna-a4-backup-$TS.tar.gz"

# 表示言語。install.sh が UI_LANG を export する（未設定なら日本語）。
UI_LANG="${UI_LANG:-ja}"
# 日本語の原文をキーに英語へ訳す。訳が無い行はそのまま（安全側）。
msg() {
    local m="$1"
    [ "$UI_LANG" = "en" ] || { printf '%s' "$m"; return 0; }
    case "$m" in
        "▶ 0. バックアップ") m="▶ 0. Backup" ;;
        "   ロールバックはこの tar を展開し、元の所有者へ chown して start.sh で起動します。") m="   Roll back by extracting this tar, chowning back to the original owner, and starting with start.sh." ;;
        "▶ 1. ユーザー作成 (存在すればスキップ)") m="▶ 1. Create user (skip if it exists)" ;;
        "▶ 2. ディレクトリ作成") m="▶ 2. Create directories" ;;
        "▶ 3. 状態ファイル・チェーン鍵を保護ディレクトリへ移行") m="▶ 3. Move state files and the chain key to the protected directory" ;;
        "▶ 3.2 modules.json の状態パスを絶対パスへ書き換え") m="▶ 3.2 Rewrite modules.json state paths to absolute" ;;
        "▶ 3.5 プラグインを agent ユーザーから読めるようにする") m="▶ 3.5 Make plugins readable by the agent user" ;;
        "▶ 3.6 共有グループと共有設定の権限") m="▶ 3.6 Shared group and config permissions" ;;
        "▶ 3.7 ログの権限（共有ログ + 検知ログの移設）") m="▶ 3.7 Log permissions (shared logs + detection log relocation)" ;;
        "▶ 4. unit 導入 (kizuna-eye-agent.service = A-4 版)") m="▶ 4. Install unit (kizuna-eye-agent.service)" ;;
        "▶ 5. 旧 agent を停止") m="▶ 5. Stop the old agent" ;;
        "▶ 6. 起動") m="▶ 6. Start" ;;
        "✅ 移行完了。確認してください:") m="✅ Migration complete. Please verify:" ;;
        "   - *ダッシュボードを再起動*（kizuna-eye グループ所属を反映。UI のログ閲覧と") m="   - *Restart the dashboard* (to pick up the kizuna-eye group; needed for UI log viewing" ;;
        "     共有設定の読み書きに必要）。agent は systemd 管理なので start.sh / stop.sh は") m="     and reading/writing shared config). The agent is systemd-managed, so start.sh / stop.sh" ;;
        "     agent を触らない（二重起動防止）。dashboard だけを指定する:") m="     do not touch it (prevents double start). Restart only the dashboard:" ;;
        "   - ダッシュボード GET /api/status とアラート/Discord 通知") m="   - Dashboard GET /api/status and alert/Discord notifications" ;;
        "   - FIM の権限 INFO が消えること（CAP_DAC_READ_SEARCH の効果）") m="   - The FIM permission INFO is gone (effect of CAP_DAC_READ_SEARCH)" ;;
        "   - backup プラグインの書き込み先に agent が書けること（ReadWritePaths を確認）") m="   - The backup plugin can write to its target directory (check ReadWritePaths)" ;;
        "   - 検知ログ（group read なので sudo 不要）: ls -l "*) m="   - Detection log (group read, no sudo needed): ls -l ${m#   - 検知ログ（group read なので sudo 不要）: ls -l }" ;;
        "   - 状態ファイル（agent 専用）: sudo ls -l "*) m="   - State files (agent-only): sudo ls -l ${m#   - 状態ファイル（agent 専用）: sudo ls -l }" ;;
        "   - 共有設定が 0640 kizuna-eye であること: ls -l "*) m="   - Shared config is 0640 kizuna-eye: ls -l ${m#   - 共有設定が 0640 kizuna-eye であること: ls -l }" ;;
        "   ログ方針を UI のログ一覧優先に変える場合:") m="   To prefer UI log listing over protected logs:" ;;
        "   ロールバック用バックアップ: "*) m="   Rollback backup: ${m#   ロールバック用バックアップ: }" ;;
        # --- 個別行（動的値を含む） ---
        "   既存: "*) m="   existing: ${m#   既存: }" ;;
        "   作成: "*) m="   created: ${m#   作成: }" ;;
        "   グループ作成: "*) m="   group created: ${m#   グループ作成: }" ;;
        *": 既に "*" に所属") m="${m%%: 既に *} already in ${m#*: 既に }"; m="${m% に所属}" ;;
        *" を "*" に追加（プロセスへの反映には再起動が必要）") m="${m%% を *} added to ${m#* を }"; m="${m% に追加（プロセスへの反映には再起動が必要）} (restart needed to take effect)" ;;
        *" が無いためスキップ") m="${m% が無いためスキップ} not present; skipping" ;;
        "   ℹ️ "*" なし（plugins.require_signature=false なら不要）") m="   ℹ️ ${m#   ℹ️ }"; m="${m% なし（plugins.require_signature=false なら不要）} not present (not needed if require_signature=false)" ;;
        "   ℹ️ 移設先に既存: "*"（スキップ）") m="   ℹ️ already exists at destination: ${m#   ℹ️ 移設先に既存: }"; m="${m%（スキップ）} (skipped)" ;;
        "   移設: "*) m="   moved: ${m#   移設: }" ;;
        "   ℹ️ 検知ログは repo logs に残す（UI のログ一覧に表示される）") m="   ℹ️ keeping the detection log in repo logs (shown in the UI log list)" ;;
        "   ⚠️ agent_linux が残っています (PID: "*) m="   ⚠️ agent_linux still running (PID: ${m#   ⚠️ agent_linux が残っています (PID: }"; m="${m/ → root 権限で停止します/ → stopping with root privileges}" ;;
        "❌ agent_linux を停止できませんでした (PID: "*) m="❌ Could not stop agent_linux (PID: ${m#❌ agent_linux を停止できませんでした (PID: }"; m="${m/。二重起動を避けるため中止します。/. Aborting to avoid a double start.}" ;;
        "   手動で停止してから再実行してください: "*) m="   Stop it manually and re-run: ${m#   手動で停止してから再実行してください: }" ;;
        "   ✅ 旧 agent は残っていません") m="   ✅ No leftover agent" ;;
        "❌ "*" が見つかりません") m="❌ not found: ${m#❌ }"; m="${m% が見つかりません}" ;;
        "   既存を退避: "*) m="   backed up existing: ${m#   既存を退避: }" ;;
        "❌ unit に未置換のプレースホルダが残っています:") m="❌ Unreplaced placeholders remain in the unit:" ;;
        "❌ root で実行してください: "*) m="❌ Run as root: ${m#❌ root で実行してください: }" ;;
        "❌ 旧ユーザーを指定してください: "*) m="❌ Specify the old user: ${m#❌ 旧ユーザーを指定してください: }" ;;
    esac
    # 括弧内の注記を英語化
    m="${m//（modules.json は登録後に作成される）/(modules.json is created after registration)}"
    printf '%s' "$m"
}

echo "$(msg "▶ 0. バックアップ")"
tar czf "$BACKUP" -C / \
    "${SRC_DATA#/}" \
    "${BIN_DIR#/}"/*.meta.json 2>/dev/null || true
# 状態ファイルは repo logs にもあるため併せて退避
tar rzf "$BACKUP" -C "$DIR" logs 2>/dev/null || true
echo "   $BACKUP"
echo "$(msg "   ロールバックはこの tar を展開し、元の所有者へ chown して start.sh で起動します。")"

echo "$(msg "▶ 1. ユーザー作成 (存在すればスキップ)")"
if id "$NEW_USER" >/dev/null 2>&1; then
    echo "$(msg "   既存: $(id "$NEW_USER")")"
else
    useradd --system --home "$NEW_HOME" --create-home --shell /usr/sbin/nologin "$NEW_USER"
    echo "$(msg "   作成: $(id "$NEW_USER")")"
fi

echo "$(msg "▶ 2. ディレクトリ作成")"
install -d -o "$NEW_USER" -g "$NEW_USER" -m 0700 "$STATE_DIR" "$KEY_DIR" "$NEW_HOME/logs"
# ホームはグループに通り抜け (x) のみ許可する。検知ログを group read で読める
# ようにするためで、state/keys は 0700 のままなので中身は見えない。
chmod 0710 "$NEW_HOME"
if [ "$A4_SECURITY_LOG_DIR" = "$NEW_HOME/logs" ]; then
    # 検知ログを保護ディレクトリに置く: グループは読み取り+通り抜けのみ。
    chmod 0750 "$NEW_HOME/logs"
    chgrp "$SHARE_GROUP" "$NEW_HOME/logs"
elif [ "$A4_SECURITY_LOG_DIR" != "$REPO_LOGS" ]; then
    install -d -o "$NEW_USER" -g "$SHARE_GROUP" -m 0750 "$A4_SECURITY_LOG_DIR"
fi
stat -c '%a %U:%G %n' "$NEW_HOME" "$STATE_DIR" "$KEY_DIR" "$NEW_HOME/logs" | sed 's/^/   /'

echo "$(msg "▶ 3. 状態ファイル・チェーン鍵を保護ディレクトリへ移行")"
# 設定 (agent_config.json / modules.json) は移設しない。ダッシュボードが保存の
# たびに書き換える唯一のファイルで、UI の設定変更を agent に届ける経路だから
# （§3.6 で共有グループに group read を与える）。
#
# 状態ファイル類（FIM / ports / SUID / cron / logins / blocks / alertstate）。
# 退避ファイル (*.legacy-* / *.pre-* / *.archive) は移行しない。誤って「生きた
# 基準」として使われると改ざん検知が無効化されるため。
for f in "$SRC_DATA"/kizuna-security-*.json "$REPO_LOGS"/kizuna-security-*.json; do
    [ -e "$f" ] || continue
    bn="$(basename "$f")"
    case "$bn" in
        *.legacy-*|*.pre-*|*.archive) continue ;;
        # 行数アンカーはプラグインが log_path から導出する。検知ログと必ず
        # 同じ場所に置く必要があるため §3.7 でログと一緒に扱う。
        *logstate*) continue ;;
    esac
    [ -f "$STATE_DIR/$bn" ] && continue
    install -o "$NEW_USER" -g "$NEW_USER" -m 0600 "$f" "$STATE_DIR/$bn"
done
ls -l "$STATE_DIR" 2>/dev/null | sed 's/^/   /'

# チェーン鍵
if [ -f "$SRC_DATA/keys/chain.key" ]; then
    install -o "$NEW_USER" -g "$NEW_USER" -m 0600 "$SRC_DATA/keys/chain.key" "$KEY_DIR/chain.key"
    echo "   $(stat -c '%a %U:%G' "$KEY_DIR/chain.key") $KEY_DIR/chain.key"
fi

# 既定の "./logs/..." は agent の cwd (= repo) 基準で解決されるため、運用
# ユーザーが書き換えられる repo logs/ に状態が置かれてしまう。保護ディレクトリ
# の絶対パスへ書き換えることで、移行後に「隠蔽」できなくする。
#
# これらのキーはプラグインの UI (Fields) にも出るため、後から UI で保存しても
# 消えない（web/static/modules.js は既存 config とマージする: Object.assign）。
#
# これは「既存インストールの移行」でだけ必要な処理。初回インストールの
# modules.json は無い（モジュールは UI から追加）ので、kizuna_security が
# 登録されていなければ見出しも含めて何も出さない。
MODULES_JSON="$SRC_DATA/modules.json"
if [ -f "$MODULES_JSON" ] && grep -q '"kizuna_security"' "$MODULES_JSON" 2>/dev/null; then
    if ! command -v python3 >/dev/null 2>&1; then
        if [ "$UI_LANG" = "en" ]; then echo "   ⚠️ python3 missing; cannot rewrite modules.json (install it via install.sh or check manually)"; else echo "   ⚠️ python3 が無いため modules.json を書き換えられません（install.sh で導入するか手動確認）"; fi
    else
    echo "$(msg "▶ 3.2 modules.json の状態パスを絶対パスへ書き換え")"
    python3 - "$MODULES_JSON" "$STATE_DIR" "$A4_SECURITY_LOG_DIR" "$KEY_DIR" "$REPO_LOGS/alert_history.jsonl" "$UI_LANG" <<'PY'
import json, os, sys, tempfile

path, state_dir, log_dir, key_dir, alert_hist, lang = sys.argv[1:7]
with open(path, encoding="utf-8") as fh:
    data = json.load(fh)

baselines = {
    "integrity_baseline_path": "kizuna-security-fim.json",
    "ssh_login_baseline_path": "kizuna-security-logins.json",
    "listen_port_baseline_path": "kizuna-security-ports.json",
    "suid_baseline_path": "kizuna-security-suid.json",
    # 高リスク領域（/tmp 等）専用の SUID ベースライン。ここを運用ユーザーの
    # 書き換えられる repo logs/ に置いたままだと、攻撃者が自分の SUID ファイルを
    # ベースラインに書き込んで「既知」に見せかけ、検知を回避できてしまう。
    "suid_fast_baseline_path": "kizuna-security-suid-fast.json",
    "cron_baseline_path": "kizuna-security-cron.json",
    "block_state_path": "kizuna-security-blocks.json",
    "alert_history_state_path": "kizuna-security-alertstate.json",
}

patched = False
for mod in data:
    if not isinstance(mod, dict) or mod.get("name") != "kizuna_security":
        continue
    cfg = mod.setdefault("config", {})
    for key, name in baselines.items():
        cfg[key] = os.path.join(state_dir, name)
    cfg["chain_key_path"] = os.path.join(key_dir, "chain.key")
    cfg["log_path"] = os.path.join(log_dir, "kizuna-security.log")
    # ダッシュボードが書くアラート履歴はプラグインの入力。絶対パスで固定する
    # （0600 のままだと agent が読めず整合性検証が失敗するため §3.7 で 0640）。
    cfg["alert_history_path"] = alert_hist
    patched = True
    break

if not patched:
    # bash 側の grep で kizuna_security の存在は確認済み。ここに来るのは
    # 解析上の例外だけなので、静かに何もしない（初回にモジュールの話を出さない）。
    sys.exit(0)

fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix=".modules.json-")
os.close(fd)
with open(tmp, "w", encoding="utf-8") as fh:
    json.dump(data, fh, ensure_ascii=False, indent=2)
    fh.write("\n")
st = os.stat(path)
os.chmod(tmp, st.st_mode & 0o7777)
try:
    os.chown(tmp, st.st_uid, st.st_gid)   # 所有者は運用ユーザーのまま（root 実行なので可能）
except PermissionError:
    pass
os.replace(tmp, path)
print(("   updated:" if lang == "en" else "   更新:"), path)
print("   log_path =", os.path.join(log_dir, "kizuna-security.log"))
PY
    fi
fi

# agent_config.json の log_file を絶対パスへ書き換える。既定 "logs/agent.log" は
# config ディレクトリ基準で解決され、A-4 後の agent (kizuna-eye) には data/logs が
# 書けない（0700 user 所有 + ProtectSystem=strict + ReadWritePaths 外）。共有ログ
# 置き場 (repo logs, kizuna-eye 所有) を絶対パスで指す。
AGENT_CFG="$SRC_DATA/agent_config.json"
if [ -f "$AGENT_CFG" ] && command -v python3 >/dev/null 2>&1; then
    python3 - "$AGENT_CFG" "$REPO_LOGS/agent.log" "$UI_LANG" <<'PY'
import json, os, sys, tempfile
path, log_path, lang = sys.argv[1:4]
with open(path, encoding="utf-8") as fh:
    d = json.load(fh)
d["log_file"] = log_path
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix=".agent_config.json-")
os.close(fd)
with open(tmp, "w", encoding="utf-8") as fh:
    json.dump(d, fh, ensure_ascii=False, indent=2)
    fh.write("\n")
st = os.stat(path)
os.chmod(tmp, st.st_mode & 0o7777)
try:
    os.chown(tmp, st.st_uid, st.st_gid)
except PermissionError:
    pass
os.replace(tmp, path)
print(("   updated:" if lang == "en" else "   更新:"), path, "log_file =", log_path)
PY
fi

echo "$(msg "▶ 3.5 プラグインを agent ユーザーから読めるようにする")"
# A-3/A-4: agent は kizuna-eye として動くため、dashboard(実行ユーザー) が
# 0600 で配置した .so / .so.sig は署名検証も dlopen もできず、移行直後に
# プラグインが全滅する（fail-closed なので静かに止まる）。ディレクトリは
# 読み取り+通り抜け、本体も読み取り可へ緩める。書き込み（追加・削除）は
# 所有者=dashboard 実行ユーザーのままなので、他ユーザーは配置できない。
PLUGINS_DIR="$BIN_DIR/plugins"
if [ -d "$PLUGINS_DIR" ]; then
    chmod 0755 "$PLUGINS_DIR"
    find "$PLUGINS_DIR" -maxdepth 1 -type f \( -name '*.so' -o -name '*.so.sig' \) -exec chmod 0644 {} +
    echo "   $(stat -c '%a %U:%G' "$PLUGINS_DIR") $PLUGINS_DIR"
    ls -1 "$PLUGINS_DIR" | sed 's/^/     /'
else
    echo "$(msg "   ℹ️ $PLUGINS_DIR が無いためスキップ")"
fi

# A-4/H-1: agent 本体 (agent_linux) も kizuna-eye から実行できる必要がある。
# dashboard (実行ユーザー) が 0700 でビルドしたバイナリは、agent が kizuna-eye に
# なった瞬間に exec できず 203/EXEC で起動不能になる（実機で発生）。実行権を
# グループ kizuna-eye に与える（所有者と書き込み権は変えない）。
AGENT_BIN="$BIN_DIR/agent_linux"
if [ -f "$AGENT_BIN" ]; then
    chgrp "$SHARE_GROUP" "$AGENT_BIN"
    chmod 0750 "$AGENT_BIN"
    echo "   $(stat -c '%a %U:%G' "$AGENT_BIN") $AGENT_BIN"
fi

echo "$(msg "▶ 3.6 共有グループと共有設定の権限")"
# agent は $DATA の agent_config.json / modules.json を読み続ける（UI の設定変更を
# agent に届けるため）。どちらもダッシュボード (= 運用ユーザー) が保存のたびに
# 書き換えるため、共有グループ kizuna-eye に group read (0640) を与える。
# ディレクトリは通り抜けのみ (0710) として、agent に $DATA の中身を列挙させない。
if ! getent group "$SHARE_GROUP" >/dev/null 2>&1; then
    groupadd --system "$SHARE_GROUP"
    echo "$(msg "   グループ作成: $SHARE_GROUP")"
fi
for u in "$NEW_USER" "$OLD_USER"; do
    if id -nG "$u" | tr ' ' '\n' | grep -qx "$SHARE_GROUP"; then
        echo "$(msg "   $u: 既に $SHARE_GROUP に所属")"
    else
        usermod -aG "$SHARE_GROUP" "$u"
        echo "$(msg "   $u を $SHARE_GROUP に追加（プロセスへの反映には再起動が必要）")"
    fi
done
chgrp "$SHARE_GROUP" "$SRC_DATA"
# setgid (2710): ダッシュボードが modules.json / agent_config.json を保存し直す
# たびに、新しいファイル (一時ファイル→rename) のグループが kizuna-eye で
# 継承されるようにする。これが無いと保存のたびにグループが user に戻り、
# agent が設定を読めなくなる。
chmod 2710 "$SRC_DATA"
for f in agent_config.json modules.json; do
    [ -f "$SRC_DATA/$f" ] || continue
    chgrp "$SHARE_GROUP" "$SRC_DATA/$f"
    chmod 0640 "$SRC_DATA/$f"
    echo "   $(stat -c '%a %U:%G' "$SRC_DATA/$f")"
done

# H-1: alert_history 署名鍵 (alert_history.key) は dashboard (= 運用ユーザー) が
# 書き続け、agent (kizuna-eye) が改ざん検証で読む。よって鍵は「運用ユーザー所有・
# group read」にする。keys ディレクトリは setgid (2710) にして、dashboard が鍵を
# 作り直してもグループ kizuna-eye を継承させる（group read が消えない）。
# chain.key は agent 専用領域へ移設済み（上の §3）。
if [ -d "$SRC_DATA/keys" ]; then
    chgrp "$SHARE_GROUP" "$SRC_DATA/keys"
    chmod 2710 "$SRC_DATA/keys"
    echo "   $(stat -c '%a %U:%G' "$SRC_DATA/keys") $SRC_DATA/keys"
fi
if [ -f "$SRC_DATA/keys/alert_history.key" ]; then
    chown "$OLD_USER:$SHARE_GROUP" "$SRC_DATA/keys/alert_history.key"
    chmod 0640 "$SRC_DATA/keys/alert_history.key"
    echo "   $(stat -c '%a %U:%G' "$SRC_DATA/keys/alert_history.key") $SRC_DATA/keys/alert_history.key"
fi

# 署名公開鍵: plugins.require_signature=true のとき agent が読む。$DATA 配下
# (0700) にあると agent から読めないため、/etc/kizuna-eye に配る。
PUB_SRC="$OLD_HOME/.kizuna-eye/keys/plugin_signing/plugin_signing.pub"
if [ -f "$PUB_SRC" ]; then
    install -D -m 0644 -o root -g root "$PUB_SRC" /etc/kizuna-eye/plugin_signing.pub
    echo "   $(stat -c '%a %U:%G' /etc/kizuna-eye/plugin_signing.pub) /etc/kizuna-eye/plugin_signing.pub"
else
    echo "$(msg "   ℹ️ $PUB_SRC なし（plugins.require_signature=false なら不要）")"
fi

echo "$(msg "▶ 3.7 ログの権限（共有ログ + 検知ログの移設）")"
# repo の logs/ は agent とダッシュボードの共有ログ置き場。setgid でグループを
# kizuna-eye に継承させ、sticky で互いのファイルを消しにくくする。agent は
# 自分のログ (agent.log / kizuna-backup-lite.log) を書き続けるためにディレクトリ
# への書き込み権限が必要なので、グループ書き込みを許可する。
if [ -d "$REPO_LOGS" ]; then
    chgrp "$SHARE_GROUP" "$REPO_LOGS"
    chmod 3770 "$REPO_LOGS"   # setgid + sticky + rwxrwx---
    echo "   $(stat -c '%a %U:%G' "$REPO_LOGS")"
fi

# agent / プラグインが「書き続ける」ログは所有者を agent に移す（書き込み権を
# 維持しつつ、group read で運用ユーザーとダッシュボードが読めるようにする）。
for f in "$REPO_LOGS/agent.log" "$REPO_LOGS/kizuna-backup-lite.log"; do
    [ -f "$f" ] || continue
    chown "$NEW_USER:$SHARE_GROUP" "$f"
    chmod 0640 "$f"
    echo "   $(stat -c '%a %U:%G' "$f")"
done
# プラグイン/ダッシュボードが「読む」ファイルは group read を与える。
# alert_history.jsonl はダッシュボードが書き、プラグインが整合性検証に使う
# （0600 のままだと agent が読めず V2-B の検証が失敗する）。
for f in "$REPO_LOGS/alert_history.jsonl" "$REPO_LOGS/dashboard.log"; do
    [ -f "$f" ] || continue
    chgrp "$SHARE_GROUP" "$f"
    chmod 0640 "$f"
    echo "   $(stat -c '%a %U:%G' "$f")"
done

# 検知ログ本体と行数アンカー (logstate。プラグインが log_path から導出) の移設。
# 退避ファイル (*.legacy-*) も一緒に移す（repo 側に残った古いアンカーを誤って
# 使わないようにするため）。
if [ "$A4_SECURITY_LOG_DIR" != "$REPO_LOGS" ]; then
    for f in "$REPO_LOGS"/kizuna-security.log*; do
        [ -e "$f" ] || continue
        bn="$(basename "$f")"
        if [ -e "$A4_SECURITY_LOG_DIR/$bn" ]; then
            echo "$(msg "   ℹ️ 移設先に既存: $A4_SECURITY_LOG_DIR/$bn（スキップ）")"
            continue
        fi
        mv "$f" "$A4_SECURITY_LOG_DIR/$bn"
        echo "$(msg "   移設: $bn")"
    done
    chown -R "$NEW_USER:$SHARE_GROUP" "$A4_SECURITY_LOG_DIR"
    find "$A4_SECURITY_LOG_DIR" -type f -exec chmod 0640 {} +
    find "$A4_SECURITY_LOG_DIR" -type d -exec chmod 0750 {} +
    echo "   $(stat -c '%a %U:%G' "$A4_SECURITY_LOG_DIR")"
    ls -l "$A4_SECURITY_LOG_DIR" | sed 's/^/     /'
else
    echo "$(msg "   ℹ️ 検知ログは repo logs に残す（UI のログ一覧に表示される）")"
    for f in "$REPO_LOGS"/kizuna-security.log*; do
        [ -e "$f" ] || continue
        chown "$NEW_USER:$SHARE_GROUP" "$f"
        chmod 0640 "$f"
        echo "   $(stat -c '%a %U:%G' "$f")"
    done
fi


echo "$(msg "▶ 4. unit 導入 (kizuna-eye-agent.service = A-4 版)")"
SRC_UNIT="$DIR/systemd/kizuna-eye-agent.service"
DST_UNIT="/etc/systemd/system/kizuna-eye-agent.service"
[ -f "$SRC_UNIT" ] || { echo "$(msg "❌ $SRC_UNIT が見つかりません")"; exit 1; }
if [ -f "$DST_UNIT" ]; then
    cp -a "$DST_UNIT" "/tmp/kizuna-eye-agent.service.bak-$TS"
    echo "$(msg "   既存を退避: /tmp/kizuna-eye-agent.service.bak-$TS")"
fi
sed -e "s|__DIR__|$DIR|g" -e "s|__BIN__|$BIN_DIR|g" -e "s|__DATA__|$SRC_DATA|g" "$SRC_UNIT" > "$DST_UNIT"
chmod 0644 "$DST_UNIT"
if grep -q '__[A-Z][A-Z_]*__' "$DST_UNIT"; then
    echo "$(msg "❌ unit に未置換のプレースホルダが残っています:")"
    grep -n '__[A-Z][A-Z_]*__' "$DST_UNIT"
    exit 1
fi

echo "$(msg "▶ 5. 旧 agent を停止")"
systemctl disable --now kizuna-eye-agent 2>/dev/null || true
if [ -x "$DIR/stop.sh" ]; then
    # sudo は既定で環境をリセットするため、UI_LANG を明示的に渡す（表示言語の維持）。
    sudo -u "$OLD_USER" env UI_LANG="$UI_LANG" "$DIR/stop.sh" || true
fi
sleep 1

# 手動起動された agent が残っていないか確認する。残ったまま §6 で systemd 版を
# 起動すると、同じ設定・同じ state（/var/lib/kizuna-eye）を奪い合う二重 agent に
# なり、アラート重複や FIM の誤検知が延々と続く。
find_leftover_agents() {
    local p exe
    for p in /proc/[0-9]*; do
        exe="$(readlink "$p/exe" 2>/dev/null || true)"
        exe="${exe% (deleted)}"
        [ "$exe" = "$BIN_DIR/agent_linux" ] && echo "${p#/proc/}"
    done
    # set -e + pipefail なので、見つからなかったときに 1 を返すとスクリプトが
    # 途中で落ちる。常に 0 を返し、判定は出力の有無で行う。
    return 0
}
LEFTOVER="$(find_leftover_agents | tr '\n' ' ' || true)"
if [ -n "$LEFTOVER" ]; then
    echo "$(msg "   ⚠️ agent_linux が残っています (PID: $LEFTOVER) → root 権限で停止します")"
    # shellcheck disable=SC2086
    kill $LEFTOVER 2>/dev/null || true
    sleep 2
    LEFTOVER="$(find_leftover_agents | tr '\n' ' ' || true)"
    if [ -n "$LEFTOVER" ]; then
        # shellcheck disable=SC2086
        kill -9 $LEFTOVER 2>/dev/null || true
        sleep 1
        LEFTOVER="$(find_leftover_agents | tr '\n' ' ' || true)"
    fi
fi
if [ -n "$LEFTOVER" ]; then
    echo "$(msg "❌ agent_linux を停止できませんでした (PID: $LEFTOVER)。二重起動を避けるため中止します。")"
    echo "$(msg "   手動で停止してから再実行してください: sudo kill -9 $LEFTOVER")"
    exit 1
fi
echo "$(msg "   ✅ 旧 agent は残っていません")"

echo "$(msg "▶ 6. 起動")"
systemctl daemon-reload
systemctl enable --now kizuna-eye-agent
sleep 2
systemctl --no-pager --full status kizuna-eye-agent || true

echo ""
echo "$(msg "✅ 移行完了。確認してください:")"
echo "   - systemctl status kizuna-eye-agent"
echo "   - journalctl -u kizuna-eye-agent -n 50"
echo "$(msg "   - *ダッシュボードを再起動*（kizuna-eye グループ所属を反映。UI のログ閲覧と")"
echo "$(msg "     共有設定の読み書きに必要）。agent は systemd 管理なので start.sh / stop.sh は")"
echo "$(msg "     agent を触らない（二重起動防止）。dashboard だけを指定する:")"
echo "       sudo -u $OLD_USER $DIR/stop.sh dashboard"
echo "       sudo -u $OLD_USER $DIR/start.sh dashboard"
echo "$(msg "   - ダッシュボード GET /api/status とアラート/Discord 通知")"
echo "$(msg "   - FIM の権限 INFO が消えること（CAP_DAC_READ_SEARCH の効果）")"
echo "$(msg "   - backup プラグインの書き込み先に agent が書けること（ReadWritePaths を確認）")"
echo "$(msg "   - 検知ログ（group read なので sudo 不要）: ls -l $A4_SECURITY_LOG_DIR/kizuna-security.log")"
echo "$(msg "   - 状態ファイル（agent 専用）: sudo ls -l $STATE_DIR")"
echo "$(msg "   - 共有設定が 0640 kizuna-eye であること: ls -l $SRC_DATA/agent_config.json（modules.json は登録後に作成される）")"
echo "$(msg "   ログ方針を UI のログ一覧優先に変える場合:")"
echo "     sudo A4_SECURITY_LOG_DIR=$REPO_LOGS $0 $OLD_USER"
echo "$(msg "   ロールバック用バックアップ: $BACKUP")"
