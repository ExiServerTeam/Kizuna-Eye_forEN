
---

# Kizuna-Eye プロジェクト 作業記録 第10部（A実装完了）

**セッション日付**: 2026年10月3日（金）続き2
**作業内容**: A実装（sudo ヘルパーによる crontab 読み取り）、フェーズ2完全成功

---

## 第65章: A実装（sudo ヘルパー）

### 65-1. 経緯: sudoers のワイルドカード構文エラー

当初 user ALL=(root) NOPASSWD: /usr/bin/cat /var/spool/cron/crontabs/* を試みたが、この sudo（1.9.10+）はコマンド引数のワイルドカードを許可しない（wildcards are not allowed in command arguments）ため構文エラーになった。

さらに visudo -c が失敗したのに tee でファイルが書き込まれ、不正な sudoers ファイルが残る事故が発生（sudo 自体は警告付きで動作継続）。速やかに削除した。

### 65-2. 解決: 読み取り専用ヘルパースクリプト方式

ワイルドカードを使わず、専用ヘルパーを NOPASSWD で許可する方式に変更。

ヘルパー: /usr/local/bin/kizuna-cron-read.sh（root:root 755）
- /var/spool/cron/crontabs/ の各ファイルの 名前TAB sha256 を出力
- ファイル名を [A-Za-z0-9_.-] で厳格に検証
- 読み取り専用（書き込み・削除なし）

sudoers: /etc/sudoers.d/kizuna-security-cron（root:root 440）

user ALL=(root) NOPASSWD: /usr/local/bin/kizuna-cron-read.sh

検証結果: sudo -n -l に (root) NOPASSWD: /usr/local/bin/kizuna-cron-read.sh が表示され、sudo -n ヘルパー が EXIT=0 で動作。

---

## 第66章: cronmon.go の A 修正

### 66-1. 変更点（5編集）

1. import 追加: context, os/exec, strings
2. scan(): p == cronSudoDir のとき readCronViaSudo() で crontab を読む。失敗時は従来の WalkDir にフォールバック
3. 新規ファイル検知のバグ修正: if !existed { if !c.watched[p] { continue } } を if !c.watched[p] && !c.underWatchedDir(p) { continue } に変更。旧コードは初めて見たファイルを無言でベースラインに吸収し、新規 crontab を検知できなかった
4. checkUnreadable(): crontabs ディレクトリでヘルパーが成功すれば警告を出さない（かつ既存の警告フラグを解除）
5. 新関数: readCronViaSudo(), underWatchedDir(), 定数 cronSudoDir / cronSudoHelper

### 66-2. readCronViaSudo の安全性

- exec.CommandContext(ctx, sudo, -n, cronSudoHelper).Output() で引数を配列で渡す（シェル経由なし＝インジェクション不可）
- 10秒タイムアウト
- 出力のファイル名は / \ . .. を拒否（パス注入防止）

### 66-3. 適用手順

1. バックアップ（cronmon.go.bak-20261003_013850, .prepatch-a）
2. パッチスクリプト scripts/patch_cronmon_sudo.py
3. ドライラン成功（5 edits）
4. 本番適用 + diff 確認
5. gofmt -w でインデント整形（パッチ挿入行のインデント崩れを修正）
6. go vet クリーン
7. ビルド → 配備 → Agent 再起動
