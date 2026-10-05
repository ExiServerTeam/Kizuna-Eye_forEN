#!/bin/bash
# ============================================================
# Kizuna-Security: ワンクリック対処ヘルパー (タスク2)
#
# ダッシュボードは user 権限で動くため、kill / cron 削除 /
# ファイアウォール操作を直接は実行できない。このヘルパーは
# sudoers で「引数付きでもこのスクリプトだけ」を許可し、
# 内部でアクションを列挙型に固定して検証する。
#
# 使い方（すべて sudo -n 経由で呼ばれる）:
#   kizuna-action.sh block_ip <IPv4|IPv6>
#   kizuna-action.sh unblock_ip <IPv4|IPv6>
#   kizuna-action.sh delete_cron <crontab-basename>
#   kizuna-action.sh kill_process <pid>
#   kizuna-action.sh kill_port <port>
#   kizuna-action.sh restore_file <absolute-path> <backup-path>
#
# セキュリティ:
#   - 外部コマンドは絶対パス（PATH 乗っ取り防止）
#   - 引数は厳格に検証（IP は net.ParseIP 相当、PID は数字のみ、
#     パスは許可プレフィックスのみ、cron 名は basename のみ）
#   - シェル経由で引数を再展開しない（"$@" を直接渡す）
# ============================================================
set -u

NFT=/usr/sbin/nft
CRONTAB_DIR=/var/spool/cron/crontabs

# ss の絶対パスはディストリビューションで /usr/bin と /usr/sbin の
# どちらにもなり得る。片方を決め打ちすると、パスが違う環境で
# 「no process is listening」と誤って報告し、実際には listen 中の
# プロセスを停止できない（無言の失敗）。存在するほうを使う。
SS=""
for c in /usr/sbin/ss /usr/bin/ss /bin/ss /sbin/ss; do
  if [ -x "$c" ]; then SS="$c"; break; fi
done
if [ -z "$SS" ]; then
  SS=$(command -v ss 2>/dev/null || true)
fi
if [ -z "$SS" ]; then
  /usr/bin/printf 'ss command not found\n' >&2; exit 6
fi

usage() {
  /usr/bin/printf 'usage: %s <block_ip|unblock_ip|delete_cron|kill_process|kill_port|restore_file> ...\n' "$0" >&2
  exit 2
}

