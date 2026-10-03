# 残タスクの手元実行手順（SMB是正 / 秘密情報移行 / chain.key ローテーション）

- 対象: サーバ `192.168.0.233`（`user` で SSH、sudo はパスワードあり）
- 稼働: `/opt/kizuna-eye/bin/dashboard_linux`・`agent_linux`（cwd `/samba/share/Kizuna-Eye`）
- 関連: `docs/session_security_audit_20261003.md` §10（chmod 適用と SMB 露出の判定）
- 前提: 変更前に必ずバックアップ（本書は各手順の先頭で `/tmp` 等へ退避する）

## 0. 現状（2026-10-03 実測）

| 項目 | 実測値 |
|---|---|
| `/etc/samba/smb.conf` `[Share-1TB]` | 248-255行: `read only = no` / `guest ok = yes` / `create mask = 0777` / `directory mask = 0777` / `force user = user` / `browseable = yes` |
| `[global]` | 105行 `map to guest = bad user` |
| `smbd` | active |
| 共有上の秘密（600） | `dashboard_config.json` / `agent_config.json` / `users.json` / `sessions.json` / `modules.json` / `keys/chain.key`（32B） |
| ディレクトリ | `keys/` 700, `logs/` 700（配下 600）, `avatars/` 700 |
| 共有 ACL | 共有内の複数ファイルに `+`（Samba 由来）。`getfacl`/`setfacl` は未導入 |
| プラグイン設定 | `modules.json` の `kizuna_security.config` は `plugin_path` / `watch_files` / `poll_interval_sec` / `failed_burst` / `block_mode` のみ |
| プラグイン既定値 | `chain_key_path = /samba/share/Kizuna-Eye/keys/chain.key`、`integrity_baseline_path`・`log_path` 等は cwd 相対（`./logs/...`）、`integrity_files` は `/etc/*` 等のシステムファイル9件 |

要点: `force user = user` により **SMB 経由のアクセスは uid 1000（= ファイル所有者）になる**ため、`chmod 600` は SMB に対して無効。したがって対策は
(A) 共有を認証必須にする（§1）、(B) 秘密を共有外へ移す（§2）の 2 段で行う。

---

## 1. タスクA: `[Share-1TB]` のゲスト無効化（要 sudo）

```bash
# 1-0 バックアップ
sudo cp -a /etc/samba/smb.conf /tmp/smb.conf.bak-$(date +%Y%m%d_%H%M%S)

# 1-1 [global]: ゲストへのフォールバックを止める（105行目を置換）
sudo sed -i 's/^[[:space:]]*map to guest[[:space:]]*=.*/   map to guest = never/' /etc/samba/smb.conf

# 1-2 [Share-1TB]: 認証必須 + 既定マスクを締める（248-255行のブロックを置換）
sudo sed -i '/^\[Share-1TB\]/,/^$/c\[Share-1TB]\n   path = /samba/share\n   browseable = yes\n   read only = no\n   guest ok = no\n   valid users = user\n   create mask = 0600\n   directory mask = 0700\n   force user = user\n' /etc/samba/smb.conf

# 1-3 構文検証（エラーがあれば /etc/samba/smb.conf を直す）
sudo testparm -s

# 1-4 反映
sudo systemctl restart smbd
```

補足:
- `map to guest` は **[global] 専用**パラメータ（共有セクションには書かない）。
- `valid users = user` を有効にするには Samba 側のユーザーが必要。未作成なら
  `sudo smbpasswd -a user` で Samba パスワードを設定する（未設定だと認証で弾かれる）。
- 既存ファイルのモードは変わらない（`create mask` は新規作成分にのみ効く）。
  既存分まで締めたい場合は次を（`web/` を含むため実行前に内容確認）:
  `cd /samba/share/Kizuna-Eye && chmod -R go-rwx .`（`*.example.json` 等も 600 になる点は許容）

検証:

```bash
# 匿名（ゲスト）では一覧が取れない → NT_STATUS_ACCESS_DENIED 等になればOK
smbclient -N -L //localhost 2>&1 | head
smbclient -N //localhost/Share-1TB -c 'ls' 2>&1 | head
# 認証ありでは取れる（Samba パスワード入力）
smbclient -U user //localhost/Share-1TB -c 'ls' 2>&1 | head
```

ロールバック:

```bash
sudo cp -a /tmp/smb.conf.bak-YYYYmmdd_HHMMSS /etc/samba/smb.conf && sudo systemctl restart smbd
```

---

## 2. タスクB: 秘密情報の共有外移行（要 sudo）

移行先は共有外の `/opt/kizuna-eye/data/`（0700, user:user）。**`users.json` / `sessions.json` は
「設定ファイルと同じディレクトリ」に自動解決される**（`pkg/config` の `absFromConfigDir` /
dashboard `configDir` 解決）ため、設定と同居させる必要がある。`modules.json` は dashboard が
`<configDir>/modules.json` を、agent が `-modules` 引数（既定 cwd 相対）を読む。

