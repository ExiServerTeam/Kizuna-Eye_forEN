#!/bin/bash
# ============================================================
# Kizuna-Eye install.sh
# 初回セットアップ用。必要なパッケージを「確認」し、足りない
# ものだけを案内する。勝手に sudo で全部入れることはしない
# （任意コード実行を避けるため。--install を明示した時だけ入れる）。
#
# 使い方:
#   ./install.sh            # 確認のみ（足りないものを案内）
#   ./install.sh --install  # 足りないものを実際にインストール
# ============================================================
set -e
cd "$(dirname "${BASH_SOURCE[0]}")"

DO_INSTALL=0
for arg in "$@"; do
    case "$arg" in
        --install|-i) DO_INSTALL=1 ;;
        -h|--help)
            echo "使い方: $0 [--install]"
            echo "  --install  足りないパッケージを実際にインストールする"
            exit 0
            ;;
    esac
done

# ---- 必須コマンド ----
# rsync   : バックアッププラグインが使用
# smartctl: ディスク S.M.A.R.T（温度・健康状態・書込量・型番）取得
# git     : アップデート取得
# curl    : 更新チェック / 通知
REQUIRED_CMDS=(rsync smartctl git curl)

# ---- パッケージ名はディストリビューションで異なる ----
# ここでは「提供パッケージ名」を distro ごとに定義する。
PKG_rsync="rsync"
PKG_smartctl="smartmontools"
PKG_git="git"
PKG_curl="curl"

# ---- パッケージマネージャ判定 ----
PM=""
INSTALL_CMD=""
if command -v apt-get >/dev/null 2>&1; then
    PM="apt"
    INSTALL_CMD="apt-get install -y"
elif command -v dnf >/dev/null 2>&1; then
    PM="dnf"
    INSTALL_CMD="dnf install -y"
elif command -v yum >/dev/null 2>&1; then
    PM="yum"
    INSTALL_CMD="yum install -y"
elif command -v pacman >/dev/null 2>&1; then
    PM="pacman"
    INSTALL_CMD="pacman -S --noconfirm"
fi

echo "============================================================"
echo " Kizuna-Eye セットアップチェック"
echo "============================================================"
if [ -n "$PM" ]; then
    echo "パッケージマネージャ: $PM"
else
    echo "⚠️  パッケージマネージャを判定できませんでした（apt/dnf/yum/pacman のいずれも無し）"
fi
echo ""

MISSING_CMDS=()
MISSING_PKGS=()

for cmd in "${REQUIRED_CMDS[@]}"; do
    if command -v "$cmd" >/dev/null 2>&1; then
        echo "✅ $cmd"
    else
        echo "❌ $cmd （未導入）"
        MISSING_CMDS+=("$cmd")
        # コマンド名 → パッケージ名の対応
        case "$cmd" in
            rsync)     MISSING_PKGS+=("$PKG_rsync") ;;
            smartctl)  MISSING_PKGS+=("$PKG_smartctl") ;;
            git)       MISSING_PKGS+=("$PKG_git") ;;
            curl)      MISSING_PKGS+=("$PKG_curl") ;;
        esac
    fi
done

echo ""
if [ ${#MISSING_CMDS[@]} -eq 0 ]; then
    echo "🎉 必要なコマンドはすべて揃っています。"
else
    echo "------------------------------------------------------------"
    echo "不足しているコマンド: ${MISSING_CMDS[*]}"
    echo "対応パッケージ     : ${MISSING_PKGS[*]}"
    echo "------------------------------------------------------------"
    if [ -z "$PM" ]; then
        echo "お使いの環境に合わせて、上記パッケージを手動で導入してください。"
    elif [ "$DO_INSTALL" -eq 1 ]; then
        echo "▶ インストールを実行します: sudo $INSTALL_CMD ${MISSING_PKGS[*]}"
        # 署名検証はパッケージマネージャに委ねる。入れるパッケージは上記に限定。
        sudo $INSTALL_CMD "${MISSING_PKGS[@]}"
        echo "✅ インストール完了"
    else
        echo "次のコマンドで導入できます（sudo が必要な場合があります）:"
        echo ""
        echo "    sudo $INSTALL_CMD ${MISSING_PKGS[*]}"
        echo ""
        echo "または、このスクリプトを --install 付きで再実行:"
        echo "    ./install.sh --install"
    fi
fi

echo ""
echo "次のステップ:"
echo "  1. agent_config.json / dashboard_config.json / modules.json を用意"
echo "  2. ./build.sh でビルド"
echo "  3. ./start.sh で起動"
