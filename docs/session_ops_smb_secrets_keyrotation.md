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
| 共有上の秘密（600） | §2 実施前: `dashboard_config.json` / `agent_config.json` / `users.json` / `sessions.json` / `modules.json` / `keys/chain.key`（32B）。**実施後は全て削除済み**（`~/.kizuna-eye/data` へ移行） |
| ディレクトリ | `keys/` 700, `logs/` 700（配下 600）, `avatars/` 700 |
| 共有 ACL | 共有内の複数ファイルに `+`（Samba 由来）。`getfacl`/`setfacl` は未導入 |
| プラグイン設定 | `modules.json` の `kizuna_security.config` は `plugin_path` / `watch_files` / `poll_interval_sec` / `failed_burst` / `block_mode` のみ |
| プラグイン既定値 | `chain_key_path = /samba/share/Kizuna-Eye/keys/chain.key`、`integrity_baseline_path`・`log_path` 等は cwd 相対（`./logs/...`）、`integrity_files` は `/etc/*` 等のシステムファイル9件 |

要点: `force user = user` により **SMB 経由のアクセスは uid 1000（= ファイル所有者）になる**ため、`chmod 600` は SMB に対して無効。したがって対策は
(A) 共有を認証必須にする（§1）、(B) 秘密を共有外へ移す（§2）の 2 段で行う。

---

## 1. タスクA: `[Share-1TB]` のゲスト無効化（**未実施・要 sudo**）

現状は未実施（`map to guest = bad user` / `guest ok = yes` のまま）。理由は次の 3 点で、いずれも
`user` のパスワードを持つ管理者が対話的に実行する必要がある（SSH の非対話 sudo は不可）。

1. `sudo cp -a` / `sudo sed -i` / `sudo testparm` / `sudo systemctl restart smbd` に sudo が要る。
2. `valid users = user` を効かせるには **Samba ユーザー `user` のパスワード登録**（`sudo smbpasswd -a user`）が
   必要になる可能性がある。未登録のまま `guest ok = no` にすると、**Windows の共有（Z: ドライブ）が
   即座に使えなくなる**ため、パスワードを決めてから 1-4 を実行する。
3. 実行すると `Z:` 経由のアクセスに認証が要る（資格情報の再入力）。作業中の切断を避けるため、
   作業者がコンソール前にいる状態で行う。

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

## 2. タスクB: 秘密情報の共有外移行（実施済み: 2026-10-03 17:56・sudo 不要で完結）

移行先は共有外の `$HOME/.kizuna-eye/data/`（= `/home/user/.kizuna-eye/data`、0700, user:user）と
した。`users.json` / `sessions.json` は「設定ファイルと同じディレクトリ」に自動解決される
（`pkg/config` の `absFromConfigDir` と dashboard の `configDir` 解決）ため、設定と同居が必要。
`/opt/kizuna-eye/data` ではなくホーム配下にした理由は、`/opt/kizuna-eye` が root 所有で
非 root 運用のままでは `install -d` に sudo が要るため（SMB 是正と同じ sudo 依存を持ち込まない）。

### 2-1 実施記録

| 項目 | 内容 |
|---|---|
| 移行先 | `~/.kizuna-eye/data/` 700 / `~/.kizuna-eye/data/keys` 700 / 各ファイル 600 |
| 移行したもの | `dashboard_config.json`（`agent_token`）, `agent_config.json`, `users.json`（bcrypt）, `sessions.json`, `modules.json`, `keys/chain.key` |
| 共有上の原本 | 削除済み（いずれも追跡外なので `git status --short` は変わらない） |
| 恒久バックアップ | `~/.kizuna-eye/originals-backup-20261003.tar.gz`（600、移行前の内容一式） |
| 移行時の一時退避 | `/tmp/kizuna-secrets-backup-<ts>/`（再起動で消える。恒久は上記 tar.gz） |
| 起動 | `start.sh` と `scripts/verify.sh` に同じ自動判定パッチ（§2-2）。`./start.sh` は引数不要 |
| 確認 | 両プロセス稼働・`Agent 認証済み接続` 111→112・プラグインは鍵を読めて正常起動 |
| 事後 | 共有側に `users.json` / `sessions.json` / `*_config.json` / `modules.json` / `keys/` が無いこと |

稼働中の書き込みを避けるため、停止 → コピー → 起動の順で行った（`./stop.sh` は停止後に
プロセスが残っていないことを確認できる）。

### 2-2 `start.sh` / `scripts/verify.sh` のパッチ（置き場の自動判定）

