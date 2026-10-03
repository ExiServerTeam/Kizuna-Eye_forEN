
---

## 第96章: V2-B（alert_history の軽量整合性チェック）

### 96-1. 背景

alert_history.jsonl は dashboard（pkg/alert/history.go）が書き、ハッシュチェーンを持たない。よってチェーン検証はできず、軽量な整合性チェックを実装。

### 96-2. 実装（integrity.go の checkAlertHistory）

状態ファイル（kizuna-security-alertstate.json）に「行数」と「全体ハッシュ」を保存し毎回比較:
- 行数減少 → warning（compaction か truncation か区別不能）
- 同行数で内容変化 → critical（in-place 改ざん）

### 96-3. 重要な修正（誤検知対応）

初版は「行数減少」を一律 critical にしたため、dashboard の正当な compaction（history.go compactLocked、上限超過時に古い行を削除）を誤検知した（ERROR）。
→ 行数減少は warning、同行数・内容変化は critical にレベル分け。

### 96-4. 結果

- 修正前: ERROR「line count decreased (238 -> 202): truncated」
- 修正後: WARN「possible compaction or truncation」1回のみ、以降安定（warn count=2 で停止）
- in-place 改ざんは critical で検知（前テストで確認）

### 96-5. 既知の限界

warning のタイトルが「アラート履歴の改ざんを検知」で、正当な compaction でも出る（誤解を招く）。「監査ログの縮小を検知」に改めるべき。実害小、文言改善は候補。

---

## 第97章: B（agent 専用ユーザー化）— 記録のみ

agent と攻撃者が同一ユーザー権限の場合、HMAC 鍵も読めるため V1 の完全防御は不可。根本対策は agent の専用ユーザー化、またはログの root 所有＋リモート転送。権限モデル変更は影響大のため後で検討。

---

## 第98章: 既知の課題（更新）

1. ログローテーションの inode 追跡（monitor.go、優先度:中）
2. テスト分離の一般化（優先度:低）
3. agent 専用ユーザー化（B、優先度:高・影響大・要検討）
4. V2-B の warning 文言改善（優先度:低）
5. V3 ヘルパーの管理者設置（未完了）

---

## 第99章: テスト結果

各実装後に go test ./... を実行、すべて ok:
- logger.go 変更後
- config.go/plugin.go/messages.go/integrity.go 変更後
- integrity.go レベル分け修正後

---

以上が、2026年10月3日セッション（続き8）の作業記録です。
V1-A / V2-B / V3 を実装、B（専用ユーザー化）は記録のみ。
