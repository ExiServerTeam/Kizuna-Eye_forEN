#!/bin/bash
# ============================================================
# A-4: agent を専用ユーザー kizuna-agent へ移行する（要 sudo・対話実行）
#
# 目的:
#   agent を運用アカウント `user`(uid=1000) から切り離す。uid=1000 が
#   奪われても FIM ベースライン / チェーン鍵 / アラート状態を改変できない。
#   併せて AmbientCapabilities=CAP_DAC_READ_SEARCH で /etc/shadow 等を
#   読めるようにし、A-2 の「権限不足 INFO」も解消する。
#
# 設計:
#   1. 状態ファイル (FIM/ports/SUID/cron/logins/blocks/alertstate) と chain.key は
#      /var/lib/kizuna-eye/{state,keys} (0700, kizuna-agent 所有) へ移設し、
#      modules.json の各パスを絶対パスへ書き換える。agent からは常に書き込め、
#      運用ユーザーからは読み書きできない。
#   2. 設定 (agent_config.json / modules.json) は $DATA のまま *共有* する。
#      ダッシュボードが保存のたびに書き換える唯一のファイルで、UI の設定変更
#      (プラグイン有効化・チェック間隔・トークン) を agent に届ける経路だから。
#      共有グループ kizuna-eye を作り 0640 (group read) を与える。agent 側の
#      再 chmod 0600 は fsutil.TightenSharedConfigMode が group read を保つ。
#   3. 検知ログ (kizuna-security.log) とその行数アンカー (logstate) は既定で
#      /var/lib/kizuna-eye/logs (0750, kizuna-agent:kizuna-eye) へ移設する。
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
#   sudo A4_SECURITY_LOG_DIR=/samba/share/Kizuna-Eye/logs systemd/migrate-agent-user.sh user
#
# 前提: systemd/kizuna-agent-a4.service が本リポジトリにあること。
#       設定変更の共有のため、実行前に tmp/_a4_prep.sh を phase1 → phase2 →
#       phase3 → phase4 の順に済ませておく（phase4 で require_signature を
#       立てた後は再起動せず、そのまま本スクリプトまで進める）。
#
# ロールバック:
#   sudo systemctl disable --now kizuna-agent
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

NEW_USER="kizuna-agent"
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