判定順は「`KIZUNA_DATA_DIR` の明示 > 移行先に `dashboard_config.json` があればそこ > 無ければ従来どおり `./`」。
これにより、共有上の設定に戻せば**パッチを戻さなくても**従来動作に戻る。

```bash
# 設定・秘密・鍵の置き場。共有上（./）から共有外（移行後）へ自動で切り替える。
DATA_DIR="${KIZUNA_DATA_DIR:-}"
if [ -z "$DATA_DIR" ]; then
    if [ -f "$HOME/.kizuna-eye/data/dashboard_config.json" ]; then
        DATA_DIR="$HOME/.kizuna-eye/data"
    else
        DATA_DIR="$PWD"
    fi
fi
```

起動側は `-config "$DATA_DIR/<name>.json"` を渡し、agent には `-modules "$DATA_DIR/modules.json"` を追加する
（`start_one` は第 4 引数以降をそのまま渡す可変長に変更）。

### 2-3 プラグインの鍵パス（`modules.json`）

`chain_key_path` は移行先を指す（`plugin/config.go` `ParseConfig` が上書きを受け付ける）:

```json
    "config": {
      "plugin_path": "/opt/kizuna-eye/bin/plugins/kizuna_security.so",
      "watch_files": "/var/log/auth.log,/var/log/dpkg.log,/var/log/apt/history.log",
      "poll_interval_sec": 15,
      "failed_burst": 5,
      "block_mode": "off",
      "chain_key_path": "/home/user/.kizuna-eye/data/keys/chain.key"
    },
```

- これを忘れると鍵を見つけられず **鍵なし運用（SHA-256）に静かに縮退**する。
- プラグイン自身のログ・ベースライン（`./logs/kizuna-security-*.json`）は cwd 相対のまま（`logs/` は 700）。
  共有外へ移す場合は `log_path` / `integrity_baseline_path` / `alert_history_path` / `suid_baseline_path`
  等も同じ書式で指定する。ただし `alert_history_path` はダッシュボード側の設定と**同じパス**にすること。

### 2-4 ロールバック

```bash
# 1) 元の設定を共有へ戻す
mkdir -p /tmp/kz-restore && tar -xzf ~/.kizuna-eye/originals-backup-20261003.tar.gz -C /tmp/kz-restore
cp -a /tmp/kz-restore/<展開先>/. /samba/share/Kizuna-Eye/     # 展開先は tar の内容に合わせる

# 2) 再起動（自動判定が ./ に戻るため、パッチはそのままでよい）
cd /samba/share/Kizuna-Eye && ./stop.sh && ./start.sh

# 3) 返却確認
ls -l dashboard_config.json users.json sessions.json modules.json keys/chain.key
```

注意: `users.json` / `sessions.json` を共有に戻すと、SMB 経由で bcrypt ハッシュとセッション
トークンが読める状態に戻る（＝移行前のリスクに戻る）。復旧用途の一時措置にとどめること。

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
#     移行後は鍵が共有外にあるため sudo 不要。retired も共有外へ置く。
KEY="$HOME/.kizuna-eye/data/keys/chain.key"
install -d -m 700 "$HOME/.kizuna-eye/keys/retired"
cp -p "$KEY" "$HOME/.kizuna-eye/keys/retired/chain.key.$(date +%Y%m%d_%H%M%S)"
ls -l "$HOME/.kizuna-eye/keys/retired/"        # 旧鍵の控え（600）
sha256sum "$KEY" > "$HOME/.kizuna-eye/keys/OLD-key.sha256"

# 3-2 新鍵（32B）を発行
umask 077; head -c 32 /dev/urandom > "$KEY"; chmod 600 "$KEY"
ls -l "$KEY"                                   # 32 バイト・600
sha256sum "$KEY" > "$HOME/.kizuna-eye/keys/NEW-key.sha256"

# 3-3 起動（旧ログは自動で .legacy-<ts> へ退避、FIM は critical 1 件 → 再ベースライン）
./start.sh
```

確認:

```bash
# ログ側: 旧チェーンと行数アンカーが対で .legacy-<ts> へ退避される（§3-5）
ls -l logs/ | grep legacy
# FIM: 想定どおり critical 1 件 → 自動で再ベースライン
grep -E 'ベースライン署名の検証に失敗' logs/agent.log | tail -1
grep -c 'log_chain_broken' logs/plugin.log logs/agent.log 2>/dev/null   # 0 が正常
# 誤検知が続かないこと（旧実装は「line count decreased」を毎チェック出していた）
grep -c 'truncated or rolled back' logs/kizuna-security.log             # 0 が正常
# 数分後（integrity_check_interval_sec 既定 300）に追加の critical が無いこと
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