```bash
# 2-0 移行先の作成
sudo install -d -m 700 -o user -g user /opt/kizuna-eye/data
sudo install -d -m 700 -o user -g user /opt/kizuna-eye/data/keys

# 2-1 停止してからコピー（稼働中の書き込みを避ける）
cd /samba/share/Kizuna-Eye && ./stop.sh
sudo cp -a dashboard_config.json agent_config.json users.json sessions.json modules.json /opt/kizuna-eye/data/
sudo cp -a keys/chain.key /opt/kizuna-eye/data/keys/chain.key
sudo chown -R user:user /opt/kizuna-eye/data
sudo chmod 700 /opt/kizuna-eye/data /opt/kizuna-eye/data/keys
sudo chmod 600 /opt/kizuna-eye/data/*.json /opt/kizuna-eye/data/keys/chain.key
```

### 2-2 `start.sh` を絶対パス起動に変更（パッチ）

```diff
--- a/start.sh
+++ b/start.sh
@@
 BIN_DIR="${KIZUNA_BIN_DIR:-/opt/kizuna-eye/bin}"
+# 設定・鍵の置き場（共有外）。移行前は従来どおり ./ を使う。
+DATA_DIR="${KIZUNA_DATA_DIR:-$PWD}"
@@
-# start_one <binary-name> <config> <logfile>
+# start_one <binary-name> <config> <logfile> [extra args...]
@@
 start_one() {
-    local name="$1" cfg="$2" log="$3"
+    local name="$1" cfg="$2" log="$3"; shift 3
@@
-    nohup "$BIN_DIR/$name" -config "$cfg" >> "$log" 2>&1 < /dev/null &
+    nohup "$BIN_DIR/$name" -config "$cfg" "$@" >> "$log" 2>&1 < /dev/null &
@@
-start_one dashboard_linux dashboard_config.json logs/dashboard.log && new_started=1 || already_running=1
+start_one dashboard_linux "$DATA_DIR/dashboard_config.json" logs/dashboard.log && new_started=1 || already_running=1
 sleep 1
-start_one agent_linux agent_config.json logs/agent.log && new_started=1 || already_running=1
+start_one agent_linux "$DATA_DIR/agent_config.json" logs/agent.log -modules "$DATA_DIR/modules.json" && new_started=1 || already_running=1
```

移行時は `/opt/kizuna-eye/data`、移行前は `$PWD`（= 現行動作）を使うため、先にパッチを当てておいても
挙動は変わらない（`KIZUNA_DATA_DIR` を指定した時だけ切り替わる）。

### 2-3 プラグインの鍵パスを移行先へ向ける（`modules.json`）

`chain_key_path` はプラグイン設定で上書きできる（`plugin/config.go` `ParseConfig`）。移行先へ
1 行追加する（`watch_files` 等の既存値はそのまま）:

```json
    "config": {
      "plugin_path": "/opt/kizuna-eye/bin/plugins/kizuna_security.so",
      "watch_files": "/var/log/auth.log,/var/log/dpkg.log,/var/log/apt/history.log",
      "poll_interval_sec": 15,
      "failed_burst": 5,
      "block_mode": "off",
      "chain_key_path": "/opt/kizuna-eye/data/keys/chain.key"
    },
```

- これを忘れると、鍵ファイルを見つけられず **鍵なし運用（SHA-256）に静かに縮退**する。
- プラグイン自身のログ・ベースライン（`./logs/kizuna-security-*.json`）は cwd 相対のままでよい
  （`logs/` は 700 に締め済み）。共有外へ移す場合は `log_path` / `integrity_baseline_path` /
  `alert_history_path` / `suid_baseline_path` 等も同じ書式で指定する。

### 2-4 起動と確認

```bash
cd /samba/share/Kizuna-Eye && ./start.sh
ps aux | grep -E 'dashboard_linux|agent_linux' | grep -v grep
grep -E 'Kizuna-Security 設定完了|プラグイン.*登録' logs/agent.log | tail -2
ls -l /opt/kizuna-eye/data /opt/kizuna-eye/data/keys   # すべて 600 / ディレクトリ 700
```

### 2-5 共有側の原本を退避（ロールバック可能な形で）

稼働と鍵チェーンが正常なのを確認してから、共有上の原本を削除する。**残すと SMB 露出が続く**。

```bash
cd /samba/share/Kizuna-Eye
sudo install -d -m 700 -o user -g user /opt/kizuna-eye/data/originals-backup-$(date +%Y%m%d)
sudo cp -a dashboard_config.json agent_config.json users.json sessions.json modules.json keys /opt/kizuna-eye/data/originals-backup-$(date +%Y%m%d)/
rm -f dashboard_config.json agent_config.json users.json sessions.json modules.json
rmdir keys 2>/dev/null || rm -rf keys     # chain.key のみ収容（移行済み）
git status --short                        # 追跡外なので変化なしが正常
```

ロールバック: `cp -a` した原本を戻し、`start.sh` のパッチを戻す（または `KIZUNA_DATA_DIR=$PWD` を付けずに起動）。

---

## 3. タスクC: `chain.key` のローテーション（漏洩前提の再発行）

