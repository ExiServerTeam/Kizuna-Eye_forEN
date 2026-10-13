# Kizuna-Eye .deb パッケージング

`dpkg-deb` だけで `.deb` を作る（nfpm 不要）。

## 対応環境

- **Ubuntu Server 20.04 LTS 以降** / Debian 11 以降（amd64）。
- CGO のため、**サポートする最古の環境（ubuntu:20.04 = glibc 2.31）で
  ビルド**する。新しい libc でビルドすると古い側で
  `GLIBC_x.y not found` になるため、`docker-build-deb.sh` が
  ubuntu:20.04 コンテナ内でビルドする。

## 前提ツール（メンテナ側）

    sudo apt install docker.io dpkg-dev apt-utils gnupg rsync

| ツール | 用途 |
|---|---|
| `docker` | 20.04 コンテナでのビルド・検証 |
| `dpkg-dev` | `dpkg-scanpackages`（Packages 生成） |
| `apt-utils` | `apt-ftparchive`（リポジトリメタ生成） |
| `gnupg` | リポジトリ署名（Release.gpg / InRelease） |

---

# 1 コマンドでリリース

ソースから「検証済み APT リポジトリ」まで一気に作る。

    sudo ./packaging/release.sh

内部でやること:

1. 前提チェック（docker / dpkg-dev / gpg）
2. 署名鍵が無ければ生成（`apt-key.sh`。初回のみ）
3. **ubuntu:20.04 コンテナ内で本体をビルド → `.deb` 生成**
   （glibc 2.31 にリンクするので 20.04 以降で動く）
4. APT リポジトリを構築 + 署名（`apt-repo.sh`）
5. **ubuntu:20.04 コンテナで `apt install kizuna-eye` を実検証**

オプション:

    sudo ./packaging/release.sh ubuntu:22.04   # 検証イメージを指定
    KIZUNA_SKIP_VERIFY=1 sudo ./packaging/release.sh   # 検証をスキップ

生成物:

    dist/kizuna-eye_<ver>_amd64.deb      # 単体配布用
    apt-repo/                            # APT リポジトリ（要公開）
    ├── pool/main/k/kizuna-eye/kizuna-eye_<ver>_amd64.deb
    ├── dists/stable/InRelease
    ├── dists/stable/Release{,.gpg}
    ├── dists/stable/main/binary-amd64/Packages(.gz)
    ├── kizuna.gpg                       # 公開鍵（ASCII, ユーザー配布用）
    └── kizuna-keyring.gpg               # 公開鍵（バイナリ）

---

# 手動でやる場合

`release.sh` を使わず個別に叩くこともできる。

    ./packaging/apt-key.sh            # 署名鍵（初回のみ）
    ./packaging/docker-build-deb.sh   # ubuntu:20.04 内でビルド → .deb
    ./packaging/apt-repo.sh           # apt-repo/ を生成 + 署名
    sudo ./packaging/test-apt-repo.sh ubuntu:20.04   # 隔離検証

## 中身の確認

    dpkg-deb -c dist/kizuna-eye_*.deb   # ファイル一覧
    dpkg-deb -I dist/kizuna-eye_*.deb   # 依存などのメタ情報

---

# ユーザー側（インストール）

`apt install kizuna-eye` の**一行だけで完結**する。postinst が
ユーザー作成・設定生成（`agent_token` 自動生成）・systemd 登録・起動まで全部やる。

## リポジトリを登録（初回のみ）

    # 公開鍵を登録（dearmor が必須。armor のままでは signed-by が効かない）
    curl -fsSL https://<org>.github.io/<repo>/kizuna.gpg \
      | sudo gpg --dearmor -o /usr/share/keyrings/kizuna.gpg

    # リポジトリを追加
    echo "deb [signed-by=/usr/share/keyrings/kizuna.gpg] https://<org>.github.io/<repo> stable main" \
      | sudo tee /etc/apt/sources.list.d/kizuna.list

    sudo apt update

## インストール（これだけ）

    sudo apt install kizuna-eye

完了時に表示される URL をブラウザで開き、`/setup` で管理者アカウントを作成する。
以降の設定（通知・モジュール・閾値・言語）は**すべて Web UI** で行う。

## アンインストール

    sudo apt remove kizuna-eye     # サービス撤去・データは残す
    sudo apt purge  kizuna-eye     # データ・ユーザーも削除

---

# パッケージ構成

| 配置 | 内容 |
|---|---|
| `/opt/kizuna-eye/bin/` | バイナリ（agent / dashboard / plugin-inspect / plugin-sign） |
| `/usr/share/kizuna-eye/` | web, example, systemd unit, helpers（読み取り専用） |
| `/var/lib/kizuna-eye/data` | 設定・状態・ログ（`kizuna-eye` 所有） |
| `/lib/systemd/system/` | `kizuna-eye-agent.service` / `kizuna-dashboard.service` / `kizuna-action.{service,socket}` |

## 依存

    Depends: adduser, python3, smartmontools, rsync, bubblewrap
    Recommends: nftables, ca-certificates

- `smartmontools` / `rsync`: S.M.A.R.T とバックアッププラグイン。
- `bubblewrap`: アップロード .so の隔離検査（fail-closed）。
- `nftables`: Kizuna-Security の自動 IP ブロック。
- `ca-certificates`: Discord / Slack など HTTPS 通知。

## 注意

- プラグインは同梱しない（UI から追加）。`require_signature` は初期 `false`。
- agent と dashboard は専用ユーザー `kizuna-eye` で動く（権限分離）。
- アップグレード（`apt upgrade`）は既存の設定ファイルを書き換えない。
  設定の調整は**新規作成時のみ**行うため、Web UI での変更は保持される。

---

# 公開（GitHub Pages の例）

`apt-repo/` の中身を `gh-pages` ブランチのルートに置く。

    cd apt-repo
    git init -b gh-pages
    git add -A && git commit -m "apt repo"
    git remote add origin git@github.com:<org>/<repo>.git
    git push -f origin gh-pages

公開 URL: `https://<org>.github.io/<repo>/`

## 鍵のローテーション

- 署名鍵を失うと既存ユーザーは `apt update` に失敗する。`.gnupg-apt/` を必ずバックアップ。
- 鍵を替えたらユーザーは公開鍵の再登録（`/usr/share/keyrings/kizuna.gpg` の差し替え）が必要。
- 秘密鍵はリポジトリにコミットしない（`.gitignore` 済み）。
