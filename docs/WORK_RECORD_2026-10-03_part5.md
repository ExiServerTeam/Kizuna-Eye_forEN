
---

# Kizuna-Eye プロジェクト 作業記録 第12部

**セッション日付**: 2026年10月3日（金）続き4
**作業内容**: フェーズ4（SSHログイン検知）

---

## 第76章: monitor.go 事前点検

### 76-1. デッドロック（fim.go型）: なし

emit() は m.mu.Lock() でキュー追加し即 Unlock()。lang() メソッドは存在せず、m.tr() は m.cfg.Language を参照するだけ（m.mu を取らない）。自己デッドロックなし。

### 76-2. 検知漏れ（cronmon.go型）: 該当なし

monitor.go は行ベースの追記読み取り（tail方式）で sources[path].offset を進める。cronmon のような「初見ファイルのベースライン吸収」は構造的に存在しない。

### 76-3. ログローテーション対応: 部分的（制限あり）

現状: if info.Size() < startOffset { startOffset = 0 } でサイズ縮小のみ検知。

- inode追跡: なし
- ファイル位置記録: あり（offset）
- ローテーション検知: サイズ縮小のみ

評価: logrotate の create 方式では新ファイルが旧offsetより小さければリセットされ継続。ただし厳密なinodeチェックがなく、ローテーション直後に新ファイルが旧offsetを超えて成長すると、その間の行を見逃す可能性（低確率だが実在）。→ 改善候補として記録。

### 76-4. SSH検知範囲: 4パターン網羅

| ログ行 | 正規表現 |
|---|---|
| Accepted password/publickey for user | reSSHLogin |
| Failed password for user | reSSHFailed |
| Failed password for invalid user | reSSHFailed |
| Invalid user X from Y | reInvalidUser |
| Connection closed by authenticating user | reAuthClosed |

### 76-5. 環境

- auth.log: syslog:adm 640、読取可
- sshpass / expect なし、python3 のみ

---

## 第77章: 攻撃と結果（成功）

### 77-1. 攻撃方法

sshpass がないため、ssh に PreferredAuthentications=password, PubkeyAuthentication=no, NumberOfPasswordPrompts=1 を指定し、</dev/null で空入力を渡して認証失敗させる。

ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no \
    -o NumberOfPasswordPrompts=1 kizuna_nouser@localhost exit </dev/null

### 77-2. auth.log 記録

Invalid user kizuna_nouser from 127.0.0.1 port 59456
Failed none for invalid user kizuna_nouser from 127.0.0.1 port 59456 ssh2
Connection closed by invalid user kizuna_nouser 127.0.0.1 port 59456 [preauth]

### 77-3. 検知結果（成功）

security.log:
{"actor":"kizuna_nouser","event":"ssh_failed","level":"WARNING","ip":"127.0.0.1","message":"ユーザー kizuna_nouser のログインに失敗しました。","source":"/var/log/auth.log","ts":"2026-10-03T02:20:54Z"}

alert_history:
{"type":"security_ssh_failed","level":"warning","icon":"⚠️","title":"SSH ログイン失敗","message":"ユーザー kizuna_nouser のログインに失敗しました。 (ユーザー: kizuna_nouser / 接続元: 127.0.0.1)"}

### 77-4. 後始末

auth.log のテストログ行は削除しない（OSログの追記原則、失敗ログ1件は無害）。

---

## 第78章: 検知状況まとめ（2026-10-03 最終）

| フェーズ | 検知項目 | 結果 |
|---|---|---|
| 1 | 新規リッスンポート | 成功 |
| 2 | cron変更 | 成功（B+A実装） |
| 3 | SUID/SGID | 成功 |
| 4 | SSHログイン | 成功 |
| 5 | FIM改ざん | 未実施（機能は正常、遅延検知を確認） |

---

## 第79章: 学んだ教訓（追記4）

1. monitor.go は tail方式で cronmon 型の検知漏れはない。
2. ログローテーション対応はサイズ縮小のみ。inode追跡を加えると堅牢になる（改善候補）。
3. sshpass がなくても ssh オプションで認証失敗を再現できる。
4. OSログ（auth.log）は追記のみ。テストログの削除はしない。

---

## 第80章: 次回セッションへの引き継ぎ

### 完了
- フェーズ1〜4 すべて成功
- FIM調査

### 未完了
- フェーズ5（FIM改ざん検知の明示的テスト）
- 改善候補: monitor.go のログローテーション inode 追跡

### 次回アクション
1. フェーズ5（FIM改ざん）: テストファイルを作成し、integrity_files に追加して改ざん検知を確認
2. もしくは、これまでの修正の回帰テスト（go test 実行）

---

以上が、2026年10月3日セッション（続き4）の作業記録です。