**注意（解消済み）**: この 17:52 のローテーション時点では、新鍵はまだ**共有上の
`keys/chain.key`（ゲスト書込可）**にあった。§1（SMB 是正）と §2（秘密移行）の完了後、
鍵が共有外（`~/.kizuna-eye/data/keys/`）へ移ったことを確認して**もう一度 §3 を実施**し、
「共有上で生成された鍵」を排除した（実施記録は §3-6）。

### 3-5 既知の落とし穴: 退避後の行数アンカー（2026-10-03 修正済み）

`checkLogMonotonic`（`plugin/integrity.go`）はセキュリティログの**行数の最大値**を
`logs/kizuna-security-logstate.json` に記録し、減少を critical として検知する（末尾削除・
巻き戻し対策）。退避ローテーションは新チェーンを数行から始めるため、アンカーを残したままだと
**新チェーンが旧行数を超えるまで、毎回の整合性チェックで critical が出続ける**（実測: 4 分間隔）。

- 2026-10-03 の鍵ローテーションで実際に発生（17:53:13 と 17:57:11 の 2 件、anchor 6738 行）。
- 修正: `NewFileLoggerKeyed` の退避時に、アンカーも**同じ時刻の接尾辞で対で退避**し、
  新ファイルは次回チェックで再アンカーする（`archiveChainLogState`）。
  退避イベント `chain_quarantined` には `state_archive` / `state_archive_error` を付与。
  テスト: `TestQuarantineResetsLineCountAnchor`。
- **修正前のプラグインが動いている間**に手動でローテーションした場合は、次のどちらかで止める:
  `mv logs/kizuna-security-logstate.json logs/kizuna-security-logstate.json.legacy-<ts>`（推奨）または
  `rm logs/kizuna-security-logstate.json`（次回チェックで `{"lines":<現在行数>}` を再作成）。

### 3-6 実施記録（移行後の再ローテーション: 2026-10-03 18:03）

| 項目 | 値 |
|---|---|
| 旧鍵 sha256 | `21def4e5230c901f02b8c458b24dbaf3acecbe84526b1439d9bb8bfed3ecf8f7`（17:52 に共有上で生成した鍵） |
| 新鍵 sha256 | `99977bba9997eeb64089bae6644fbbb8e1d75f4d25a7bdc0003511967d5e2f6f` |
| 旧鍵の退避先 | `~/.kizuna-eye/keys/retired/chain.key.20261003_180305`（600・32B） |
| 記録 | `~/.kizuna-eye/keys/OLD-key.sha256` / `NEW-key.sha256`（600） |
| 旧チェーン | `logs/kizuna-security.log.legacy-20261003_180307`（10,625 B、17:56〜18:03 区間） |
| 行数アンカー | `logs/kizuna-security-logstate.json.legacy-20261003_180307`（`{"lines":22}`、**修正版が自動退避**） |
| FIM | 想定どおり critical 1 件（`FIM: ベースライン署名の検証に失敗`）→ 自動で再ベースライン |
| 誤検知 | `truncated or rolled back` **0 件**（§3-5 の修正が本番で有効なことを実証） |
| 配備 | 修正版 `kizuna_security.so`（9,122,856 B, 18:03）を `/opt/kizuna-eye/bin/plugins/` へ配備 |
| 退避前の .so | `/tmp/kizuna_security.so.prefix-20261003_180300`（9,117,576 B、切り戻し用） |

手順（実際に使ったコマンド列）:

```bash
cd /samba/share/Kizuna-Eye
cp -p /opt/kizuna-eye/bin/plugins/kizuna_security.so /tmp/kizuna_security.so.prefix-$(date +%Y%m%d_%H%M%S)
./stop.sh
(cd plugins/Kizuna-Security/plugin && bash build.sh)   # ビルド + /opt/.../plugins へ配備
KEY="$HOME/.kizuna-eye/data/keys/chain.key"
install -d -m 700 "$HOME/.kizuna-eye/keys/retired"
cp -p "$KEY" "$HOME/.kizuna-eye/keys/retired/chain.key.$(date +%Y%m%d_%H%M%S)"
sha256sum "$KEY" > "$HOME/.kizuna-eye/keys/OLD-key.sha256"
umask 077; head -c 32 /dev/urandom > "$KEY"; chmod 600 "$KEY"
sha256sum "$KEY" > "$HOME/.kizuna-eye/keys/NEW-key.sha256"
./start.sh
```

