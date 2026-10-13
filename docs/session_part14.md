
---

# Kizuna-Eye プロジェクト 作業記録 第21部

**セッション日付**: 2026年10月3日（金）続き13
**作業内容**: E攻撃（sudoヘルパー悪用）

---

## 第116章: E攻撃（sudoヘルパー悪用）

### 116-1. 手法と結果

| # | 攻撃 | 結果 |
|---|---|---|
| 1 | helper /etc/passwd | 引数無視、読まれず |
| 2 | helper --help | 引数無視 |
| 3 | helper ../../etc/passwd | 引数無視 |
| 4 | PATH=/tmp:$PATH sudo -n helper | 絶対パス＋sudo環境リセットで無効 |
| 5 | symlink(/etc/shadow) を引数に | 引数無視、Permission denied |

### 116-2. 判定

すべて防御。ヘルパーは引数を一切使わず /var/spool/cron/crontabs 固定。/etc/passwd が読めたのは world-readable(644) のためで昇格ではない。/etc/shadow は symlink 経由でも読めなかった。

### 116-3. 注記（多層防御・低優先度）

sudoers は helper を引数付きでも許可する（任意引数許可）。ヘルパーが引数を無視するため実害なし。厳密には `user ALL=(root) NOPASSWD: /usr/local/bin/kizuna-cron-read.sh ""` で引数固定が望ましい。既知の課題に追記。

---

## 第117章: 既知の課題（追記）

| # | 課題 | 優先度 |
|---|---|---|
| 6 | sudoers の引数固定（helper ""） | 低 |

---

以上が、2026年10月3日セッション（続き13）の作業記録です。
E攻撃はすべて防御。