`keys/chain.key` は HMAC 鍵で、用途は次の 2 つ（`plugin/integrity.go`・`plugin/fim.go`）:
1. `logs/kizuna-security.log` のチェーン署名（`VerifyChainKeyed`）
2. FIM ベースライン `logs/kizuna-security-fim.json` の署名（`signFimState`）

鍵を替えると (1) は **自動で** `.legacy-<ts>` へ退避され新チェーンが始まる（`NewFileLoggerKeyed`）。
(2) は「署名が一致しません」で **critical が 1 回通知され、ベースラインを取り直す**設計
（`loadBaseline` が false を返し再ベースライン）。よって「critical 1 件は想定内」として実施する。

```bash
# 3-0 停止（書き込み中のログを跨がないため）
cd /samba/share/Kizuna-Eye && ./stop.sh

# 3-1 旧鍵を共有外へバックアップ（ローテーション前の検証用に保管）
sudo install -d -m 700 -o user -g user /opt/kizuna-eye/data/keys/retired
cp -p keys/chain.key /opt/kizuna-eye/data/keys/retired/chain.key.$(date +%Y%m%d_%H%M%S)

# 3-2 新鍵（32B）を発行（移行後は /opt/kizuna-eye/data/keys/chain.key を対象にする）
umask 077; head -c 32 /dev/urandom > keys/chain.key; chmod 600 keys/chain.key
ls -l keys/chain.key   # 32 バイト・600 を確認

# 3-3 起動（旧ログは自動で .legacy-<ts> へ退避、FIM は critical 1 件 → 再ベースライン）
./start.sh
```

確認:

```bash
ls -l logs/ | grep -E 'legacy|kizuna-security-fim.json'   # .legacy-<ts> と新しいベースライン
grep -c 'log_chain_broken' logs/plugin.log logs/agent.log 2>/dev/null   # 0 が正常
grep -E 'ベースライン署名の検証に失敗' logs/agent.log | tail -2          # 意図した 1 件だけ
# 5 分後（integrity_check_interval_sec 既定 300）に追加の critical が出ていないこと
```

注: 鍵ID を行に持たせる改修（監査 F-4 の推奨）は未実装のため、**旧鍵で署名した区間は新鍵では
検証できない**。退避した `.legacy-<ts>` と retired 鍵をセットで保管し、必要時に旧鍵で検証する。

### 3-4 実施記録（2026-10-03 17:52 実施済み／sudo 不要で完結）

`/opt/kizuna-eye/data` は未作成だったため、retired 鍵の退避先を**共有外のホーム**にして実施:

| 項目 | 値 |
|---|---|
| 旧鍵 sha256 | `308a341888227bebccbb1fabf283c65e509ec0203797b1408b1c5a949c527e10` |
| 新鍵 sha256 | `21def4e5230c901f02b8c458b24dbaf3acecbe84526b1439d9bb8bfed3ecf8f7` |
| 旧鍵の退避先 | `~/.kizuna-eye/keys/chain.key.20261003_175242`（600） |
| 記録 | `~/.kizuna-eye/keys/OLD-key.sha256` / `NEW-key.sha256`（600） |
| 旧チェーン | `logs/kizuna-security.log.legacy-20261003_175243`（2.49MB、自動退避） |
| FIM | `logs/kizuna-security-fim.json` を 17:52 に再作成（想定どおり critical 1 件） |
| 事後 | 両プロセス稼働・再認証 OK（`Agent 認証済み接続` 110→111）・`log_chain_broken` 0 件 |

**注意**: この時点で新鍵はまだ**共有上の `keys/chain.key`（ゲスト書込可）**にある。攻撃者が読める限り
ローテーションの効果は限定的なので、§1（SMB 是正）＋ §2（秘密移行）で鍵を共有外へ移した後に
**もう一度 §3 を実施**するのが望ましい（移行後は `/opt/kizuna-eye/data/keys/chain.key` を対象に、
retired は §3-1 の `/opt/.../keys/retired` へ）。

---

## 4. 実施順序と注意

1. §1（smb.conf）→ 匿名アクセス遮断を確認
2. §3（鍵ローテーション）→ 共有上に鍵がある間に実施しても良いが、§2 の後なら `/opt` 側を対象にする
3. §2（秘密移行）→ 最後に共有上の原本を退避（§2-5）
4. 共有の書き込み可否は `web/`（格納型 XSS）・`scripts/`・`start.sh`・`modules.json` の改ざんに直結する。
   §1 の `create mask/directory mask` だけでは**既存ファイルは締まらない**ため、§1 の補足の
   `chmod -R go-rwx` か、ファイル所有を別 uid/root にする運用（監査の根本対策）を検討する。
5. `install.sh` の sudoers 設定（`/etc/sudoers.d/kizuna-security-cron`）が指す実体は
   `/usr/local/bin/kizuna-cron-read.sh`（root:root 0755, 共有外）で、共有上の原本
   `scripts/kizuna-cron-read.sh` は install 時にのみ読まれる。共有への書き込みを禁じれば
   「次回 install 時に差し替え」という経路も塞がる（§1）。
