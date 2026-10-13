
---

# Kizuna-Eye プロジェクト 作業記録 第9部（追記）

**セッション日付**: 2026年10月3日（金）続き
**作業内容**: 攻撃フェーズ2（cron変更検知）の検証と、検知不能の改善（B実装）

---

## 第60章: 攻撃フェーズ2（cron変更検知）

### 60-1. 攻撃内容と結果

ユーザー crontab にコメント行のみ追加（実行ジョブなし・副作用ゼロ）。

| 項目 | 結果 |
|---|---|
| 検知 | 検知されず |
| security.log | cron イベントなし |
| alert_history | cron イベントなし |
| 後始末 | crontab -r で削除済み |

### 60-2. 原因: 権限による構造的な検知不能

/var/spool/cron/crontabs は drwx-wx--T（root:crontab）で、Agent の実行ユーザー user はディレクトリを読めない。cronmon.scan() は filepath.WalkDir で走査するが、読めないため無言で何も検知しない。

これは攻撃の見逃しではなく、検知機構の構造的限界（root権限が必要）。

---

## 第61章: B実装（読めないディレクトリの警告）

### 61-1. 方針

読めない監視対象ディレクトリを検知したら、無言で失敗せず警告イベントを発行する。既存の emitFn（Monitor.emit）を再利用するため、kizuna-security.log とアラート履歴の両方に自動記録される。

### 61-2. 変更ファイル

バックアップ（/samba/share/Kizuna-Security/plugin/）:
- cronmon.go.bak-20261003_013850
- messages.go.bak-20261003_013850
- パッチ適用時のバックアップ: cronmon.go.prepatch, messages.go.prepatch

変更1: messages.go — 翻訳キー追加（ja/en）

cron.unreadable.title / cron.unreadable.msg を ja と en の両カタログに追加。

変更2: cronmon.go — 構造体に unreadable map[string]bool を追加、Check() の先頭で c.checkUnreadable() を呼び、checkUnreadable() メソッドを追加。

デッドロック回避: checkUnreadable() は c.mu を取らずに emitFn を呼ぶ。emitFn（Monitor.emit）は monitor の別ミューテックスを取るだけで cronmon へ再入しないため、fim.go のような自己デッドロックは発生しない。

### 61-3. 適用手順

1. バックアップ作成
2. パッチスクリプト scripts/patch_cronmon_unreadable.py を作成
3. ドライラン成功
4. 本番適用 + diff -u で差分確認
5. ビルド: GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o kizuna_security.so .
6. 配備: cp kizuna_security.so /opt/kizuna-eye/bin/plugins/
7. Agent 再起動

### 61-4. 結果: 成功

agent.log に [WARN] Kizuna-Security cron: 監視不能ディレクトリ: /var/spool/cron/crontabs を記録。security.log に event=cron の警告イベントを記録。アラート履歴にも記録。

これにより、cron監視が機能していないことが可視化された。従来は無言で失敗していた。
