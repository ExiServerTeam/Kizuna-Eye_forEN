
---

## 第72章: フェーズ3（SUID/SGID検知）

### 72-1. suid.go Check() 事前点検

- デッドロック: なし（lang() メソッドなし、s.lang フィールド直参照。emitFn は s.mu 保持中に呼ぶが monitor.emit は別ミューテックスで再入なし）
- ただし **時刻ゲート**あり: suid_scan_hour の既定 3（深夜3時のみ走査）。テスト時に現在02時台で走査されない問題

### 72-2. テスト障害の回避

SUID ビット付与には root 権限（chown root）が必要で、sudo -n touch は拒否された。

代替: **自分所有のファイルに SUID を付与**する方式。
- テスト専用ディレクトリ /tmp/kizuna-suid-test を作成
- modules.json の suid_paths に /tmp/kizuna-suid-test を追加
- suid_scan_hour=-1（時刻制限なし）、suid_scan_interval_sec=60（短縮）
- user 所有のファイルに chmod 4755 で SUID 付与可能

### 72-3. 攻撃と結果

攻撃: touch /tmp/kizuna-suid-test/pwned && chmod 4755 /tmp/kizuna-suid-test/pwned

検知結果（成功）:

security.log:
{"event":"suid","level":"CRITICAL","message":"/tmp/kizuna-suid-test/pwned に SUID ビット付きのファイルが出現しました（モード 4755）。","ts":"2026-10-03T02:12:54Z"}

alert_history:
{"type":"security_suid","level":"critical","icon":"🚨","title":"新規 SUID ファイルを検知","message":"/tmp/kizuna-suid-test/pwned に SUID ビット付きのファイルが出現しました（モード 4755）。"}

### 72-4. 後始末

- テストファイル削除（rm -f /tmp/kizuna-suid-test/pwned）
- modules.json を元に戻す（suid_paths, suid_scan_hour, suid_scan_interval_sec 削除）
- Agent 再起動（既定の3時スキャンに戻る）

---

## 第73章: 検知状況まとめ（2026-10-03 最終）

| フェーズ | 検知項目 | 結果 |
|---|---|---|
| 1 | 新規リッスンポート | 成功 |
| 2 | cron変更 | 成功（B+A実装） |
| 3 | SUID/SGID | 成功 |
| 4 | SSHログイン | 未実施（過去に検知実績あり） |
| 5 | FIM改ざん | 未実施（機能は正常、フェーズ1で遅延検知を確認） |

---

## 第74章: 学んだ教訓（追記3）

1. FIM はベースライン差分方式。停止中の変更は復旧時にまとめて検知される（誤検知ではない）。
2. suid.go は scanHour による時刻ゲートを持つ。既定は深夜3時のみ。テスト時は -1 で無効化する。
3. SUID ビットはファイル所有者が設定可能。テストは自分所有のファイル＋suid_paths 追加で sudo 不要。
4. テスト後の巻き戻し（modules.json、テストファイル）を必ず行う。

---

## 第75章: 次回セッションへの引き継ぎ

### 完了
- フェーズ1（新規リッスンポート）
- フェーズ2（cron変更、B+A実装）
- フェーズ3（SUID/SGID）
- FIM誤検知調査

### 未完了
- フェーズ4（SSHログイン検知）
- フェーズ5（FIM改ざん検知）

### 次回アクション
1. フェーズ4（SSHログイン）から再開
2. 存在しないユーザーでのログイン試行（auth.log に記録）

---

以上が、2026年10月3日セッション（続き3）の作業記録です。
