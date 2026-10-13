
---

## 第62章: A実装（sudo による crontab 読み取り）— 次回作業

### 62-1. 目的

B で「読めない」ことを可視化したが、実際に読めるようにするには root 権限が必要。sudo -n cat で crontab を読み取れるようにし、検知を有効化する。

### 62-2. sudoers 設定（管理者が手元で実行）

sudo visudo -f /etc/sudoers.d/kizuna-security-cron

内容（1行、読み取り専用）:

user ALL=(root) NOPASSWD: /usr/bin/cat /var/spool/cron/crontabs/*

注意: cat のパスは which cat で確認（通常 /usr/bin/cat）。ワイルドカードはファイル名のみにマッチし、/ は越えない。書き込み・削除は許可しない。

### 62-3. 検証（管理者が手元で実行）

sudo -n cat /var/spool/cron/crontabs/root 2>&1 | head
sudo -n -l | grep crontabs

### 62-4. cronmon.go の追加修正（A実装、次回）

cronmon.scan() を拡張し、/var/spool/cron/crontabs 配下のファイルを sudo -n cat で読み取ってハッシュ化する。ディレクトリ一覧も sudo 経由で取得する。

実装上の注意:
- exec.Command("sudo", "-n", "cat", path) の引数は配列で渡す（シェル経由にしない＝インジェクション回避）
- path は sanitizePath 済みの絶対パスのみ
- sudo 失敗時は B の警告経路にフォールバック

### 62-5. A実装後の再テスト

1. ユーザー crontab にコメント行を追加
2. cron カテゴリの critical イベントが出ることを確認
3. crontab 削除

---

## 第63章: 検知状況まとめ（2026-10-03 時点）

| フェーズ | 検知項目 | 結果 | 対応 |
|---|---|---|---|
| 1 | 新規リッスンポート | 成功 | fim.goデッドロック修正後 |
| 2 | cron変更 | 検知不能→可視化 | B実装済み、A実装待ち |
| 3 | SUID/SGID | 未実施 | 次回 |
| 4 | SSHログイン | 未実施（過去に検知実績あり） | 次回 |
| 5 | FIM改ざん | 未実施 | 次回 |

---

## 第64章: 学んだ教訓（追記）

1. 権限による検知不能は「見逃し」ではなく「死角」。可視化（B）が先、権限付与（A）が後。
2. 監視対象に「読めないパス」が含まれる場合、警告を出すことで、無言の失敗を防げる。
3. filepath.WalkDir は読めないディレクトリでエラーを返すが、return nil で握りつぶすと死角になる。今回の cronmon はまさにこれだった。
4. sudo -n（NOPASSWD）は最小権限で。cat のみ、対象パスを限定する。
5. パッチスクリプトはドライラン→差分確認→本番の順で。日本語リテラルはアンカーにせず、キー名や構造でマッチさせる。

---

以上が、2026年10月3日セッション（続き）の作業記録です。

次回セッション: A実装（sudoers 設定 → cronmon.go の sudo cat 対応）から開始予定。