---

## 5. 誤検知の修正記録（2026-10-03 18:14 配備）

| 事象 | 条件 | 対応 |
|---|---|---|
| `アラート履歴 ... file modified in place (same line count, different hash)` の critical | ダッシュボードの履歴圧縮（`pkg/alert` `NewHistory(200)`）が同一 4 分窓で「追記 + 切り詰め」を行い、行数が変わらないとき（実測: 03:14〜18:03 の 14.5 時間で 1 件。アラート多発時は増える） | **修正済み**: `checkAlertHistory` が最終（最後の非空）行の sha256 を状態ファイル（`last_hash`）に保存し、行数が同じでハッシュが変わった場合は「最終行が進んでいれば正規の圧縮 = warning / 最終行が変わらなければ改ざん = critical」と判定する。旧形式の状態ファイルからの移行直後は基準が無いため warning に留める（誤報回避）。テスト: `TestCheckAlertHistoryCompactionIsWarning`（圧縮 = warning / 中間行改ざん = critical / 旧形式 = warning の 3 ケース） |
| `ログ ... line count decreased` の連発（退避後） | 退避時にログだけが退避され、行数アンカーが残っていた | **修正済み（§3-5）**: アンカーも `.legacy-<ts>` 付きで同時退避。テスト: `TestQuarantineResetsLineCountAnchor` |
| `i18n 整合性` の `level.` 未定義 | 抽出器が `t('level.' + x)` の連結断片をキーと誤認 | **修正済み**: `scripts/check_i18n.js` はキー引用符の直後が `)`/`,` のときのみ採用（動的キーは除外） |

`chain_quarantined`（退避）と FIM 署名不一致は**鍵ローテーション時の想定内**で、それぞれ 1 件ずつ出る
（§3-3）。なお `TCP ポート 8080 が新たに待ち受けを開始しました` はダッシュボード再起動に伴う
1 回限りの warning（正常）。

---

## 6. 参考: 残タスク優先順位 #1（FIM デッドロック / F-1）の確認（2026-10-03）

`docs/session_security_audit_20261003.md` §4 #1 は**対応済み**を確認した。

- `plugins/Kizuna-Security/plugin/fim.go`: `langMu` が `mu` から分離されている
  （`Check()` は `mu` のみ、`lang()` / `SetLang()` は `langMu`）→ 通知経路の `lang()` 取得が
  監視ループを待たせない（56-60 / 142-143 / 323-335 行）。
- 配備版 `kizuna_security.so` は 2026-10-03 18:14 に本リポジトリのソースから再ビルド済み
  （= 配備版にも適用されている）。
- 検証: `verify.sh --smoke` PASS=12 / FAIL=0、プラグイン `go test ./...` ok（デッドロック再現テストを含む）。


---

## 4. 実施順序と注意

1. §2（秘密移行）→ **実施済み（17:56）**。共有外 `~/.kizuna-eye/data` へ。原本は削除し
   `~/.kizuna-eye/originals-backup-20261003.tar.gz` に退避済み。
2. §3（鍵ローテーション）→ **実施済み（17:52 → 移行後に再実施）**。移行後は
   `~/.kizuna-eye/data/keys/chain.key` を対象にする。
3. §1（smb.conf）→ **未実施（要 sudo）**。§2 で秘密を共有外へ出したため、**露出していた
   users.json / sessions.json / chain.key は既に守られている**。§1 は残るリスク（共有上の
   `web/`（格納型 XSS）・`scripts/`・`start.sh`・`modules.json` への書き込み）を塞ぐために行う。
4. 共有の書き込み可否は `web/`（格納型 XSS）・`scripts/`・`start.sh`・`modules.json` の改ざんに直結する。
   §1 の `create mask/directory mask` だけでは**既存ファイルは締まらない**ため、§1 の補足の
   `chmod -R go-rwx` か、ファイル所有を別 uid/root にする運用（監査の根本対策）を検討する。
5. `install.sh` の sudoers 設定（`/etc/sudoers.d/kizuna-security-cron`）が指す実体は
   `/usr/local/bin/kizuna-cron-read.sh`（root:root 0755, 共有外）で、共有上の原本
   `scripts/kizuna-cron-read.sh` は install 時にのみ読まれる。共有への書き込みを禁じれば
   「次回 install 時に差し替え」という経路も塞がる（§1）。
