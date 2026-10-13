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
