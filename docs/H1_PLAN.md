# H-1 計画書: agent 専用ユーザー化（権限分離）

> 最終更新: 2026-10-05
> ステータス: ドラフト（レビュー待ち）／実施は別セッション
> 目的: V1（HMAC チェーン）の前提を成立させる。agent を運用ユーザー user (uid=1000) から切り離し、uid=1000 が奪われても鍵・状態・ベースラインを改変できないようにする。
> 命名: 専用ユーザー・グループを kizuna-eye に統一する（ユーザー kizuna-eye、グループ kizuna-eye）。agent の systemd ユニットは **kizuna-eye-agent.service**（既存の kizuna-eye.service は start.sh を呼ぶ旧 system service で別物。名前衝突を避けるため agent 版は kizuna-eye-agent.service とする）。

---

## 1. 現状の権限モデル（2026-10-05 実機調査）

| 対象 | 所有者/モード | 備考 |
|---|---|---|
| agent プロセス | user (uid=1000) | systemd 未使用。start.sh の手動管理 |
| dashboard プロセス | user | 同上 |
| 設定 (data/*.json) | user:user 0600 | agent_config / dashboard_config / modules |
| 鍵 (keys/chain.key, keys/alert_history.key) | user:user 0600 | 攻撃者と同一ユーザーが読める |
| 状態 (FIM/ports/SUID/cron/logins/alertstate) | user:user 0600 | repo logs/ と data に散在 |
| ログ (logs/agent.log 等) | user:user 0600 | repo logs/ |
| systemd ユニット | 未設置（kizuna-agent / kizuna-eye とも not-found） | start.sh 管理 |
| 移行スクリプト | systemd/migrate-agent-user.sh (20KB, 未適用) | 実装済み |
| A-4 unit | systemd/kizuna-eye-agent.service | 実装済み（agent 専用ユーザー版。既存 kizuna-eye.service とは別物） |

問題: 同一ユーザー権限のため、user を奪った攻撃者は chain.key でチェーンを再署名でき、FIM ベースライン・状態ファイルも書き換えられる。V1 の改ざん検知は原理的に無効化される（既知の最大の弱点）。

---

## 2. 目標の権限モデル

| 対象 | 目標 所有者/モード |
|---|---|
| agent プロセス | kizuna-eye (system, nologin) で systemd 管理（ユニット kizuna-eye-agent.service） |
| dashboard プロセス | user (従来どおり手動管理) |
| 状態・chain.key | /var/lib/kizuna-eye/{state,keys} 0700 kizuna-eye:kizuna-eye |
| 検知ログ | /var/lib/kizuna-eye/logs 0750 kizuna-eye:kizuna-eye (既定) |
| 共有設定 | data/*.json 0640 user:kizuna-eye |
| 共有ログ | repo logs/ 3770 kizuna-eye setgid+sticky |
| 共有グループ | kizuna-eye (専用ユーザー kizuna-eye + 運用ユーザー user) |
| ケーパビリティ | CAP_DAC_READ_SEARCH (/etc/shadow 等の読取。agent は非 root) |

設計の要点（既存スクリプトが実装済み、命名のみ変更）:
- 状態・chain.key は agent 専用領域へ移設。運用ユーザーからは読み書き不可。
- 設定は共有（dashboard が保存する唯一の経路。group read 0640）。
- ログは共有（group read で運用者が閲覧）。

---

## 3. 影響範囲（命名統一 kizuna-eye）

- migrate-agent-user.sh: NEW_USER を kizuna-eye へ。unit 生成名を kizuna-eye-agent.service へ。
- systemd/kizuna-agent-a4.service 相当を systemd/kizuna-eye-agent.service として作成。User= / Group= を kizuna-eye へ。
- 導入先 /etc/systemd/system/kizuna-eye-agent.service へ。
- 運用コマンド: systemctl status/enable/disable kizuna-eye、journalctl -u kizuna-eye。
- 共有グループ名を kizuna-eye に統一。
- /var/lib/kizuna-eye の所有権を kizuna-eye:kizuna-eye。
- 注: ユーザーとグループが同名 kizuna-eye になる。useradd --system は同名グループも作るため整合する。

---

## 4. 移行手順（既存 migrate-agent-user.sh を使用、定数変更のうえ）

方針: 新規スクリプトは作らない。既存の systemd/migrate-agent-user.sh を使う（NEW_USER / unit 名のみ変更）。

前提:
- root で対話実行 (sudo)。
- A-4 unit systemd/kizuna-eye-agent.service（agent 版）がリポジトリにある。
- 事前に tmp/_a4_prep.sh の phase1〜phase4 を済ませる（実在確認済み: 6447B）。

手順（概要）:
  sudo systemd/migrate-agent-user.sh user

スクリプトが実行する内容（改名後）:
1. バックアップ (/tmp/kizuna-a4-backup-<ts>.tar.gz: data + bin/*.meta.json + repo logs)
2. ユーザー kizuna-eye 作成（system/nologin、同名グループも作成）
3. 状態・chain.key を /var/lib/kizuna-eye/{state,keys} へ移設 (0600, kizuna-eye)
4. modules.json の状態パスを絶対パスへ書換
5. プラグイン .so/.sig を agent から読めるよう 0644 へ
6. 共有グループ kizuna-eye 作成 + 設定を 0640 + data を setgid 2710
7. ログ権限 (repo logs 3770, 検知ログ移設)
8. unit を /etc/systemd/system/kizuna-eye-agent.service に導入
9. 旧 agent 停止（手動起動の残骸も root で停止）
10. systemctl enable --now kizuna-eye

ログ方針の選択:
- 既定: 検知ログを /var/lib/kizuna-eye/logs（保護）へ。
- UI ログ一覧に残す場合: sudo A4_SECURITY_LOG_DIR=/samba/share/Kizuna-Eye/logs systemd/migrate-agent-user.sh user。
  （この場合アンカーを運用ユーザーが消せ、原理的に隠蔽可能になる点に注意）

移行後（ダッシュボードの再起動が必要）:
  sudo -u user /samba/share/Kizuna-Eye/stop.sh dashboard
  sudo -u user /samba/share/Kizuna-Eye/start.sh dashboard
agent は systemd 管理。start.sh/stop.sh は agent を触らない。

---

## 5. ロールバック手順

スクリプトが表示するバックアップ tar (/tmp/kizuna-a4-backup-<ts>.tar.gz) を使う。
  sudo systemctl disable --now kizuna-eye
  sudo tar xzf /tmp/kizuna-a4-backup-<ts>.tar.gz -C /
  /samba/share/Kizuna-Eye/start.sh
- **所有権の復元（対象パスを明示）**: バックアップ展開後、次の各対象を `user:user` へ戻す。
  - `/home/user/.kizuna-eye/data`（配下の *.json, keys/）
  - `/samba/share/Kizuna-Eye/logs`（agent.log, kizuna-security.log 等）
  - `/samba/share/Kizuna-Eye/bin` は対象外（root 管理）だが、`/opt/kizuna-eye/bin/plugins` の .so/.sig は移行時に 0644 化したため、必要に応じ 0600 へ戻す（dashboard 運用に合わせる）。
  - `/var/lib/kizuna-eye`（state/keys）は agent 専用のため、手動管理へ戻す場合は削除可（バックアップに含まれない）。
  - コマンド例: `sudo chown -R user:user /home/user/.kizuna-eye/data /samba/share/Kizuna-Eye/logs`
- unit を退避した場合: `/tmp/kizuna-eye-agent.service.bak-<ts>` から復元。
- 旧 agent unit（`/etc/systemd/system/kizuna-agent.service`・`kizuna-eye-agent.service`）が残っていれば削除し `systemctl daemon-reload`。※ 旧 system service `/etc/systemd/system/kizuna-eye.service` は本移行とは別物のため触らない。
- **sudoers の復元**: CR-1 で追加した kizuna-eye 用ルールを削除し `visudo -cf` で検証。
- modules.json を書き換えているため、バックアップから modules.json も戻す（絶対パス化を解除）。

---

## 6. リスクと対策

| # | リスク | 対策 |
|---|---|---|
| R1 | プラグイン全滅: agent が kizuna-eye になると 0600 の .so/.sig を読めず署名検証/dlopen に失敗 (fail-closed で静かに停止) | スクリプト 3.5 が 0644 へ緩める。移行後にログで署名検証OK・登録OKを確認 |
| R2 | dashboard が agent ログを読めない: 所有者が変わる | repo logs を 3770 + group read。dashboard 再起動でグループ反映 |
| R3 | 共有設定が読めない: 保存のたびに group が user に戻る | data を setgid 2710、設定を 0640。fsutil.TightenSharedConfigMode が group read を保持 |
| R4 | 二重 agent: 手動 agent が残ると state を奪い合う | スクリプト 5 が /proc で残骸を検出し停止。残れば中止 |
| R5 | FIM 権限不足: 非 root で /etc/shadow 等が読めない | AmbientCapabilities=CAP_DAC_READ_SEARCH で解消 |
| R6 | ロールバック不能 | バックアップ tar を必ず取得。手順を本文書に明記 |
| R7 | 検知ログの隠蔽: repo logs に残すと運用ユーザーが消せる | 既定は保護ディレクトリ。UI 優先時はリスクを明記 |
| R8 | backup プラグインの書込先 /samba/share/CD | unit の ReadWritePaths に含まれる。移行後にバックアップ実行で確認 |
| R9 | dashboard の agent_config 保存: agent が systemd 管理になると start.sh が触らない | start.sh/stop.sh は systemd 管理時 agent をスキップ（実装済み） |
| R10 | alert_history.key を plugin が読めない（調査事項2） | 鍵を 0640 user:kizuna-eye、keys ディレクトリを 2710（group traverse）に。実装変更1（LoadOrCreateAlertHistoryKey の 0640 化）とセット |
| R11 | cron 監視の停止（CR-1/CR-2） | **解決済み**: 対処(a) 採用により cronmon.go を直接読み取りへ変更（AmbientCapabilities=CAP_DAC_READ_SEARCH）。sudo/sudoers 不要（実装変更5） |
| R12 | systemd の cgroup kill | oneshot/通常 service が nohup 等で子プロセスをデタッチ起動しても、systemd の既定 KillMode=control-group は service 終了時に cgroup 内の全プロセスへ SIGTERM を送る（実機で watchdog が agent を毎分フラップさせた）。対策: `KillMode=none`（または管理を systemd に一本化） |

---

## 7. migrate-agent-user.sh との整合 / レビュー事項の調査結果

### (2) alert_history.key の移行後の扱い【調査完了】

現状:
- 場所: /home/user/.kizuna-eye/data/keys/alert_history.key
- 権限: 0600 user:user（親 data 700、keys 700）
- 書き手: dashboard（実行ユーザー user が LoadOrCreateAlertHistoryKey で作成/署名）
- 読み手: プラグイン（agent 内。readChainKey で読む。modules.json の alert_history_key_path）

問題:
- 移行後、プラグインは kizuna-eye として動く。現状の 0700 data / 0700 keys / 0600 key では、kizuna-eye から読めない。
- 読めない場合、プラグイン側は len(key)==0 のゲートで「検証スキップ」となり、誤検知は出ないが保護も無効（サイレント）。

根本的な制約（重要）:
- chain.key は「書き手=読み手=agent(kizuna-eye)」なので kizuna-eye 専用にできる。
- しかし alert_history.key の書き手は dashboard（user）である。署名鍵は書き手が持つ必要があり、user が奪われたら鍵も読める。
- したがって alert_history の HMAC は、chain.key と違い「user 権限奪取」に対しては完全な防御にならない（他ユーザー/オフライン改ざん等には有効な多層防御）。

推奨（採用案）:
- 鍵の場所は変更しない（/home/user/.kizuna-eye/data/keys/alert_history.key）。dashboard（user）が書き続けられるため。
- ただし kizuna-eye グループに読み取りを与える: keys ディレクトリを chgrp kizuna-eye + 2710（group traverse）、鍵を 0640 user:kizuna-eye。
- 実装変更が必要: LoadOrCreateAlertHistoryKey は 0600 で作成する。作成モードを 0640 にする、または作成後に chmod する（group read の保持）。fsutil の共有モードヘルパー流用が候補。
- migrate-agent-user.sh にも keys ディレクトリ/鍵の権限調整を追記する。
- 代替案（非推奨）: /var/lib/kizuna-eye/keys へ移すと、dashboard（user）が書けず、ダッシュボード分離が必要になる。今回は採らない。

### (3) 移行スクリプトの modules.json 書換と層4キーの整合【調査完了】

現状:
- migrate-agent-user.sh は python で modules.json の次を書換: integrity_baseline_path / ssh_login_baseline_path / listen_port_baseline_path / suid_baseline_path / suid_fast_baseline_path / cron_baseline_path / block_state_path / alert_history_state_path / chain_key_path / log_path / alert_history_path。
- alert_history_key_path は書換対象に含まれない。

判断:
- alert_history_key_path の場所を変えない（推奨案）ため、modules.json の値は現行のままで正しい。書換は不要。
- ただし権限（keys ディレクトリと鍵）の調整を migrate-agent-user.sh に追記する必要がある（上記(2)）。
- 将来パスを変える場合に備え、スクリプトの baselines 書換に alert_history_key_path を追加する箇所を明記しておく（コメント）。

### (4) systemd 未設置からの A-4 前提の満たし方【解決済み】
- H-1 リハーサル（2026-10-05）で systemd 導入と prep phase1〜4 を実施し、移行に成功した。手順は第10章に記録。

### (5) 命名統一に伴うスクリプト/unit 修正
- migrate-agent-user.sh: NEW_USER, UNIT 生成名, sudoers/所有権の対象を kizuna-eye へ。
- kizuna-agent-a4.service → kizuna-eye-agent.service（User=/Group=kizuna-eye）。
- 運用コマンド・journalctl・ロールバックの unit 名を kizuna-eye へ。

### (6) 移植性のための引数化
- ユーザー名・パス（リポジトリ/データ/バックアップ先/unit 名）を環境変数または引数で上書き可能にし、他環境へそのまま移植できるようにする（OSS公開時にも有利）。

---

## 8. テスト項目（2026-10-05 H-1 実施後の結果）

- [x] systemctl status kizuna-eye-agent が active（active (running)）。
- [x] journalctl -u kizuna-eye-agent に起動ログ。
- [x] agent.log に両プラグインの署名検証OK・登録OK。
- [x] version mismatch が 0 件。
- [x] /api/status が更新される (health=healthy)。
- [x] FIM の権限不足 INFO が消える (CAP_DAC_READ_SEARCH) — 0件。
- [x] 状態ファイルが /var/lib/kizuna-eye/state (0700 kizuna-eye) に移設。
- [x] chain.key が /var/lib/kizuna-eye/keys (0600 kizuna-eye)。
- [x] alert_history.key が kizuna-eye から読める（0640 user:kizuna-eye、keys 2710）。
- [x] 共有設定が 0640 user:kizuna-eye。
- [x] 検知ログが group read で読める（sudo 不要。移設先 /var/lib/kizuna-eye/logs）。
- [x] alert_history の HMAC 署名が「検証成功」（署名不一致0件）。
- [x] cron 監視が動作: 直接読み取り経路（CAP_DAC_READ_SEARCH）で crontab 変更を検知（sudo 不要）。
- [ ] backup プラグインが /samba/share/CD へ書ける（未確認。unit の ReadWritePaths には含まれる）。
- [ ] Discord 通知が届く（未確認）。
- [ ] ダッシュボードから設定変更 → agent に反映 (mtime 監視)（未確認）。
- [x] sudoers 依存が無い（対処(a) で sudo 不使用。CR-1 は不要）。
- [x] NoNewPrivileges と sudo の整合: NoNewPrivileges=yes を維持（sudo を使わないため矛盾なし）。
- [ ] ロールバック手順を（テスト環境で）リハーサル（**未実施・任意**）。

### 実施後に追加で見つけて直した点（第10章参照）

- agent_linux の実行権限（203/EXEC）→ migrate §3.5 修正。
- agent.log の log_file 絶対パス化 → migrate §3.2b 修正。
- unit の StartLimit セクション → [Unit] へ移動。

---

## 8.5 A-4前提条件の充足状況（2026-10-05 調査）

### (1) tmp/_a4_prep.sh phase1〜4 の目的

- phase0: 現状診断（id / バイナリ / 鍵 / 設定 / systemd / プロセスを読み取り表示）。
- phase1: ビルドして /opt/kizuna-eye/bin のバイナリを差し替え（rename で稼働中でも安全。.prev-* に退避）。
- phase2: 署名鍵（plugin_signing）を生成（既存は上書きしない）。
- phase3: 配置済み .so へ署名（sign-all）+ 検証。
- phase4: 設定（dashboard_config / agent_config）へ plugins ポリシーを反映（require_signature=true、public_key_file=/etc/kizuna-eye/plugin_signing.pub、plugins_dir=/opt/kizuna-eye/bin/plugins）。既存の権限/所有者は引き継ぐ。

### (2) systemd 設置状況

- /etc/systemd/system/kizuna* は**存在しない**（未設置）。
- kizuna 関連の unit ファイルも**無し**。現状は start.sh の手動管理。

### (3) kizuna-eye ユーザー/グループ

- kizuna-eye グループ: **無し**。
- kizuna-eye ユーザー: **無し**。
- kizuna-agent グループ/ユーザーも**無し**（A-4 未適用）。

### (4) /var/lib/kizuna-eye

- **存在しない**（A-4 移行未実施）。

### (5) alert_history.key の権限

- 0600 user:user（/home/user/.kizuna-eye/data/keys/alert_history.key）。

### (6) modules.json の alert_history_key_path

- /home/user/.kizuna-eye/data/keys/alert_history.key（現行のまま）。

### (6.5) pubkey パスの不整合（実在・2026-10-05 調査）

| 項目 | 値 |
|---|---|
| /etc/kizuna-eye/ ディレクトリ | **存在しない** |
| /etc/kizuna-eye/plugin_signing.pub | **存在しない** |
| /home/user/.kizuna-eye/keys/plugin_signing/plugin_signing.pub | 存在（45B, 0644, sha256=bd18fc405a6935b15488a2136c977cd78c6386f7e99b78b5a5938b4eb01568de） |
| dashboard_config.json plugins.public_key_file | /home/user/.kizuna-eye/keys/plugin_signing/plugin_signing.pub |
| agent_config.json plugins.public_key_file | /home/user/.kizuna-eye/keys/plugin_signing/plugin_signing.pub |
| _a4_prep.sh phase4 が設定する値 | /etc/kizuna-eye/plugin_signing.pub |

判定: **不整合が実在する**。phase4 の想定（/etc/kizuna-eye/）に対し、実設定は home を指したまま。かつ /etc/kizuna-eye/ は未作成のため、**phase4 は未適用**（require_signature=true は別経路で設定されたか、phase4 後に戻された）。

A-4 への影響: migrate-agent-user.sh は公開鍵を /etc/kizuna-eye/plugin_signing.pub に配る（root）。agent が kizuna-eye になると home（0700）配下は読めないため、**A-4 実施時は公開鍵を /etc/kizuna-eye/ に配置し、設定をそのパスに合わせる必要がある**。

### (7) prep 実行履歴

- tmp/ に **.log ファイルは無い**（実行履歴ログなし）。
- tmp/_a4_prep.sh 本体は存在（6447B）。A-4 関連の補助スクリプト群も存在。
- 観測: 現行 dashboard_config.json の plugins.require_signature は **true**（署名必須は有効）。ただし public_key_file は /home/user/.kizuna-eye/keys/plugin_signing/plugin_signing.pub を指し、phase4 が設定する /etc/kizuna-eye/plugin_signing.pub とは異なる。phase4 の完全適用とは断定できない（要確認）。

### 充足状況まとめ

| 前提条件 | 状態（H-1 実施前） | 現在（2026-10-05 H-1 後） |
|---|---|---|
| _a4_prep.sh（phase1〜4）実在 | 充足（6447B） | 充足 |
| systemd unit 設置 | 未充足 | **充足**（kizuna-eye-agent.service） |
| kizuna-eye ユーザー/グループ | 未充足 | **充足**（uid=994） |
| /var/lib/kizuna-eye | 未充足 | **充足**（state/keys/logs） |
| alert_history.key の group read | 未充足（0600 user:user） | **充足**（0640 user:kizuna-eye） |
| 署名ポリシー（require_signature） | 一部充足（true） | 充足（pubkey=/etc/kizuna-eye/） |
| pubkey パス統一（/etc/kizuna-eye/） | 未充足 | **充足**（配置済み・phase4 適用済み） |

→ **A-4 は適用済み（2026-10-05）**。詳細は第10章。

---

## 8.6 実装変更の優先順位

1. **LoadOrCreateAlertHistoryKey の作成モード 0640 化**（H-1 実施前に必須）
   - 現状 0600 で鍵を作成するため、移行後に kizuna-eye が読めない。0640（group read）へ。fsutil の共有モードヘルパー流用が候補。
   - 併せて keys ディレクトリの group traverse（2710）と鍵 0640 を migrate-agent-user.sh に追記。
2. **migrate-agent-user.sh の kizuna-eye 改名＋権限追記**（H-1 実施時）
   - NEW_USER / unit 名 / 所有権対象を kizuna-eye へ。keys/alert_history.key の権限調整を追記。
3. **agent unit を kizuna-eye-agent.service として導入**（H-1 実施時）
   - User=/Group= を kizuna-eye へ。導入先 /etc/systemd/system/kizuna-eye-agent.service。
4. **pubkey パスの統一（/etc/kizuna-eye/plugin_signing.pub）**（H-1 実施前〜実施時）
   - 推奨（対処1）: 公開鍵を /etc/kizuna-eye/ に配置したうえで _a4_prep.sh phase4 を再実行し、dashboard/agent 両設定の public_key_file を /etc/kizuna-eye/plugin_signing.pub に統一。
   - 順序厳守: **鍵の配置 → 設定変更**（逆順だと設定が存在しない鍵を指し、プラグインのロードが fail-closed で失敗する）。
   - 対処2: 既存公開鍵を /etc/kizuna-eye/ にコピーし、設定を手動変更（phase4 を使わない）。
   - 対処3: phase4 の想定を /home/user/ に変更し _a4_prep.sh を修正（非推奨: A-4 後に agent は home を読めない）。
   - 推奨理由: OSS 公開時のドキュメントとしてシンプル（root 管理の /etc/kizuna-eye/ に統一）。
5. **cronmon.go を sudo ヘルパーから直接読み取りへ変更**（H-1 実施時。対処(a) 採用）
   - AmbientCapabilities=CAP_DAC_READ_SEARCH により、非 root(kizuna-eye) が crontabs を直接読める（検証済み）。
   - sudo/sudoers 依存を撤去。ケーパビリティ欠如時のフォールバック警告は維持。
   - これにより CR-1（sudoers 追加）は不要、CR-2（NoNewPrivileges 調整）も不要（unit は AmbientCapabilities 済み）。

---

## 8.7 最終レビュー結果（2026-10-05）

### 重大（実装前に必ず解決）

**[CR-1] sudoers のギャップ（抜け漏れ）**
- プラグインの cron 監視は `sudo -n /usr/local/bin/kizuna-cron-read.sh` を実行する（cronmon.go）。
- 現行 sudoers ルールは **`user` 用**（`user ALL=(root) NOPASSWD: ...kizuna-cron-read.sh ""`）。
- migrate-agent-user.sh は **sudoers を一切変更しない**（grep 確認: no sudoers handling）。
- よって agent が `kizuna-eye` になると、sudoers に kizuna-eye 用の許可が無く、**cron 監視が機能しない**（sudo -n が失敗）。
- 確認: `/var/spool/cron/crontabs` は `drwx-wx--T root:crontab` で非 root は列挙不可 → sudo ヘルパーが必須。`/etc/crontab`(644)・`/etc/cron.d`(755) は直接読める。
- 修正案: migrate-agent-user.sh に「sudoers へ kizuna-eye 用の NOPASSWD ルールを追加」するステップを追加。既存 `/etc/sudoers.d/kizuna-security-cron` の書式に合わせ、次の行を kizuna-eye 用に追加する:
  `kizuna-eye ALL=(root) NOPASSWD: /usr/local/bin/kizuna-cron-read.sh ""`
  追加後 `visudo -cf` で検証。ロールバックで該当行を削除。

**[CR-2] NoNewPrivileges=yes と sudo の矛盾**
- A-4 unit は `NoNewPrivileges=yes`。
- しかし cron 監視は `sudo`（setuid root バイナリ）で権限を得る設計。NoNewPrivileges=yes は setuid による権限昇格を禁止するため、**sudo が失敗し cron 監視が壊れる**可能性が高い。
- 修正案（いずれか）:
  - (a) cron 監視を sudo 依存から外す（CAP_DAC_READ_SEARCH があるので、cron ファイルを直接読めるか再検討）。
  - (b) unit の NoNewPrivileges=yes を外す（sudo を許可）。ただしセキュリティは下がる。
  - (c) cron 監視だけ別ユニット/ヘルパーに分離。
  - → **要検証**: 実機で「kizuna-eye として sudo -n cronSudoHelper が通るか」を A-4 前に確認する。
  - 環境注記: 本機の sudo は sudo-rs 0.2.13（Ubuntu の Rust 実装）。従来 sudo と NoNewPrivileges の相互作用が異なる可能性があるため、実機検証が必須。systemd は 259。

#### CR-2 実機検証手順（選択肢B: kizuna-eye 先行作成）

前提: root で実行。読み取り＋sudo -n 実行のみ。停止・再起動なし。

手順:

1. ユーザー/グループ作成（agent はまだ user のまま。影響なし）
   groupadd --system kizuna-eye
   useradd -r -g kizuna-eye -d /var/lib/kizuna-eye -s /usr/sbin/nologin kizuna-eye

2. sudoers 追加（別ファイル。visudo -cf で検証）
   sudo visudo -f /etc/sudoers.d/kizuna-cron-verify
   次の1行を書いて保存:
     kizuna-eye ALL=(root) NOPASSWD: /usr/local/bin/kizuna-cron-read.sh ""
   sudo visudo -cf /etc/sudoers.d/kizuna-cron-verify

3. sudo -n 検証（パスワードキャッシュに注意。クリーンな sudo -n）
   sudo -u kizuna-eye sudo -n /usr/local/bin/kizuna-cron-read.sh         # 成功(exit 0)
   sudo -u kizuna-eye sudo -n /usr/local/bin/kizuna-cron-read.sh foo     # 拒否(non-zero)

4. NoNewPrivileges=yes 下での検証（A-4 unit と同条件を systemd-run で再現）
   sudo systemd-run --uid=kizuna-eye --gid=kizuna-eye --property=NoNewPrivileges=yes --property=User=kizuna-eye --property=Group=kizuna-eye --wait --pipe /usr/local/bin/kizuna-cron-read.sh
   → 成功: helper は root 所有 0755 で実行可能。

4b. A-4 unit と同条件で sudo 経由を検証
   sudo systemd-run --uid=kizuna-eye --gid=kizuna-eye --property=NoNewPrivileges=yes --property=User=kizuna-eye --property=Group=kizuna-eye --wait --pipe sudo -n /usr/local/bin/kizuna-cron-read.sh
   → 成功: 対処(a)寄り(NoNewPrivileges下でもsudo可)。失敗: 対処(b)か(c)が必要。

5. 後片付け（検証後）
   sudo rm -f /etc/sudoers.d/kizuna-cron-verify
   sudo userdel kizuna-eye; sudo groupdel kizuna-eye

判定（2026-10-05 実機検証結果）:
- 手順5: 引数なし=exit 0（成功）、引数付き=exit 1（拒否）。✅ CR-1 の sudoers 行は有効。
- 手順4: helper 直接実行=exit 0（成功）。NoNewPrivileges=yes でも helper 自体は実行可。
- 手順4b: sudo -n 経由=exit 1（失敗）。理由: 「The no new privileges flag is set, which prevents sudo from running as root」。
- **結論: NoNewPrivileges=yes は sudo を確実にブロックする（4b 失敗）。CR-2 は実在する問題と確定。**

採用する対処: **対処(a)（CAP_DAC_READ_SEARCH で直接読み取り）を採用**。sudo 依存と CR-1 の sudoers 追加を不要にする。
（旧検討: 対処(c) 基本／(b) 代替の案は、(a) 成功により採用しない。(a) が最もシンプルでセキュリティも高い。）
- (c) cron 監視を別ユニット/ヘルパーに分離: agent 本体は NoNewPrivileges=yes を維持し、cron 読み取りは NoNewPrivileges を付けない専用の仕組み（root 実行のヘルパー or 別 unit）で行い、結果を agent が読む。セキュリティを落とさず cron 監視を維持できる。
- (b) 代替: unit から NoNewPrivileges=yes を外す。実装は最小だが、agent プロセス全体で setuid 昇格が可能になりセキュリティは下がる。
- 追加の検討候補: CAP_DAC_READ_SEARCH により /var/spool/cron/crontabs を sudo 無しで直接読めるか（対処(a) の再検討）。読めれば sudo 依存を外せる。→ 要検証（別タスク）。
- いずれの場合も CR-1（kizuna-eye 用 sudoers）は必要（crontabs 列挙のため）。
- 注意: 対処(c) を採る場合、sudo は agent 本体（NoNewPrivileges）から呼ばない。cron 読み取り専用の別経路に限定する。
  - 補足: cronmon.go の readCronViaSudo は sudo 失敗時に「blind walk + unreadable warning」へフォールバックする（クラッシュはしない）。ただし crontabs は列挙不可のため実質 cron 監視が劣化する。
  - 状況: 実機検証の結果、NoNewPrivileges=yes で sudo は不可（4b 失敗）。採用は対処(c) 基本／(b) 代替。(a) は CAP_DAC_READ_SEARCH(AmbientCapabilities) で crontabs を直接読めるかを別途検証中（結果により (a) 採用の可否が決まる）。

### 中（整合性の修正）

- **[MD-1] セクション9と8.6の不一致**: 9 は「実装変更3点」だが 8.6 は4点（pubkey 追加）。9 を「4点（＋CR-1/CR-2）」に更新する。
- **[MD-2] ロールバックの所有権復元が曖昧**: 「必要に応じて chown -R user:user」を、対象パス（data/logs/bin/plugins/state）を明示した手順に具体化。
- **[MD-3] R10 の参照**: R10 の対策が「7.(2) 参照」のみ。要点（0640 + keys 2710、実装変更1）を R10 に一行追記。

### 軽（任意）

- **[LT-1] ユーザー/グループ同名**: 既に注記済み（問題なし）。
- **[LT-2] テスト項目**: sudoers/NoNewPrivileges 関連（cron 監視が動くこと）を追加すべき。

### 反映済みの確認

- (2)(3)(4)(6.5) は 7章・8.5 に反映済み。
- 実装変更5点の順序（1・4 は H-1 前〜時、2・3・5 は H-1 時）は 8.6 に明記済み。
- 対処(a) 採用（2026-10-05 検証済み）により、CR-1（sudoers）と CR-2（NoNewPrivileges 調整）は**実装不要**。

---

## 9. 実施判断

- **実施済み（2026-10-05）**: H-1 リハーサルとして本番環境で migrate を実行し成功。結果は第10章。
- ロールバックは未リハーサル（任意・残課題）。バックアップ tar は `/tmp/kizuna-a4-backup-20261005_075713.tar.gz`。
- **実装変更が必要な項目（最終・5点）**:
  1. LoadOrCreateAlertHistoryKey の作成モード 0640 化（**H-1 前必須**）
  2. migrate-agent-user.sh の kizuna-eye 改名＋権限追記（H-1 時。**sudoers 追加は不要**）
  3. agent unit を kizuna-eye-agent.service として導入（H-1 時）
  4. pubkey パス統一（H-1 前〜時）
  5. cronmon.go を sudo ヘルパーから直接読み取りへ変更（H-1 時。対処(a) 採用）
- **解決済み（実装不要）**: CR-1（sudoers 追加。対処(a) で sudo 不要）／CR-2（NoNewPrivileges 調整。unit は AmbientCapabilities 済み）

## 10. H-1 リハーサル結果（2026-10-05 実施）

実施: 実機で migrate-agent-user.sh を実行（成功）。agent は kizuna-eye として systemd 管理下で稼働、health=healthy、プラグイン2つ署名検証OK。

### リハーサルで見つけて修正した3件

1. **agent_linux の実行権限（203/EXEC）** — dashboard が 0700 でビルドした本体バイナリを kizuna-eye が exec できず起動失敗。migrate §3.5 に「agent_linux を chgrp kizuna-eye + 0750」を追加。
2. **agent.log の log_file パス** — `logs/agent.log`（相対）は data/logs に解決され、kizuna-eye から書けない（0700 user + ProtectSystem=strict）。migrate §3.2b に「agent_config.json の log_file を repo logs の絶対パスへ書換」を追加。
3. **unit の StartLimit セクション誤り** — `[Service]` に置くと systemd が無視（Unknown key）。`[Unit]` へ移動。

### 残置・要判断

- **`data/keys/chain.key` が残置**: migrate は `install`（コピー）のため元鍵が `/home/user/.kizuna-eye/data/keys/chain.key`（0600 user:user）に残る。agent は新鍵（`/var/lib/kizuna-eye/keys/chain.key`）を使うため害はないが、衛生上削除を推奨（要許可）。
- **`user` のグループ反映**: migrate の `usermod -aG kizuna-eye user` は次回ログインから有効。現在のセッションでは group read ファイルが直接読めない（sudo か再ログインで解決）。

### テスト項目の結果（判明分）

- systemctl status kizuna-eye-agent: active (running) ✅
- health: healthy ✅
- プラグイン署名検証OK・登録OK ✅
- 状態/chain.key 移設 ✅ /var/lib/kizuna-eye/{state,keys}
- 検知ログ移設 ✅ /var/lib/kizuna-eye/logs
- cron 直接読み取り・CAP_DAC_READ_SEARCH 効果 ✅（SUID/FIM inotify 動作）
- FIM 権限不足 INFO: **0 件** ✅（CAP_DAC_READ_SEARCH の効果で解消。H-1 の主目的の一つ達成）
- alert_history 署名不一致: **0 件** ✅（kizuna-eye が署名鍵を読めて検証が正常）
- chain.key: /var/lib/kizuna-eye/keys（0600 kizuna-eye）✅
- セキュリティイベント送信: 動作中 ✅
- 未確認（任意）: Discord 通知の到達、ロールバックのリハーサル。

### 将来の課題（kizuna-agent.service 削除時）

- **判断（2026-10-05）**: `systemd/kizuna-agent.service` は**当面残置**する。理由: 現在 agent は `kizuna-eye-agent` で systemd 管理中であり、`install-services.sh` を実行する予定がない（未使用の暫定 unit なので実害なし）。
- **削除する条件**: 将来 `install-services.sh` を実行する予定が生じた場合、先に**対処案(3)（agent を install-services.sh から外し dashboard のみ導入）**を実施してから、`kizuna-agent.service` を削除する。
- これを削除する段階で、`systemd/install-services.sh` の修正が必要になる。
  - 現状: `install-services.sh` は「dashboard + `kizuna-agent`（`__USER__` プレースホルダを持ち `user` で動く暫定版）」を導入する作り。
  - 新 unit `kizuna-eye-agent.service` は `User=kizuna-eye` 固定・`kizuna-eye` ユーザー前提のため、参照名を単純置換しても動かない（`__USER__` 置換が効かず、ユーザー未作成だと起動失敗する）。
  - 対処案(2): `install-services.sh` を `kizuna-eye-agent` 対応に作り替え、`kizuna-eye` ユーザーが無ければエラーにする。
  - 対処案(3・推奨): agent を `install-services.sh` から外し、dashboard のみ導入する。agent は `migrate-agent-user.sh` に一本化する（最終形）。
- 実施は破壊的変更（unit 導入経路の変更）のため、着手前にユーザーの許可を取る。
