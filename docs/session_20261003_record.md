
---

# Kizuna-Eye プロジェクト 作業記録 第8部

**セッション日付**: 2026年10月3日（金）
**セッション目標**: Kizuna-Security の検知停止バグの特定・修正、攻撃フェーズ1の再検証

---

## 第53章: セッション開始時の重大インシデント

### 53-1. 症状

攻撃フェーズ1（新規リッスンポート検知）を実行したところ、ポート44444 が検知されなかった。

| 確認項目 | 結果 |
|---|---|
| alert_history.jsonl | listen_port / 44444 の記録なし |
| kizuna-security.log | configured イベントのみ。listen_port なし |
| kizuna-security-ports.json | 10/2 06:57 から更新なし |
| Agent プロセス | 生存（00:21起動） |
| agent.log | 00:23:51 以降、更新停止 |

### 53-2. 一時的な誤解（不正アクセス疑い）

dashboard.log に 00:22〜00:23 の pentest_admin ログインとプラグイン差し替えが記録されており、当初は不正アクセスを疑った。

確認の結果: pentest_admin はユーザーのアカウントで、別AIセッションが途中停止した際の操作と判明。不正アクセスではない。

### 53-3. 原因の連鎖

1. 別セッションが kizuna_backup_lite を再アップロードする際、target_dir が空のまま modules.json に書き込まれた
2. Agent が modules.json を再読み込みし、kizuna_backup_lite の設定検証に失敗（target_dir は必須です）
3. agent.log が 00:23:51 で停止

---

## 第54章: 証拠保全と復旧

### 54-1. 証拠保全

秘密情報（IP・セッション）を含むため、Samba外（~/kizuna_evidence_<TS>/）に権限700で保全。

- agent.log, dashboard.log, alert_history.jsonl, kizuna-security.log
- modules.json, dashboard_config.json, sessions.json, users.json
- ps_snapshot.txt, ss_snapshot.txt
- /proc/<PID>/status, /proc/<PID>/wchan, /proc/<PID>/task

### 54-2. バックアップ方針の修正

当初の誤り: 設定ファイル（*.json / *.sh）を Samba 共有内にバックアップしてしまった。

修正後:
- ソースコード（cmd, pkg, internal, web, Kizuna-Security/plugin）→ Samba 共有内
- 設定ファイル（秘密情報を含む）→ Samba 外（~user/、権限600）

### 54-3. modules.json の修正

kizuna_backup_lite に target_dir=/samba/share/CD を復元。dry_run:true と run_on_start:false を設定し、Samba共有への実書き込みを防止。

---

## 第55章: 根本原因の特定（重要）

### 55-1. 当初の仮説と外れ

| 仮説 | 結果 |
|---|---|
| modules.json の設定ミス | 修正しても改善せず |
| Agent の runModuleLoop 未起動 | goroutine ダンプで否定 |
| kizuna_backup_lite が原因 | 独立して動作していた |

### 55-2. SIGQUIT による goroutine ダンプ

kill -QUIT <agent_pid> で全 goroutine のスタックを取得（user権限で可能、sudo不要）。

決定的証拠（goroutine 25）:

goroutine 25 ... [sync.Mutex.Lock, 7 minutes]:
    internal/sync.(*Mutex).lockSlow(...)
kizuna-security-plugin.(*FIM).lang(...)
    /samba/share/Kizuna-Security/plugin/fim.go:158
kizuna-security-plugin.(*FIM).Check(...)
    /samba/share/Kizuna-Security/plugin/fim.go:123
kizuna-security-plugin.(*SecurityPlugin).Run(...)
    /samba/share/Kizuna-Security/plugin/plugin.go:82

### 55-3. バグの本質: FIM の自己デッドロック

fim.go の Check() が f.mu.Lock() を保持したまま、同じ f.mu を取る lang() を呼んでいた。

Go の sync.Mutex は再入不可のため、自己デッドロック。

連鎖:
1. Run() → mon.Scan()（成功）
2. → fim.Check() でデッドロック
3. → cronMon.Check() / portMon.Check() / suidMon.Check() に到達不能
4. → ports.json 未更新 → ポート44444 未検知

