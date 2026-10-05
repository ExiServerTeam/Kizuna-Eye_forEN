# Kizuna-Eye .deb パッケージング

`dpkg-deb` だけで `.deb` を作る（nfpm 不要）。

## 前提

- CGO のため、**サポートする一番古い Ubuntu/Debian でビルド**すること。
- 先に本体をビルドしておく: `./build.sh`（`/opt/kizuna-eye/bin` に出力）

## ビルド

    ./packaging/build-deb.sh
    # → dist/kizuna-eye_<version>_<arch>.deb

## 中身の確認

    dpkg-deb -c dist/kizuna-eye_*.deb   # ファイル一覧
    dpkg-deb -I dist/kizuna-eye_*.deb   # 依存などのメタ情報

## 導入

    sudo apt install ./dist/kizuna-eye_*.deb
    # 依存（python3 / smartmontools / rsync / bubblewrap）も自動で入る

初回アクセス: `http://<host>:8080` → `/setup` で管理者作成。

## アンインストール

    sudo apt remove kizuna-eye     # サービス撤去・データは残す
    sudo apt purge  kizuna-eye     # データ・ユーザーも削除

## パッケージ構成

| 配置 | 内容 |
|---|---|
| `/opt/kizuna-eye/bin/` | バイナリ（agent / dashboard / plugin-inspect / plugin-sign） |
| `/usr/share/kizuna-eye/` | web, example, systemd unit（読み取り専用） |
| `/var/lib/kizuna-eye/data` | 設定・状態・ログ（`kizuna-eye` 所有） |
| `/lib/systemd/system/` | `kizuna-eye-agent.service` / `kizuna-dashboard.service` |

## 注意

- プラグインは同梱しない（UI から追加）。`require_signature` は初期 `false`。
- 実効ユーザーは `kizuna-eye`（agent も dashboard も同一）。

---

# APT リポジトリ（`apt install kizuna-eye`）

`.deb` を配るだけなら `sudo apt install ./kizuna-eye_*.deb` で足りる。
名前だけで入れたい場合は、以下の手順でリポジトリを作り公開する。

## 1. 署名鍵を作る（初回のみ）

    ./packaging/apt-key.sh
    # → .gnupg-apt/ に署名鍵を生成（コミット禁止。安全にバックアップ）

## 2. リポジトリを構築

    ./packaging/build-deb.sh     # .deb を作る
    ./packaging/apt-repo.sh      # apt-repo/ を生成 + 署名

生成物:

    apt-repo/
    ├── pool/main/k/kizuna-eye/kizuna-eye_<ver>_amd64.deb
    ├── dists/stable/InRelease
    ├── dists/stable/Release
    ├── dists/stable/Release.gpg
    ├── dists/stable/main/binary-amd64/Packages(.gz)
    ├── kizuna.gpg           # 公開鍵（ASCII, ユーザー配布用）
    └── kizuna-keyring.gpg   # 公開鍵（バイナリ）

## 3. 隔離環境で検証

    sudo ./packaging/test-apt-repo.sh ubuntu:24.04
    # file:// リポジトリとして apt install kizuna-eye を検証

## 4. 公開（GitHub Pages の例）

`apt-repo/` の中身を `gh-pages` ブランチのルートに置く。

    cd apt-repo
    git init -b gh-pages
    git add -A && git commit -m "apt repo"
    git remote add origin git@github.com:<org>/<repo>.git
    git push -f origin gh-pages

公開 URL: `https://<org>.github.io/<repo>/`

## 5. ユーザー側の設定

    # 公開鍵を登録（dearmor が必須。armor のままでは signed-by が効かない）
    curl -fsSL https://<org>.github.io/<repo>/kizuna.gpg \
      | sudo gpg --dearmor -o /usr/share/keyrings/kizuna.gpg

    # リポジトリを追加
    echo "deb [signed-by=/usr/share/keyrings/kizuna.gpg] https://<org>.github.io/<repo> stable main" \
      | sudo tee /etc/apt/sources.list.d/kizuna.list

    sudo apt update
    sudo apt install kizuna-eye

## 鍵のローテーション

- 署名鍵を失うと既存ユーザーは `apt update` に失敗する。`.gnupg-apt/` を必ずバックアップ。
- 鍵を替えたらユーザーは公開鍵の再登録（`/usr/share/keyrings/kizuna.gpg` の差し替え）が必要。
- 秘密鍵はリポジトリにコミットしない（`.gitignore` 済み）。
