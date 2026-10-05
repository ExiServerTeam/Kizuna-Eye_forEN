
---

# Kizuna-Eye プロジェクト 作業記録 第24部

**セッション日付**: 2026年10月3日（金）続き16
**作業内容**: 段階1（出所タグ＋重み付き保持）、段階2（V5偽装検知）、UI出所バッジ

---

## 第125章: 動機

アラート履歴は最大200件で古い順に無差別削除。偽アラートを大量注入されると本物が押し出されて消える（V5と組合せると証跡隠滅）。これを改善する。

---

## 第126章: 段階1（出所タグ＋重み付き保持・案A）

### 126-1. history.go

- HistoryEntry に Source(string) / Trusted(bool) を追加。
- levelWeight: critical=3, warning=2, info=1, success=1。
- trimLocked: 200件超過時、(trusted, 重み) の低いものから、次に古いものから削除。trusted=false かつ低レベルの偽アラートが先に消え、本物の critical が残る。
- AddSourced(a, source, trusted) を追加（Add は後方互換）。
- compactLocked/Load も同じ trimLocked を使用。

### 126-2. engine.go

- ReportAlert(a, source, trusted) / dispatch(a, source, trusted) にシグネチャ変更。
- エンジン自身のメトリクスアラート（pending loop、agent切断/未接続）は "dashboard", true。

### 126-3. cmd/dashboard/main.go（5箇所）

| Type | source | trusted |
|---|---|---|
| security_*（Agent経由・ログ由来） | agent | false |
| connection_flood | dashboard | true |
| agent_token_mismatch / agent_duplicate | dashboard | true |
| audit_*（プラグイン操作） | dashboard | true |
| auth_*（ログインロックアウト等） | dashboard | true |

### 126-4. UI（app.js + style.css + i18n.js）

- renderAlerts に出所バッジ。信頼(dashboard)=緑 / 未検証(agent)=黄。
- i18n: alerts.source_trusted / alerts.source_untrusted（ja/en）。

### 126-5. 動作確認

alert_history.jsonl に source フィールドが記録（security_sudo は source:agent）。テスト全 ok。

---

## 第127章: 段階2（V5: journald照合による偽装検知）

### 127-1. 方式

- journalctl -o json -f -n 0 を tail。
- auth facility(4/10) かつ SYSLOG_IDENTIFIER=sshd かつ（_COMM!=sshd または _UID!=0）を偽装として spoofCache に保持（直近100件/5分TTL）。
- classify で auth.log の行が spoofCache に部分一致したら spoofed_log(critical) を発行。
- journalctl 未検出なら無効化してフォールバック。

### 127-2. 実装中のトラブル（2件、いずれも修正済み）

1. **nil Context で Agent クラッシュ**: Configure は Init より先で p.ctx が nil。exec.CommandContext(nil) が panic。→ startSpoofWatch に nil ガード＋Configure では context.Background() を渡す。
2. **sudo の誤検知**: 「auth facility かつ _COMM!=sshd」を全部拾い、正規の sudo を偽装と誤判定。→ SYSLOG_IDENTIFIER=sshd を名乗るものだけに限定。

### 127-3. 結果（成功）

logger -p auth.info -t sshd で偽装行を注入:
{"event":"spoofed_log","level":"CRITICAL","message":"... 偽装された可能性のある行 ...: ... sshd: Accepted password for v5final ..."}

誤検知（sudo）: 0件。

### 127-4. 限界

_COMM=sshd を偽装できる攻撃者（root化済み等）には効かない。一般ユーザーの logger による偽装を検知するのが目的（V5の主要シナリオ）。

---

## 第128章: テスト結果

- Kizuna-Eye: 全パッケージ ok。
- Kizuna-Security プラグイン: ok。
- JS構文: app.js / i18n.js OK。

---

## 第129章: 既知の課題（更新）

| # | 課題 | 優先度 |
|---|---|---|
| 1 | agent 専用ユーザー化（B） | 高 |
| 2 | HMAC鍵管理 | 中 |
| 3 | ログローテーション inode 追跡 | 中 |
| 4 | V2-B warning 文言改善 | 低 |
| 5 | テスト分離の一般化 | 低 |
| 6 | sudoers の引数固定（helper ""） | 低 |
| 7 | V5: journald照合で検知（段階2で実装済）。root化済み攻撃者は対象外 | 中 |
| 8 | /tmp の古いファイル（CD_*.tar.gz 1.47G, dash_fix*）でビルドが失敗。TMPDIR を Samba へ変更して回避 | 中 |

---

以上が、2026年10月3日セッション（続き16）の作業記録です。
段階1・段階2 実装完了、V5偽装検知成功。
