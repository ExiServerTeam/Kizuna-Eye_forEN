package main

import "fmt"

// msg returns the localized string for key in lang, falling back to Japanese.
// Kizuna-Security emits notifications from the Agent (server side), where no
// browser locale exists, so the language is an explicit plugin setting rather
// than the dashboard UI language.
func msg(lang, key string, args ...interface{}) string {
	table, ok := catalog[lang]
	if !ok {
		table = catalog["ja"]
	}
	s, ok := table[key]
	if !ok {
		s = catalog["ja"][key]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

var catalog = map[string]map[string]string{
	"ja": {
		"ssh_login.title":            "SSH ログイン成功",
		"ssh_login.msg":              "ユーザー %s が %s からログインしました。",
		"ssh_login.unknown.title":    "未知のIPからのSSHログイン成功",
		"ssh_login.unknown.msg":      "ユーザー %s が、これまで見られなかった IP %s からログインしました。",
		"ssh_login.burst.title":      "SSHログイン成功の急増",
		"ssh_login.burst.msg":        "IP %s からのログイン成功が短時間に %d 回発生しました。",
		"ssh_login.burst.known.note": "（既知 IP です。監視ツールなどからの反復接続でも同じ急増が起きます。）",

		"ssh_failed.title":           "SSH ログイン失敗",
		"ssh_failed.msg":             "ユーザー %s のログインに失敗しました。",
		"ssh_failed.burst.title":     "SSH ログイン失敗の多発",
		"ssh_failed.burst.msg":       "IP %s から %d 回のログイン失敗を検知しました（総当たり攻撃の可能性）。",
		"ssh_failed.sustained.title": "SSH ログイン失敗の持続（低頻度の総当たり）",
		"ssh_failed.sustained.msg":   "IP %s から %d 秒間に %d 回のログイン失敗を検知しました（間隔を空けた持続的な総当たり攻撃の可能性）。",
		"ssh_failed.enum.title":      "SSH ユーザー名の総当たり（列挙）",
		"ssh_failed.enum.msg":        "IP %s から %d 種類のユーザー名でログイン失敗を検知しました（ユーザー名を回した総当たり・列挙の可能性）: %s",
		"ssh_failed.loopback.note":   " 接続元はループバック (127.0.0.1) です。ホスト上に侵入済みの攻撃者、または 127.0.0.1 へのトンネル経由の試行で、送信元 IP では特定できません。",

		"sudo.title":      "sudo 実行",
		"sudo.msg":        "%s が %s として「%s」を実行しました。",
		"sudo.fail.title": "sudo 認証失敗",
		"sudo.fail.msg":   "%s の sudo 認証が失敗しました。",

		"account.title": "アカウント操作",

		"install.title":         "パッケージをインストール",
		"install.upgrade.title": "パッケージをアップグレード",
		"install.remove.title":  "パッケージを削除",
		"install.msg":           "パッケージ %s を%sしました。",
		"install.verb.install":  "インストール",
		"install.verb.upgrade":  "アップグレード",
		"install.verb.remove":   "削除",
		"apt.title":             "APT %s",

		"integrity.create.title":     "重要ファイルの作成を検知",
		"integrity.create.msg":       "%s が新規に作成されました（バックドアの可能性）。",
		"integrity.change.title":     "重要ファイルの改ざんを検知",
		"integrity.change.msg":       "%s が変更されました。至急確認してください。",
		"integrity.delete.title":     "重要ファイルの削除を検知",
		"integrity.delete.msg":       "%s が削除されました。",
		"integrity.baseline.title":   "監視対象をベースラインに追加",
		"integrity.baseline.msg":     "%s を新たに監視対象としてベースラインに取り込みました。意図した変更か確認してください。",
		"integrity.unreadable.title": "重要ファイルを読み取れません",
		"integrity.unreadable.msg":   "%s を読み取れません（権限不足・削除など）。改ざんを検知できない状態です: %s",
		// 権限不足は非 root 運用では常時発生するため info（履歴のみ）で通知する。
		// それ以外の原因（symlink 差し替えなど）は warning のまま。
		"integrity.unreadable.perm.msg": "%s を権限不足のため読み取れません（実行ユーザーに読取権限がありません）。このファイルは改ざん検知の対象外です。root 実行・CAP_DAC_READ_SEARCH の付与・integrity_files からの除外のいずれかを検討してください: %s",
		// 複数件をまとめた通知（1ファイル1通知だと履歴が埋まるため）。
		"integrity.unreadable.multi.perm.msg": "%d 件のファイルを権限不足のため読み取れません（実行ユーザーに読取権限がありません）。これらは改ざん検知の対象外です。root 実行・CAP_DAC_READ_SEARCH の付与・integrity_files からの除外のいずれかを検討してください: %s",
		"integrity.unreadable.multi.msg":      "%d 件のファイルを読み取れません（削除・symlink 差し替えなど）。改ざんを検知できない状態です: %s",
		"integrity.baseline.tamper.title":     "FIM ベースラインの改ざんを検知",
		"integrity.baseline.tamper.msg":       "FIM ベースライン %s の署名が一致しません（改ざん・鍵変更の可能性）。ベースラインを再取得しました: %s",
		// ディレクトリ監視（fim_watch）。パスを事前に列挙できない置き場
		// （/tmp 等）での作成・変更・削除を検知する。
		"fimwatch.create.title":    "監視ディレクトリでファイル作成を検知",
		"fimwatch.create.msg":      "%s が新規に作成されました（監視ディレクトリ内・バックドアの可能性）。",
		"fimwatch.change.title":    "監視ディレクトリでファイル改ざんを検知",
		"fimwatch.change.msg":      "%s が変更されました。至急確認してください。",
		"fimwatch.delete.title":    "監視ディレクトリでファイル削除を検知",
		"fimwatch.delete.msg":      "%s が削除されました。",
		"fimwatch.transient.title": "監視ディレクトリで短命なファイルを検知",
		"fimwatch.transient.msg":   "%s が作成され、走査する前に削除されました（内容を確認できないまま消えています）。",
		"fimwatch.degraded.title":  "監視ディレクトリの一部を走査できません",
		"fimwatch.degraded.msg":    "監視ディレクトリ %s の一部を走査できません（上限到達=%v / 大きすぎて除外=%d 件 / 読み取り不能=%d 件）。これらは改ざん検知の対象外です。fim_watch_max_files / fim_watch_max_size_kb / fim_watch_ignore を見直してください。",
		"fimwatch.many.title":      "監視ディレクトリで多数の変更を検知",
		"fimwatch.many.msg":        "監視ディレクトリで %d 件の変更を検知しました。個別通知は %d 件までとし、残り %d 件は要約のみにしました。",

		"listen_port.title": "新規リッスンポートを検知",
		"listen_port.msg":   "%s ポート %d が新たに待ち受けを開始しました（%s）。",

		"suid.new.title":  "新規 %s ファイルを検知",
		"suid.new.msg":    "%s に %s ビット付きのファイルが出現しました（モード %s）。",
		"suid.mode.title": "SUID/SGID のモード変更を検知",
		"suid.mode.msg":   "%s のモードが %s から %s に変わりました。",

		"cron.add.title":            "cron ジョブの追加を検知",
		"cron.add.msg":              "%s が新規に作成されました（永続化の可能性）。",
		"cron.change.title":         "cron ジョブの変更を検知",
		"cron.change.msg":           "%s が変更されました。至急確認してください。",
		"cron.delete.title":         "cron ジョブの削除を検知",
		"cron.delete.msg":           "%s が削除されました。",
		"cron.unreadable.title":     "cron 監視が機能していません",
		"cron.unreadable.msg":       "cron監視が機能していません: %s が読めません（権限不足）。",
		"spoofed.title":             "偽装されたログを検知",
		"spoofed.msg":               "auth.log に偽装された可能性のある行があります（journald の送信元が sshd 以外）: %s",
		"integrity.chain.title":     "セキュリティログの改ざんを検知",
		"integrity.chain.msg":       "セキュリティログ %s のハッシュチェーンが壊れています: %s",
		"integrity.alerthist.title": "アラート履歴の改ざんを検知",
		"integrity.alerthist.msg":   "アラート履歴 %s が不正です: %s",
		"integrity.logtrunc.title":  "セキュリティログの削除を検知",
		"integrity.logtrunc.msg":    "セキュリティログ %s が改ざんされました: %s",
		"integrity.keyperm.title":   "チェーン鍵の権限が緩すぎます",
		"integrity.keyperm.msg":     "チェーン鍵 %s が他ユーザーから読める権限（%s）です。ログを再署名して検証を回避できるため 0600 にしてください。",
		// High-5: 既存ログの検証に失敗して退避したことを黙らずに通知する。
		"integrity.quarantine.title": "セキュリティログの検証失敗（退避して新規開始）",
		"integrity.quarantine.msg":   "セキュリティログ %s のハッシュチェーンを検証できなかったため %s へ退避し、新しいチェーンを開始しました。原因: %s。改ざん、またはチェーン鍵の入れ替えを確認してください。",
		// Medium-7: 鍵が読めないとプラグイン自体が起動しない（監視停止）。
		"integrity.keyfail.title": "セキュリティプラグインが起動できません（チェーン鍵）",
		"integrity.keyfail.msg":   "チェーン鍵 %s を読み込めないため Kizuna-Security を開始できませんでした: %s。鍵の存在と権限（0600 の通常ファイル）を確認してください。この間、改ざん検知は停止しています。",

		"block.dryrun.title":   "ブロック推奨IP（ドライラン）",
		"block.dryrun.msg":     "IP %s をブロック対象と判定しました（%d 回の失敗）。dry-run のため実際にはブロックしていません。",
		"block.fail.title":     "IPブロック失敗",
		"block.fail.msg":       "IP %s のブロックに失敗しました（%s / 権限不足などの可能性）。",
		"block.done.title":     "IPをブロックしました",
		"block.done.msg":       "IP %s を %d 秒間ブロックしました（%s / %d 回の失敗）。",
		"block.restored.title": "ブロック状態を復元しました",
		"block.restored.msg":   "再起動前にブロックしていた IP %s を復元しました（残り %d 秒）。",
	},
	"en": {
		"ssh_login.title":            "SSH login succeeded",
		"ssh_login.msg":              "User %s logged in from %s.",
		"ssh_login.unknown.title":    "SSH login from an unknown IP",
		"ssh_login.unknown.msg":      "User %s logged in from IP %s, which has not been seen before.",
		"ssh_login.burst.title":      "Burst of successful SSH logins",
		"ssh_login.burst.msg":        "IP %s produced %d successful logins in a short window.",
		"ssh_login.burst.known.note": " (Known IP; repeated connections from monitoring tools also produce this burst.)",

		"ssh_failed.title":           "SSH login failed",
		"ssh_failed.msg":             "Login for user %s failed.",
		"ssh_failed.burst.title":     "Repeated SSH login failures",
		"ssh_failed.burst.msg":       "Detected %d failed logins from IP %s (possible brute-force attack).",
		"ssh_failed.sustained.title": "Sustained SSH login failures (low-and-slow brute force)",
		"ssh_failed.sustained.msg":   "Detected %d failed logins from IP %s within %d seconds (possible low-and-slow brute-force attack).",
		"ssh_failed.enum.title":      "SSH username brute force (enumeration)",
		"ssh_failed.enum.msg":        "Detected failed logins for %d distinct usernames from IP %s (possible username enumeration / rotating brute force): %s",
		"ssh_failed.loopback.note":   " The source is loopback (127.0.0.1): the attempt came from an attacker already on the host or through a tunnel to 127.0.0.1, so the source IP does not identify it.",

		"sudo.title":      "sudo executed",
		"sudo.msg":        "%s ran \"%s\" as %s.",
		"sudo.fail.title": "sudo authentication failed",
		"sudo.fail.msg":   "sudo authentication for %s failed.",

		"account.title": "Account operation",

		"install.title":         "Package installed",
		"install.upgrade.title": "Package upgraded",
		"install.remove.title":  "Package removed",
		"install.msg":           "Package %s was %s.",
		"install.verb.install":  "installed",
		"install.verb.upgrade":  "upgraded",
		"install.verb.remove":   "removed",
		"apt.title":             "APT %s",

		"integrity.create.title":        "Critical file created",
		"integrity.create.msg":          "%s was newly created (possible backdoor).",
		"integrity.change.title":        "Critical file modified",
		"integrity.change.msg":          "%s was modified. Investigate immediately.",
		"integrity.delete.title":        "Critical file deleted",
		"integrity.delete.msg":          "%s was deleted.",
		"integrity.baseline.title":      "New watch target added to baseline",
		"integrity.baseline.msg":        "%s was newly adopted into the integrity baseline. Verify this change was intended.",
		"integrity.unreadable.title":    "Watch target unreadable",
		"integrity.unreadable.msg":      "%s could not be read (permissions/deleted); tampering cannot be detected: %s",
		"integrity.unreadable.perm.msg": "%s cannot be read because the account running the agent lacks permission, so tampering with it cannot be detected. Run as root, grant CAP_DAC_READ_SEARCH, or remove it from integrity_files: %s",
		// Aggregated notification (one alert per check, not per file).
		"integrity.unreadable.multi.perm.msg": "%d files cannot be read because the account running the agent lacks permission, so tampering with them cannot be detected. Run as root, grant CAP_DAC_READ_SEARCH, or remove them from integrity_files: %s",
		"integrity.unreadable.multi.msg":      "%d files could not be read (deleted/replaced/permissions); tampering cannot be detected: %s",
		"integrity.baseline.tamper.title":     "FIM baseline tampering detected",
		"integrity.baseline.tamper.msg":       "Signature mismatch for FIM baseline %s (tampering or key change). The baseline was rebuilt: %s",
		// Directory watch (fim_watch): covers staging areas (/tmp and friends)
		// where the file names cannot be listed in advance.
		"fimwatch.create.title":    "New file in a monitored directory",
		"fimwatch.create.msg":      "%s was newly created inside a monitored directory (possible backdoor).",
		"fimwatch.change.title":    "File tampering in a monitored directory",
		"fimwatch.change.msg":      "%s was modified. Investigate immediately.",
		"fimwatch.delete.title":    "File deleted in a monitored directory",
		"fimwatch.delete.msg":      "%s was deleted.",
		"fimwatch.transient.title": "Short-lived file in a monitored directory",
		"fimwatch.transient.msg":   "%s was created and removed before it could be hashed (its content is unknown).",
		"fimwatch.degraded.title":  "Part of a monitored directory cannot be scanned",
		"fimwatch.degraded.msg":    "Part of the monitored directory %s could not be scanned (cap reached=%v / oversized=%d / unreadable=%d). Those files are not covered by tamper detection; review fim_watch_max_files / fim_watch_max_size_kb / fim_watch_ignore.",
		"fimwatch.many.title":      "Many changes in a monitored directory",
		"fimwatch.many.msg":        "Detected %d changes in a monitored directory; per-file notifications are capped at %d, so %d were summarised only.",

		"listen_port.title": "New listening port detected",
		"listen_port.msg":   "%s port %d started listening (%s).",

		"suid.new.title":  "New %s file detected",
		"suid.new.msg":    "A file with the %s bit appeared at %s (mode %s).",
		"suid.mode.title": "SUID/SGID mode change detected",
		"suid.mode.msg":   "Mode of %s changed from %s to %s.",

		"cron.add.title":            "cron job added",
		"cron.add.msg":              "%s was newly created (possible persistence).",
		"cron.change.title":         "cron job modified",
		"cron.change.msg":           "%s was modified. Investigate immediately.",
		"cron.delete.title":         "cron job deleted",
		"cron.delete.msg":           "%s was deleted.",
		"cron.unreadable.title":     "cron monitoring is not working",
		"cron.unreadable.msg":       "cron monitoring is not working: %s cannot be read (insufficient permissions).",
		"spoofed.title":             "Forged log entry detected",
		"spoofed.msg":               "auth.log contains a likely forged line (journald origin is not sshd): %s",
		"integrity.chain.title":     "Security log tampering detected",
		"integrity.chain.msg":       "Hash chain of security log %s is broken: %s",
		"integrity.alerthist.title": "Alert history tampering detected",
		"integrity.alerthist.msg":   "Alert history %s is invalid: %s",
		"integrity.logtrunc.title":  "Security log deletion detected",
		"integrity.logtrunc.msg":    "Security log %s was tampered with: %s",
		"integrity.keyperm.title":   "Chain key permissions are too permissive",
		"integrity.keyperm.msg":     "Chain key %s is readable by other users (mode %s); an attacker could re-sign the log and defeat verification. Set it to 0600.",
		// High-5: report the quarantine instead of silently starting a new chain.
		"integrity.quarantine.title": "Security log failed verification (archived, new chain started)",
		"integrity.quarantine.msg":   "The hash chain of security log %s could not be verified, so it was moved to %s and a new chain was started. Reason: %s. Check for tampering or a rotated chain key.",
		// Medium-7: without the key the plugin cannot start (monitoring stops).
		"integrity.keyfail.title": "Security plugin cannot start (chain key)",
		"integrity.keyfail.msg":   "Kizuna-Security could not start because chain key %s is unreadable: %s. Check that the key exists and is a regular file with mode 0600. Tamper detection is disabled until this is fixed.",

		"block.dryrun.title":   "Block candidate (dry-run)",
		"block.dryrun.msg":     "IP %s was judged a block candidate (%d failures). Not blocked because dry-run is enabled.",
		"block.fail.title":     "Failed to block IP",
		"block.fail.msg":       "Failed to block IP %s (%s / possibly insufficient privileges).",
		"block.done.title":     "IP blocked",
		"block.done.msg":       "Blocked IP %s for %d seconds (%s / %d failures).",
		"block.restored.title": "Block state restored",
		"block.restored.msg":   "Restored block for IP %s from before the restart (%d seconds remaining).",
	},
}
