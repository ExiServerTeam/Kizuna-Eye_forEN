
---

# Kizuna-Eye プロジェクト 作業記録 第15部

**セッション日付**: 2026年10月3日（金）続き7
**作業内容**: 強攻撃フェーズ（検知回避・証跡隠滅）

---

## 第88章: 偵察

| 項目 | 結果 |
|---|---|
| agent の PATH | /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:... |
| security.log 所有権 | user:user 0600（agent と同一ユーザー） |
| alert_history.jsonl 所有権 | user:user 0600 |
| /usr/local/bin 書込 | 不可 |
| sudo secure_path | 不明（sudo -n env 拒否） |
| VerifyChain 呼び出し | logger.go の NewFileLogger のみ（起動時1回） |
| security.log の FIM 監視 | **なし**（integrity_files に含まれない） |

---

## 第89章: 攻撃（ハッシュチェーン改ざん）

### 89-1. 攻撃内容

security.log から攻撃痕跡（ssh_failed の1行）を削除し、残りの行のハッシュチェーンを再計算して書き戻す。

手順:
1. ログをバックアップ
2. ssh_failed 行（kizuna_nouser）を1行削除
3. 残りの行の prev_hash/hash を先頭から再計算（Go json.Marshal 互換）
4. 書き戻し

### 89-2. 結果: 改ざん成功（検知されず）

before: chain valid: True, ssh_failed count: 8
ATTACK: dropped lines: 1 -> chain valid now: True
after:  chain valid: True, ssh_failed count: 7

**改ざん後も VerifyChain は True。攻撃痕跡1件を完全に消去し、未検知。**

### 89-3. 復旧

バックアップから security.log を復元。ssh_failed 8件、チェーン有効に復帰。

---

## 第90章: 発見した脆弱性

### V1（高）: ハッシュチェーンに鍵がない

logger.go の chainHash は `sha256(prev_hash + json)` の**鍵なし**ハッシュ。改ざん権限（agent ユーザー＝ログ所有者）を持つ者は、チェーンを再計算でき、VerifyChain を偽装通過できる。

さらに VerifyChain は**起動時の1回のみ**。稼働中の改ざんは検知されない。

### V2（中）: セキュリティログが自己監視されていない

security.log と alert_history.jsonl は integrity_files（FIM）に含まれない。ログ自体の改ざん・削除を FIM が検知しない。

### V3（要確認）: ヘルパースクリプトの PATH 依存

/usr/local/bin/kizuna-cron-read.sh は basename/cut/sha256sum を絶対パスで呼んでいない。sudo が secure_path を設定していれば影響なし、していなければ PATH 乗っ取りの余地。

---

## 第91章: 修正方針（提案）

### V1 修正

1. チェーンを HMAC-SHA256（鍵付き）に変更。鍵は agent 起動時に生成/読込。
2. VerifyChain を定期実行（例: 5分ごと）し、失敗時に critical 通知。
3. 鍵ファイルは 0600 で保管し、FIM 監視対象に追加。

限界: agent と攻撃者が同一ユーザー権限の場合、鍵も読めるため完全防御は不可。根本対策は agent の専用ユーザー化、またはログの root 所有＋リモート転送。

### V2 修正

security.log / alert_history.jsonl を integrity_files に追加（自己監視）。ただし自身のログを自身が書くため、追記でハッシュが変わる点に注意（FIM は変更検知するので誤報になる）。→ ログ専用の別チェック（行数・ハッシュチェーン定期検証）が適切。

### V3 修正

ヘルパーのコマンドを絶対パス化（/usr/bin/basename, /usr/bin/cut, /usr/bin/sha256sum）。

---

## 第92章: 次回への引き継ぎ

- 上記 V1〜V3 の修正は未実施（方針提示のみ）。
- 作業記録の通り、攻撃は成功。修正は承認後に実施。

---

以上が、2026年10月3日セッション（続き7）の作業記録です。