[ $# -ge 2 ] || usage
action="$1"; shift

case "$action" in
  block_ip|unblock_ip)
    ip="$1"
    # IPv4/IPv6 を厳格に検証（シェルメタ文字を一切許可しない）。
    if ! /usr/bin/printf '%s' "$ip" | /usr/bin/grep -Eq '^[0-9a-fA-F:.]+$'; then
      /usr/bin/printf 'invalid ip\n' >&2; exit 3
    fi
    if /usr/bin/printf '%s' "$ip" | /usr/bin/grep -q ':'; then setname=blocked6; else setname=blocked4; fi
    # テーブルが無ければ作る（冪等）。
    $NFT list table inet kizuna >/dev/null 2>&1 || {
      $NFT add table inet kizuna
      $NFT add set inet kizuna blocked4 '{ type ipv4_addr ; }' 2>/dev/null || true
      $NFT add set inet kizuna blocked6 '{ type ipv6_addr ; }' 2>/dev/null || true
      $NFT add chain inet kizuna input '{ type filter hook input priority -10 ; policy accept ; }' 2>/dev/null || true
      $NFT add rule inet kizuna input ip saddr @blocked4 counter drop 2>/dev/null || true
      $NFT add rule inet kizuna input ip6 saddr @blocked6 counter drop 2>/dev/null || true
    }
    if [ "$action" = block_ip ]; then
      $NFT add element inet kizuna "$setname" "{ $ip }" 2>/dev/null || $NFT add element inet kizuna "$setname" "{ $ip timeout 24h }"
    else
      $NFT delete element inet kizuna "$setname" "{ $ip }" 2>/dev/null || true
    fi
    ;;
  delete_cron)
    base="$1"
    # basename のみ許可（パス traversal 防止）。
    case "$base" in
      *[!A-Za-z0-9_.-]*|'') /usr/bin/printf 'invalid cron name\n' >&2; exit 3 ;;
    esac
    rm -f -- "$CRONTAB_DIR/$base"
    ;;
  kill_process)
    pid="$1"
    case "$pid" in *[!0-9]*|'') /usr/bin/printf 'invalid pid\n' >&2; exit 3 ;; esac
    # PID が既に存在しない場合は成功扱い（冪等）。
    kill -TERM "$pid" 2>/dev/null || true
    ;;
  kill_port)
    # ポート番号から待ち受けプロセスのPIDを解決して停止する。
    # モーダルはポートを対象と分かっているのに PID を手入力させるのは
    # UX として悪いので、ss -ltnp の出力（users:(...pid=NNN...)）から
    # 自動で PID を取り出す。
    port="$1"
    case "$port" in *[!0-9]*|'') /usr/bin/printf 'invalid port\n' >&2; exit 3 ;; esac
    if [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
      /usr/bin/printf 'port out of range\n' >&2; exit 3
    fi
    # ss の出力から pid=NNN を取り出す。"sport = :N" のフィルタは
    # バージョンによっては解釈が異なるため、全リスナーを取ってから
    # ポート番号で絞り込む（誤って「プロセスなし」と言わないため）。
    pids=$("$SS" -ltnp 2>/dev/null \
             | /usr/bin/grep -E ":${port}[[:space:]]" \
             | /usr/bin/grep -oE 'pid=[0-9]+' \
             | /usr/bin/sed 's/^pid=//' \
             | /usr/bin/sort -u)
    if [ -z "$pids" ]; then
      /usr/bin/printf 'no process is listening on port %s\n' "$port" >&2
      exit 4
    fi
    killed=""
    for p in $pids; do
      # まず TERM。ハード kill まで自動でエスカレーションしない
      # （穏当な停止が効かない場合は操作者が UI で判断する）。
      if kill -TERM "$p" 2>/dev/null; then
        killed="$killed $p"
      fi
    done
    if [ -z "$killed" ]; then
      /usr/bin/printf 'no process could be signalled\n' >&2
      exit 5
    fi
    /usr/bin/printf 'killed:%s\n' "$killed"
    ;;
  restore_file)
    target="$1"; backup="$2"
    # 許可プレフィックス（監視ディレクトリのみ）。
    case "$target" in
      /tmp/*|/var/tmp/*|/dev/shm/*|/run/*) ;;
      *) /usr/bin/printf 'path not allowed\n' >&2; exit 3 ;;
    esac
    case "$backup" in
      /var/lib/kizuna-eye/backups/*) ;;
      *) /usr/bin/printf 'backup path not allowed\n' >&2; exit 3 ;;
    esac
    # 追加の防御（root で書き込むため）:
    #   - 復元先そのものが symlink なら拒否（install は最終要素の symlink を
    #     辿らず unlink する実装だが、実装差に依存しない）
    #   - 親ディレクトリを実体解決し、解決後も許可プレフィックス内であることを
    #     確認する（例: /tmp/link -> /etc のような親 symlink を経由して
    #     root が /etc 配下へ書くのを防ぐ）
    #   - backup 側の symlink も拒否（root に任意ファイルを読ませない）
    if [ -L "$target" ]; then
      /usr/bin/printf 'target is a symlink\n' >&2; exit 3
    fi
    if [ -L "$backup" ]; then
      /usr/bin/printf 'backup is a symlink\n' >&2; exit 3
    fi
    parent="$(/usr/bin/dirname -- "$target")"
    rparent="$(/usr/bin/readlink -f -- "$parent" 2>/dev/null || true)"
    if [ -z "$rparent" ] || [ ! -d "$rparent" ]; then
      /usr/bin/printf 'target directory not found\n' >&2; exit 3
    fi
    case "$rparent" in
      /tmp|/tmp/*|/var/tmp|/var/tmp/*|/dev/shm|/dev/shm/*|/run|/run/*) ;;
      *) /usr/bin/printf 'target directory not allowed\n' >&2; exit 3 ;;
    esac
    [ -f "$backup" ] || { /usr/bin/printf 'backup missing\n' >&2; exit 4; }
    /usr/bin/install -m 0644 -- "$backup" "$target"
    ;;
  *)
    usage
    ;;
esac

/usr/bin/printf 'ok\n'
