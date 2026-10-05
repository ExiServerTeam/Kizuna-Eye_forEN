
---

# Kizuna-Eye プロジェクト 作業記録 第14部

**セッション日付**: 2026年10月3日（金）続き6
**作業内容**: フェーズ5（FIM改ざん検知）、全フェーズ完了

---

## 第83章: フェーズ5（FIM改ざん検知）

### 83-1. 攻撃準備

- テストファイル /tmp/kizuna-fim-test/watched.txt を作成（original-content）
- modules.json の integrity_files に追加（デフォルト9件 + テストファイル）
- Agent 再起動

### 83-2. 検知結果（成功）

検知1（新規作成・ベースライン取り込み後）:
{"event":"integrity","level":"CRITICAL","message":"/tmp/kizuna-fim-test/watched.txt が新規に作成されました（バックドアの可能性）。","ts":"2026-10-03T02:49:31Z"}

検知2（ファイル変更）:
{"event":"integrity","level":"CRITICAL","message":"/tmp/kizuna-fim-test/watched.txt が変更されました。至急確認してください。","ts":"2026-10-03T02:52:49Z"}

alert_history:
{"type":"security_integrity","level":"critical","icon":"🚨","title":"重要ファイルの改ざんを検知","message":"/tmp/kizuna-fim-test/watched.txt が変更されました。至急確認してください。"}

### 83-3. 後始末

- テストディレクトリ削除（rm -rf /tmp/kizuna-fim-test）
- modules.json を元に戻す（integrity_files 削除）
- Agent 再起動

---

## 第84章: 全フェーズ完了

| フェーズ | 検知項目 | 結果 |
|---|---|---|
| 1 | 新規リッスンポート | 成功 |
| 2 | cron変更 | 成功（B+A実装） |
| 3 | SUID/SGID | 成功 |
| 4 | SSHログイン | 成功 |
| 5 | FIM改ざん | 成功 |

全5フェーズが成功。

---

## 第85章: 今回のセッションで修正したバグ一覧

| # | ファイル | 種別 | 内容 |
|---|---|---|---|
| 1 | fim.go | デッドロック | lang()/SetLang() が f.mu を再ロック。langMu に分離 |
| 2 | cronmon.go | 検知漏れ | 読めないディレクトリの無言失敗。checkUnreadable で警告 |
| 3 | cronmon.go | 検知漏れ | 初見ファイルを無言でベースライン吸収。underWatchedDir で検知 |
| 4 | cronmon.go | 権限 | /var/spool/cron/crontabs が読めない。sudo ヘルパー経由で読取 |
| 5 | suid.go | 検知漏れ | hasBaseline=len(known)>0。0件システムで検知不能。専用フラグ |
| 6 | messages.go | 追加 | cron.unreadable の翻訳キー |
| 7 | security_extra_test.go | テスト | FIM新規監視対象の期待値更新 |
| 8 | monitor_test.go | テスト分離 | 共有loginベースラインの順序依存を排除 |

---

## 第86章: 学んだ教訓（総括）

1. Go の mutex は再入不可。ロック保持中に同じ mutex を取るメソッドを呼ぶと自己デッドロック（fim.go）。
2. プラグインの Run() 内で1つハングすると、後続の全チェックが停止する（fim.Check → portMon.Check）。
3. 権限による検知不能は「死角」。可視化（B）が先、権限付与（A）が後。
4. 監視対象の初見ファイルを無言でベースラインに吸収すると検知漏れ（cronmon, suid）。
5. テストは共有状態（相対パスのファイル）で順序依存になる。テストヘルパーで永続化を無効化する。
6. SIGQUIT の goroutine ダンプはハング特定に強力（sudo不要）。
7. パッチスクリプトはドライラン→差分確認→本番。Goは gofmt -w で整形。
8. 修正後は go test を複数回実行し、順序依存がないか確認する。

---

## 第87章: 既知の課題（継続）

- ログローテーションの inode 追跡（monitor.go、優先度:中）
- テスト分離の一般化（他テストの共有状態点検）

---

以上が、2026年10月3日セッション（続き6）の作業記録です。
全5フェーズの攻撃テストが完了しました。
