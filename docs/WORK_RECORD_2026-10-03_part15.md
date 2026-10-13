
---

# Kizuna-Eye プロジェクト 作業記録 第22部

**セッション日付**: 2026年10月3日（金）続き14
**作業内容**: V5（ログインジェクション）発見・調査

---

## 第118章: V5（ログインジェクション）

### 118-1. 手法と結果

logger -p auth.info -t sshd 'Accepted password for fakeuser from 1.2.3.4 port 22 ssh2' で偽行を注入。

Kizuna-Security は偽行を ssh_login（INFO + unknown IP WARNING）として検知・記録した。

### 118-2. 原因

- logger は syslog 経由で auth facility に書き込める（user は adm 所属）。
- Kizuna-Security は auth.log を行単位で正規表現マッチし、発信元プロセス（sshd）や真正性を検証しない。
- -t sshd でタグを偽装でき、正規表現にマッチする。

### 118-3. 影響

- 偽アラート汚染（運用者を誤誘導、本物の攻撃をノイズに紛れさせる）。
- 検知の信頼性低下（ログ＝真実とは限らない）。

### 118-4. 判定: E（記録）＋ C（rsyslog/管理者タスク）

Kizuna-Security 側では完全防御が難しい（auth.log は正規表現パースのため）。

---

## 第119章: 信頼済みフィールドによる判別（調査結果）

journald の信頼済みフィールド（カーネル付与・偽装不可）:

| 送信元 | _COMM | _UID | _EXE |
|---|---|---|---|
| logger（攻撃） | logger | 1000 | (なし) |
| 本物の sshd | sshd | 0 | /usr/sbin/sshd |

_COMM/_UID/_EXE は偽装不可のため、これで「logger 経由の auth 書き込み」を判別できる。

ただし rsyslog は現在 imuxsock で読んでおり、imuxsock 経由では journald の信頼済みフィールドが rsyslog に渡らない。

---

## 第120章: 防御の選択肢

### 選択肢1（推奨）: Kizuna-Security が journald を直接読む

journalctl -o json で信頼済みフィールド込みで読み、_COMM=logger（または _UID=1000）の auth 行を「偽装の疑い」として検知する。rsyslog 変更不要・副作用なし。

### 選択肢2: rsyslog を imjournal に切替えてフィルタ

$!_COMM でフィルタ。入力方式の大きな変更で副作用リスクあり。

---

## 第121章: 既知の課題（追記）

| # | 課題 | 優先度 |
|---|---|---|
| 7 | V5: auth.log への一般ユーザー書き込み（ログインジェクション）。Kizuna-Security 単体では完全防御不可。journald 信頼済みフィールド（_COMM/_UID）での判別を検討。 | 中 |

---

## 第122章: 偽行の取扱い

今回注入した偽行（fakeuser from 1.2.3.4）は auth.log に残す（OSログ追記原則）。Kizuna-Security のイベントとしては記録済み。「偽行が混入した」ことを運用者に通知する仕組みは、選択肢1の実装時に検討。

---

以上が、2026年10月3日セッション（続き14）の作業記録です。
V5 を発見・記録。防御は選択肢1（journald直読）を推奨。