portmon.go は正常。手前の fim.go がロックして後続を全て停止させていた。

---

## 第56章: 修正

### 56-1. fim.go の修正

言語ガードを専用ミューテックス langMu に分離。

- struct FIM に langMu sync.RWMutex を追加
- lang(): f.mu → f.langMu.RLock() に変更
- SetLang(): f.mu → f.langMu.Lock() に変更

パッチスクリプト（scripts/patch_fim_deadlock.py）で適用。ドライランで3箇所マッチを確認後、本番適用。バックアップ（fim.go.bak-deadlock）作成済み。

### 56-2. ビルドと配備

cd /samba/share/Kizuna-Security/plugin
GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o kizuna_security.so .
cp kizuna_security.so /opt/kizuna-eye/bin/plugins/

### 56-3. Agent 再起動後の確認

| 指標 | 修正前 | 修正後 |
|---|---|---|
| security.log mtime | 00:23 | 01:24 |
| ports.json mtime | 10/2 06:57 | 01:24 |
| Run() 実行痕跡 | なし | あり |
| integrity イベント | なし | あり |

---

## 第57章: 攻撃フェーズ1 再検証（成功）

### 57-1. 攻撃内容

nc -l 44444 （TCP 44444 を待ち受け、timeout 70s）

### 57-2. 検知結果: 成功

security.log:
{"event":"listen_port","level":"WARNING","message":"TCP ポート 44444 が新たに待ち受けを開始しました（0.0.0.0）。","source":"ss","ts":"2026-10-03T01:25:39Z"}

alert_history.jsonl:
{"type":"security_listen_port","level":"warning","icon":"⚠️","title":"新規リッスンポートを検知","message":"TCP ポート 44444 が新たに待ち受けを開始しました（0.0.0.0）。"}

### 57-3. 判定

| 項目 | 結果 |
|---|---|
| 検知 | 成功 |
| レベル | WARNING（listen_port） |
| アラート履歴 | 記録済み |
| 検知までの時間 | 約20秒（ポーリング15秒+処理） |
| Discord通知 | 未送信（notifications.enabled=false のため、仕様通り） |

---

## 第58章: 学んだ教訓

1. Go の mutex は再入不可。ロック保持中に同じ mutex を取るメソッドを呼ぶと自己デッドロック。
2. guard 用 mutex は責務ごとに分離する。mu（状態）と langMu（言語）を混ぜると事故が起きる。
3. SIGQUIT による goroutine ダンプは強力。ハング箇所を行番号付きで特定できる。sudo不要。
4. プラグインの Run() 内で 1 つハングすると、後続の全チェックが停止する。
5. sync.RWMutex を使う場合は他メソッドと共有しない。
6. 設定ファイルのバックアップは Samba 外へ（秘密情報を含むため）。
7. 証拠保全は最優先。再起動前にログ・プロセス状態・stack を退避する。

---

## 第59章: 次回セッションへの引き継ぎ

### 59-1. 完了した作業

| # | 内容 | 状態 |
|---|---|---|
| 1 | 攻撃フェーズ1（新規リッスンポート）再検証 | 成功 |
| 2 | FIM 自己デッドロックの特定・修正 | 完了 |
| 3 | kizuna_security.so 再ビルド・配備 | 完了 |
| 4 | modules.json の復旧 | 完了 |
| 5 | 証拠保全 | 完了 |

### 59-2. 未完了の作業

| # | 内容 | 優先度 |
|---|---|---|
| 1 | 攻撃フェーズ2（cron変更検知） | 高 |
| 2 | 攻撃フェーズ3（SUID/SGID検知） | 中 |
| 3 | 攻撃フェーズ4（SSHログイン検知） | 中 |
| 4 | 攻撃フェーズ5（FIM改ざん検知） | 中 |
| 5 | 他ファイルの同種デッドロック点検（suid.go, cronmon.go, portmon.go） | 中 |

### 59-3. 次のアクション

1. 攻撃フェーズ2（cron変更検知）から再開
2. cronmon.go の Check() が mu 保持中に emit を呼んでいないか事前点検
3. suid.go の Check() も同様に点検

---

以上が、2026年10月3日セッションの作業記録です。