echo "▶ 0. バックアップ"
tar czf "$BACKUP" -C / \
    "${SRC_DATA#/}" \
    "${BIN_DIR#/}"/*.meta.json 2>/dev/null || true
# 状態ファイルは repo logs にもあるため併せて退避
tar rzf "$BACKUP" -C "$DIR" logs 2>/dev/null || true
echo "   $BACKUP"
echo "   ロールバックはこの tar を展開し、元の所有者へ chown して start.sh で起動します。"

echo "▶ 1. ユーザー作成 (存在すればスキップ)"
if id "$NEW_USER" >/dev/null 2>&1; then
    echo "   既存: $(id "$NEW_USER")"
else
    useradd --system --home "$NEW_HOME" --create-home --shell /usr/sbin/nologin "$NEW_USER"
    echo "   作成: $(id "$NEW_USER")"
fi

echo "▶ 2. ディレクトリ作成"
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

echo "▶ 3. 状態ファイル・チェーン鍵を保護ディレクトリへ移行"
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

echo "▶ 3.2 modules.json の状態パスを絶対パスへ書き換え"
# 既定の "./logs/..." は agent の cwd (= repo) 基準で解決されるため、運用
# ユーザーが書き換えられる repo logs/ に状態が置かれてしまう。保護ディレクトリ
# の絶対パスへ書き換えることで、移行後に「隠蔽」できなくする。
#
# これらのキーはプラグインの UI (Fields) にも出るため、後から UI で保存しても
# 消えない（web/static/modules.js は既存 config とマージする: Object.assign）。
MODULES_JSON="$SRC_DATA/modules.json"
if [ -f "$MODULES_JSON" ] && command -v python3 >/dev/null 2>&1; then
    python3 - "$MODULES_JSON" "$STATE_DIR" "$A4_SECURITY_LOG_DIR" "$KEY_DIR" "$REPO_LOGS/alert_history.jsonl" <<'PY'
import json, os, sys, tempfile

path, state_dir, log_dir, key_dir, alert_hist = sys.argv[1:6]
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
    print("   ⚠️ modules.json に kizuna_security モジュールが無いため書き換えできません")
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
print("   更新:", path)
print("   log_path =", os.path.join(log_dir, "kizuna-security.log"))
PY
else
    echo "   ⚠️ python3 が無いため modules.json を書き換えられません（手動確認が必要）"
fi

echo "▶ 3.5 プラグインを agent ユーザーから読めるようにする"
# A-3/A-4: agent は kizuna-agent として動くため、dashboard(実行ユーザー) が
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
    echo "   ℹ️ $PLUGINS_DIR が無いためスキップ"
fi

echo "▶ 3.6 共有グループと共有設定の権限"
# agent は $DATA の agent_config.json / modules.json を読み続ける（UI の設定変更を
# agent に届けるため）。どちらもダッシュボード (= 運用ユーザー) が保存のたびに
# 書き換えるため、共有グループ kizuna-eye に group read (0640) を与える。
# ディレクトリは通り抜けのみ (0710) として、agent に $DATA の中身を列挙させない。
if ! getent group "$SHARE_GROUP" >/dev/null 2>&1; then
    groupadd --system "$SHARE_GROUP"
    echo "   グループ作成: $SHARE_GROUP"
fi
for u in "$NEW_USER" "$OLD_USER"; do
    if id -nG "$u" | tr ' ' '\n' | grep -qx "$SHARE_GROUP"; then
        echo "   $u: 既に $SHARE_GROUP に所属"
    else
        usermod -aG "$SHARE_GROUP" "$u"
        echo "   $u を $SHARE_GROUP に追加（プロセスへの反映には再起動が必要）"
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

# 署名公開鍵: plugins.require_signature=true のとき agent が読む。$DATA 配下
# (0700) にあると agent から読めないため、/etc/kizuna-eye に配る。
PUB_SRC="$OLD_HOME/.kizuna-eye/keys/plugin_signing/plugin_signing.pub"
if [ -f "$PUB_SRC" ]; then
    install -D -m 0644 -o root -g root "$PUB_SRC" /etc/kizuna-eye/plugin_signing.pub
    echo "   $(stat -c '%a %U:%G' /etc/kizuna-eye/plugin_signing.pub) /etc/kizuna-eye/plugin_signing.pub"
else
    echo "   ℹ️ $PUB_SRC なし（plugins.require_signature=false なら不要）"
fi

echo "▶ 3.7 ログの権限（共有ログ + 検知ログの移設）"
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
            echo "   ℹ️ 移設先に既存: $A4_SECURITY_LOG_DIR/$bn（スキップ）"
            continue
        fi
        mv "$f" "$A4_SECURITY_LOG_DIR/$bn"
        echo "   移設: $bn"
    done
    chown -R "$NEW_USER:$SHARE_GROUP" "$A4_SECURITY_LOG_DIR"
    find "$A4_SECURITY_LOG_DIR" -type f -exec chmod 0640 {} +
    find "$A4_SECURITY_LOG_DIR" -type d -exec chmod 0750 {} +
    echo "   $(stat -c '%a %U:%G' "$A4_SECURITY_LOG_DIR")"
    ls -l "$A4_SECURITY_LOG_DIR" | sed 's/^/     /'
else
    echo "   ℹ️ 検知ログは repo logs に残す（UI のログ一覧に表示される）"
    for f in "$REPO_LOGS"/kizuna-security.log*; do
        [ -e "$f" ] || continue
        chown "$NEW_USER:$SHARE_GROUP" "$f"
        chmod 0640 "$f"
        echo "   $(stat -c '%a %U:%G' "$f")"
    done
fi


echo "▶ 4. unit 導入 (kizuna-agent.service = A-4 版)"
SRC_UNIT="$DIR/systemd/kizuna-agent-a4.service"
DST_UNIT="/etc/systemd/system/kizuna-agent.service"
[ -f "$SRC_UNIT" ] || { echo "❌ $SRC_UNIT が見つかりません"; exit 1; }
if [ -f "$DST_UNIT" ]; then
    cp -a "$DST_UNIT" "/tmp/kizuna-agent.service.bak-$TS"
    echo "   既存を退避: /tmp/kizuna-agent.service.bak-$TS"
fi
sed -e "s|__DIR__|$DIR|g" -e "s|__BIN__|$BIN_DIR|g" -e "s|__DATA__|$SRC_DATA|g" "$SRC_UNIT" > "$DST_UNIT"
chmod 0644 "$DST_UNIT"
if grep -q '__[A-Z][A-Z_]*__' "$DST_UNIT"; then
    echo "❌ unit に未置換のプレースホルダが残っています:"
    grep -n '__[A-Z][A-Z_]*__' "$DST_UNIT"
    exit 1
fi

echo "▶ 5. 旧 agent を停止"
systemctl disable --now kizuna-agent 2>/dev/null || true
if [ -x "$DIR/stop.sh" ]; then
    sudo -u "$OLD_USER" "$DIR/stop.sh" || true
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
    echo "   ⚠️ agent_linux が残っています (PID: $LEFTOVER) → root 権限で停止します"
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
    echo "❌ agent_linux を停止できませんでした (PID: $LEFTOVER)。二重起動を避けるため中止します。"
    echo "   手動で停止してから再実行してください: sudo kill -9 $LEFTOVER"
    exit 1
fi
echo "   ✅ 旧 agent は残っていません"

echo "▶ 6. 起動"
systemctl daemon-reload
systemctl enable --now kizuna-agent
sleep 2
systemctl --no-pager --full status kizuna-agent || true

echo ""
echo "✅ 移行完了。確認してください:"
echo "   - systemctl status kizuna-agent"
echo "   - journalctl -u kizuna-agent -n 50"
echo "   - *ダッシュボードを再起動*（kizuna-eye グループ所属を反映。UI のログ閲覧と"
echo "     共有設定の読み書きに必要）。agent は systemd 管理なので start.sh / stop.sh は"
echo "     agent を触らない（二重起動防止）。dashboard だけを指定する:"
echo "       sudo -u $OLD_USER $DIR/stop.sh dashboard"
echo "       sudo -u $OLD_USER $DIR/start.sh dashboard"
echo "   - ダッシュボード GET /api/status とアラート/Discord 通知"
echo "   - FIM の権限 INFO が消えること（CAP_DAC_READ_SEARCH の効果）"
echo "   - backup プラグインが /samba/share/CD へ書けること"
echo "   - 検知ログ（group read なので sudo 不要）: ls -l $A4_SECURITY_LOG_DIR/kizuna-security.log"
echo "   - 状態ファイル（agent 専用）: sudo ls -l $STATE_DIR"
echo "   - 共有設定が 0640 kizuna-eye であること: ls -l $SRC_DATA/agent_config.json $SRC_DATA/modules.json"
echo "   ログ方針を UI のログ一覧優先に変える場合:"
echo "     sudo A4_SECURITY_LOG_DIR=$REPO_LOGS $0 $OLD_USER"
echo "   ロールバック用バックアップ: $BACKUP"
