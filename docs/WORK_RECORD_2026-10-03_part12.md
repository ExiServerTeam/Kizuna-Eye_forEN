
---

# Kizuna-Eye プロジェクト 作業記録 第19部

**セッション日付**: 2026年10月3日（金）続き11
**作業内容**: C攻撃（偽Agent接続）と V4 修正

---

## 第106章: C攻撃（偽Agent接続）

### 106-1. 手法

WebSocket クライアントを自前実装（RFC6455、外部依存なし）し、/ws?role=agent へ接続を試行。

### 106-2. 結果

| 攻撃 | 結果 |
|---|---|
| トークンなし | 401 Unauthorized |
| 誤トークン | 401 Unauthorized |
| 正しいトークン | 101 だが「2つ目のAgent接続」として拒否 |
| 偽 security_alert 送信 | 処理されず拒否（偽アラート出ず） |
| 実Agentへの影響 | なし |

### 106-3. 発見した欠陥（V4）

トークン不一致・重複Agentの拒否は dashboard.log に記録されるが、**alert_history にも通知にも出ていなかった**。接続フラッドには SetFloodCallback でアラートがあるのに、偽Agent接続には無い、検知の穴。

---

## 第107章: V4 修正（偽Agent接続のアラート）

### 107-1. 実装（cmd/dashboard/main.go）

- Hub に noteAuthReject(reason) を追加。理由別カウンタ＋window（60秒）で、閾値10回に達したら onAuthReject を1回発火。
- SetAuthRejectCallback を追加。
- token不一致拒否時に hub.noteAuthReject("agent_token_mismatch")。
- 重複Agent拒否時に hub.noteAuthReject("agent_duplicate")。
- main() で SetAuthRejectCallback → engine.ReportAlert（warning）。タイトル: 「Agentトークン不一致（偽装の可能性）」「2つ目のAgent接続（乗っ取りの可能性）」。

### 107-2. 検証

誤トークン10回:
{"type":"agent_token_mismatch","level":"warning","title":"Agentトークン不一致（偽装の可能性）","message":"短時間に 10 回の不正なAgent接続を拒否しました（agent_token_mismatch）。"}

正しいトークン10回:
{"type":"agent_duplicate","level":"warning","title":"2つ目のAgent接続（乗っ取りの可能性）","message":"短時間に 10 回の不正なAgent接続を拒否しました（agent_duplicate）。"}

### 107-3. テスト

go test ./... 全12パッケージ ok。ビルド成功、配備、再起動済み。

---

## 第108章: 既知の課題（更新）

| # | 課題 | 優先度 |
|---|---|---|
| 1 | agent 専用ユーザー化（B） | 高 |
| 2 | HMAC鍵管理 | 中 |
| 3 | ログローテーション inode 追跡 | 中 |
| 4 | V2-B warning 文言改善 | 低 |
| 5 | テスト分離の一般化 | 低 |

（V4 は修正済みのため課題から除外）

---

以上が、2026年10月3日セッション（続き11）の作業記録です。
C攻撃を実施、V4（偽Agent接続のアラート欠如）を発見・修正。
