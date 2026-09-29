// ============================================================
// Kizuna-Eye - 多言語対応（i18n）
// ============================================================
(function () {
    'use strict';

    const VERSION = 'v0.7.1';

    const DICT = {
        ja: {
            // --- nav ---
            'nav.dashboard': 'ダッシュボード',
            'nav.modules': 'モジュール管理',
            'nav.config': '設定エディタ',
            'nav.logs': 'ログ',
            'nav.users': 'ユーザー',
            'nav.login': 'ログイン',
            'account.guest': 'ゲスト',
            'account.logout': 'ログアウト',
            'account.change_icon': 'アイコンを変更',
            'account.change_pw': 'パスワードを変更',

            // --- theme / lang ---
            'theme.dark': 'ダーク',
            'theme.light': 'ライト',
            'theme.toggle': 'テーマ切り替え',
            'lang.label': 'JA',
            'lang.toggle': '言語切り替え',

            // --- connection ---
            'status.connected': '接続中',
            'status.connecting': '接続中...',
            'status.disconnected': '切断',

            // --- dashboard cards ---
            'card.cpu': 'CPU',
            'card.memory': 'メモリ',
            'card.reorder': 'ドラッグして並べ替え',
            'card.storage': 'ストレージ',
            'card.uptime': '稼働時間',
            'card.usage': '使用率 {0}%',
            'card.free': '空き {0}',
            'card.free.unknown': '空き --',
            'card.temp.na': 'N/A℃',
            'card.temp.na.tooltip': 'センサーの初期化中、または非対応の温度センサーです',

            // --- uptime ---
            'uptime.boot_time': '起動日時',
            'uptime.load_avg': '平均負荷',
            'uptime.network': 'ネットワーク',
            'uptime.current_time': '現在時刻: {0}',
            'uptime.days_hours': '{0}日{1}時間',
            'uptime.hours_minutes': '{0}時間{1}分',
            'uptime.minutes_seconds': '{0}分{1}秒',
            'uptime.seconds': '{0}秒',

            // --- disk ---
            'disk.health.ok': '正常',
            'disk.health.fail': '異常',
            'disk.health.unknown': '不明',
            'disk.free': '空き {0}',

            // --- storage (CrystalDiskInfo 風モーダル) ---
            'storage.modal_title': 'ストレージ詳細',
            'storage.col_path': 'マウント',
            'storage.col_model': '型番',
            'storage.col_serial': 'シリアル',
            'storage.col_health': '健康状態',
            'storage.col_temp': '温度',
            'storage.col_write': '総書込量',
            'storage.col_power_on': '通電時間',
            'storage.col_capacity': '容量',
            'storage.col_usage': '使用率',
            'storage.none': 'ディスク情報がありません',
            'storage.not_available': '未取得',
            'storage.power_on_hours': '{0} 時間',
            'storage.hdd': 'HDD {0}rpm',
            'storage.ssd': 'SSD',
            'storage.smart_unavailable': 'smartctl 未検出のため S.M.A.R.T 情報は取得できません',

            // --- plugin section ---
            'plugin.section_title': 'プラグインステータス',
            'plugin.empty': 'プラグインが登録されていません',
            'plugin.last_run': '最終実行',
            'plugin.next_run': '次回実行',
            'plugin.standby': '待機中',
            'plugin.status_label': '状態',
            'plugin.not_run': '未実行',
            'plugin.not_acquired': '未取得',
            'plugin.module_count': 'モジュール数',
            'plugin.list_title': '登録プラグイン',
            'plugin.enabled': '有効',
            'plugin.disabled': '無効',
            'plugin.none': 'プラグインが登録されていません',
            'alerts.title': 'アラート履歴',
            'alerts.empty': 'アラートはありません',
            'alerts.clear': 'クリア',
            'alerts.clear_confirm': 'アラート履歴をすべて削除します。よろしいですか？',
            'alerts.cleared': 'アラート履歴を削除しました',
            'alerts.clear_failed': 'アラート履歴の削除に失敗しました',
            'history.title': 'メトリクス履歴',
            'history.waiting': 'データ収集中...',
            'logs.title': 'ログ',
            'logs.desc': 'Agent / Dashboard のログを表示します。',
            'logs.follow_off': '自動更新: OFF',
            'logs.follow_on': '自動更新: ON',
            'logs.reload': '再読み込み',
            'logs.loading': '読み込み中...',
            'logs.lines': '行数',
            'logs.no_logs': '(ログはありません)',
            'config.sidebar_title': '設定ファイル',
            'config.insert_template': 'ひな形を挿入',
            'config.format': 'フォーマット',
            'config.validate': '検証',
            'config.save': '保存',
            'config.valid_json': '有効なJSON',
            'config.invalid_json': '無効なJSON',
            'config.chars': '{0} 文字',
            'config.lines': '{0} 行',
            'config.confirm_replace': '現在の内容をひな形で置き換えます。よろしいですか？',
            'config.no_template': 'この設定のひな形はありません',
            'config.template_inserted': 'ひな形を挿入しました',
            'config.formatted': 'JSONをフォーマットしました',
            'config.saved': '設定を保存しました',
            'config.load_failed': '設定の読み込みに失敗しました',
            'config.save_failed': '設定の保存に失敗しました',
            'config.placeholder': '設定ファイルのJSONをここに編集...',
            'config.alerts_title': 'アラートの設定',
            'config.memory_warn': 'メモリ警告閾値 (%)',
            'config.memory_critical': 'メモリ危険閾値 (%)',
            'config.disk_warn': 'ディスク空き警告閾値 (%)',
            'config.disk_critical': 'ディスク空き危険閾値 (%)',
            'config.cpu_temp_warn': 'CPU温度警告閾値 (℃)',
            'config.cpu_temp_critical': 'CPU温度危険閾値 (℃)',
            'config.notify_recovery': '復旧通知を送信する',
            'config.mode_gui': 'GUI',
            'config.mode_json': 'JSON',
            'config.channels': 'Discord 通知先',
            'config.channel_not_editable': 'この通知種別はこの画面では編集できません（設定は保持されます）。',
            'config.add_channel': 'Discord 通知先を追加',
            'config.channel_remove': '削除',
            'config.modules_gui_note': 'モジュール（プラグイン）は「モジュール管理」画面で追加・編集・削除できます。ここでは JSON 編集のみ対応しています。',
            'config.modules_gui_note_short': 'モジュールはモジュール管理画面で編集してください',
            'config.loading': '読み込み中...',
            'config.file_agent': 'エージェントの設定',
            'config.file_dashboard': 'ダッシュボードの設定',
            'config.file_modules': 'プラグインの設定',
            'config.file_sub_agent': 'agent_config.json',
            'config.file_sub_dashboard': 'dashboard_config.json',
            'config.file_sub_modules': 'modules.json',
            'config.gui_help_agent': 'エージェント（監視される側）の設定です。各項目に値を入力し、最後に「保存」を押してください。',
            'config.gui_help_dashboard': 'ダッシュボード（監視する側）の設定です。各項目に値を入力し、最後に「保存」を押してください。',

            // --- config editor GUI schema ---
            'config.g.agent.conn': '接続先',
            'config.g.agent.behavior': '動作',
            'config.g.agent.log': 'ログと監視対象',
            'config.f.dashboard_url': 'ダッシュボードのURL',
            'config.h.dashboard_url': '監視サーバーのアドレス。通常は「ws://ホスト名:8080/ws」の形式です。',
            'config.f.interval': 'データを送る間隔（秒）',
            'config.h.interval': 'データを送る間隔です（1秒以上を指定してください）。',
            'config.f.agent_log_file': 'ログの保存先',
            'config.h.agent_log_file': '空の場合は logs/agent.log に保存されます。',
            'config.f.disk_path': '監視するディスクのパス',
            'config.h.disk_path': '空き容量を監視したいディスクのパス（例: /）。',
            'config.g.dash.server': 'サーバー設定',
            'config.g.dash.auth': '認証・公開ビューア',
            'config.g.dash.log': 'ログ',
            'config.g.dash.history': 'アラート履歴',
            'config.g.dash.notif_toggle': '通知のON/OFF',
            'config.g.dash.notif_timing': '通知のタイミング',
            'config.g.dash.thresholds': 'アラートのしきい値',
            'config.f.listen_addr': '待ち受けアドレス',
            'config.h.listen_addr': 'ダッシュボードが接続を受け付けるアドレス（例: :8080）。',
            'config.f.static_dir': '画面ファイルの場所',
            'config.h.static_dir': '通常は変更不要です。',
            'config.f.plugins_dir': 'プラグインの保存先',
            'config.h.plugins_dir': '空の場合は実行ファイルと同じ場所の plugins フォルダを使用します。',
            'config.c.plugins_upload_enabled': '画面からプラグインをアップロードできるようにする',
            'config.c.public_viewer': 'ゲストログインを許可する（ログインなしで CPU/メモリ/ディスク使用率を閲覧）',
            'config.f.dash_log_file': 'ログの保存先',
            'config.h.dash_log_file': '空の場合は標準出力に表示されます。',
            'config.f.log_level': 'ログの詳しさ',
            'config.h.log_level': 'debug が最も詳しく、error が最も少ない設定です。',
            'config.f.alert_history_file': 'アラート履歴の保存先',
            'config.h.alert_history_file': '空の場合は logs/alert_history.jsonl に保存されます。',
            'config.c.notif_enabled': 'アラートを通知する',
            'config.f.agent_timeout_sec': 'Agent の応答タイムアウト（秒）',
            'config.h.agent_timeout_sec': 'この時間を超えて応答がないと「Agent 応答なし」と判定します。',
            'config.f.hold_sec': '異常が続いたら通知するまでの時間（秒）',
            'config.h.hold_sec': '一時的な数値の跳ね上がりで通知しないための待ち時間です。',
            'config.f.cooldown_sec': '同じ通知を繰り返さない時間（秒）',
            'config.h.cooldown_sec': '一度通知したあと、この時間は同じ通知を出しません。',
            'config.f.recovery_hold_sec': '復旧通知を出すまでの時間（秒）',
            'config.f.memory_warn_pct': 'メモリ使用率 警告（%）',
            'config.f.memory_critical_pct': 'メモリ使用率 危険（%）',
            'config.f.disk_free_warn_pct': 'ディスク空き容量 警告（%）',
            'config.h.disk_free_warn_pct': '空き容量がこの割合を下回ると警告します。',
            'config.f.disk_free_critical_pct': 'ディスク空き容量 危険（%）',
            'config.f.cpu_temp_warn_c': 'CPU温度 警告（℃）',
            'config.f.cpu_temp_critical_c': 'CPU温度 危険（℃）',
            'config.c.notify_recovery': '復旧したときも通知する',
            'config.f.webhook_url': 'Discord の Webhook URL',
            'config.h.webhook_url': 'Discord のサーバー設定 → 連携サービス → ウェブフック で取得した URL を貼り付けてください。',
            'config.gui_help_modules': 'プラグイン（バックアップなど）の設定です。各プラグインの項目を入力し、最後に「保存」を押してください。',
            'config.module_enabled': 'このプラグインを有効にする',
            'config.module_advanced': '詳細（JSON）',
            'config.module_no_schema': 'このプラグインは入力項目を自動表示できません。JSON を直接編集してください。',
            'config.modules_empty': '登録されているプラグインはありません。',
            'common.auto': '自動',
            'modules.group_basic': '基本設定',
            'modules.plugin_config_heading': 'プラグイン設定',

            // --- statuses ---
            'status.success': '成功',
            'status.ok': '正常',
            'status.completed': '完了',
            'status.running': '実行中',
            'status.processing': '処理中',
            'status.alert': '警告',
            'status.warning': '警告',
            'status.failed': '失敗',
            'status.error': 'エラー',
            'status.critical': '重大',
            'status.unknown': '不明',

            // --- process modal ---
            'process.title_cpu': 'プロセス一覧 (Top 10 / CPU順)',
            'process.title_mem': 'プロセス一覧 (Top 10 / メモリ順)',
            'process.search_placeholder': 'プロセス名で検索...',
            'process.sort_cpu': 'CPU順',
            'process.sort_mem': 'メモリ順',
            'process.col_pid': 'PID',
            'process.col_name': '名前',
            'process.col_cpu': 'CPU',
            'process.col_mem': 'メモリ',
            'process.col_user': 'ユーザー',
            'process.col_freq': '{0}',
            'process.empty_search': '該当するプロセスがありません',
            'process.empty_waiting': 'データ待機中...',
            'process.hint_cpu': 'Top 10 / CPU降順 / 3秒ごと更新',
            'process.hint_mem': 'Top 10 / メモリ降順 / 3秒ごと更新',
            'process.close': '閉じる',
            'process.card_hint': 'クリックで詳細',

            // --- toast ---
            'toast.mem_high': 'メモリ使用率が {0}% に達しました',
            'toast.disk_low': 'ストレージの空きが {0}% を切りました',
            'toast.cpu_temp_high': 'CPU温度が {0}℃ に達しました',
            'toast.cpu_high': 'CPU使用率が {0}% に達しました',
            'toast.disconnected': 'サーバーとの接続が切断されました。再接続を試みています...',
            'toast.reconnected': 'サーバーに再接続しました',

            // --- footer ---
            'footer.text': 'Kizuna-Eye {0} · リアルタイムシステムモニタリング',

            // --- modules page ---
            'modules.page_title': 'Kizuna-Eye - モジュール管理',
            'modules.heading': 'モジュール管理',
            'modules.page_desc': 'モジュールを追加・編集・削除できます。',
            'modules.load_json': 'JSON読み込み',
            'modules.add_plugin': 'プラグイン追加',
            'modules.export': '書き出し',
            'modules.total': '全モジュール',
            'modules.enabled': '有効',
            'modules.disabled': '無効',
            'modules.loading': '読み込み中...',
            'modules.empty_title': 'モジュールがありません',
            'modules.empty_desc': '「プラグイン追加」ボタンからモジュールを追加してください',
            'modules.add_plugin_empty': 'プラグイン追加',

            'modules.badge_enabled': '有効',
            'modules.badge_disabled': '無効',
            'modules.btn_run': '▶ 実行',
            'modules.btn_enable': '有効化',
            'modules.btn_disable': '無効化',
            'toast.module_enabled': 'モジュールを有効化しました',
            'toast.module_disabled': 'モジュールを無効化しました',
            'modules.btn_open': '🌐 開く',
            'modules.btn_edit': '編集',
            'modules.btn_delete': '削除',
            'modules.label_last_run': '最終実行',
            'modules.label_status': '状態',
            'modules.label_size': 'サイズ',
            'modules.label_mode': 'モード',
            'modules.label_target': '対象ディレクトリ',
            'modules.label_remote': 'リモート接続先',
            'modules.label_interval': 'チェック間隔: {0}秒',
            'modules.label_log': 'ログ: {0}',
            'security.label_state': '監視状態',
            'security.state_active': '監視中',
            'security.state_inactive': '停止中',
            'security.label_watch': '監視対象',
            'security.label_notify': '通知レベル',
            'security.label_burst': '失敗多発しきい値',
            'security.label_last_detect': '最終検知',
            'security.burst_value': '{0} 回 / {1} 秒',

            'modules.modal_title_edit': 'モジュール編集',
            'modules.form_name': 'モジュール名',
            'modules.form_name_hint': '英数字とアンダースコア（_）が使えます',
            'modules.form_name_placeholder': '例: backup_production',
            'modules.form_type': 'バックアップ種類',
            'modules.form_type_pro': 'PRO',
            'modules.form_type_pro_desc': '設定ファイル（INI）あり・cron対応',
            'modules.form_type_lite': 'LITE',
            'modules.form_type_lite_desc': 'シンプル・CLI引数方式',
            'modules.form_config_path': '設定ファイルパス',
            'modules.form_config_hint': 'PRO版のみ必要です。空の場合は自動検出を試みます',
            'modules.form_log_path': 'ログファイルパス',
            'modules.form_log_hint': '空の場合は自動で検出します',
            'modules.form_interval': 'チェック間隔（秒）',
            'modules.form_interval_hint': '10〜3600秒（1時間）の範囲で指定してください',
            'modules.form_target': '対象ディレクトリ（表示用）',
            'modules.form_target_hint': 'バックアップ対象ディレクトリ（監視画面に表示されます）',
            'modules.form_remote': 'リモート接続先（表示用）',
            'modules.form_remote_hint': 'バックアップ送信先（監視画面に表示されます）',
            'modules.form_enable': 'このモジュールを有効にする',

            'modules.plugin_config_title': 'プラグイン設定: {0}',
            'modules.plugin_config_empty': 'このプラグインは設定項目を申告していません。',

            'modules.delete_title': 'モジュール削除',
            'modules.delete_message': '本当にモジュール「{0}」を削除しますか？',
            'modules.delete_warning': 'この操作は元に戻せません。',

            'common.cancel': 'キャンセル',
            'common.save': '保存',
            'common.delete': '削除する',

            // --- users ---
            'users.title': 'ユーザー管理',
            'users.add': '+ ユーザーを追加',
            'users.col_username': 'ユーザー名',
            'users.col_role': 'ロール',
            'users.col_created': '作成日時',
            'users.col_actions': '操作',
            'users.change_pw': 'PW変更',
            'users.delete': '削除',
            'users.add_title': 'ユーザーを追加',
            'users.username_hint': '英数字・_ . - の3〜32文字',
            'users.password': 'パスワード',
            'users.password_hint': '8文字以上',
            'users.role': 'ロール',
            'users.role_viewer': 'viewer（閲覧のみ）',
            'users.role_operator': 'operator（運用操作）',
            'users.role_admin': 'admin（全権）',
            'users.create': '作成',
            'users.prompt_new_pw': '{0} の新しいパスワード（8文字以上）',
            'users.pw_changed': 'パスワードを変更しました',
            'users.confirm_delete': '{0} を削除しますか？',
            'users.load_failed': '読み込みに失敗しました',
            'users.need_admin': '管理者権限が必要です',
            'users.change_failed': '変更に失敗しました',
            'users.delete_failed': '削除に失敗しました',
            'users.create_failed': '作成に失敗しました',
            'users.network_error': '通信エラー',

            'toast.modules_load_fail': 'モジュール一覧の取得に失敗しました',
            'toast.modules_load_fail_msg': 'モジュール読み込みに失敗しました: {0}',
            'toast.module_saved': 'モジュールを更新しました',
            'toast.module_added': 'モジュールを追加しました',
            'toast.save_fail': '保存に失敗しました: {0}',
            'toast.module_deleted': 'モジュール「{0}」を削除しました',
            'toast.delete_fail': '削除に失敗しました: {0}',
            'toast.invalid_input': '入力内容にエラーがあります',
            'toast.json_loaded': '設定ファイルを読み込みました（{0} モジュール）',
            'toast.json_invalid': '無効なフォーマットです（配列が必要です）',
            'toast.json_parse_error': 'JSONパースエラー: {0}',
            'toast.file_read_error': 'ファイル読み込みエラー',
            'toast.no_modules': 'モジュールがありません',
            'toast.export_done': '設定ファイルを書き出しました',
            'toast.plugin_uploading': 'プラグインをアップロード中...',
            'toast.plugin_registered': 'プラグイン「{0}」を登録しました',
            'toast.plugin_inspect_error': '検証エラー: {0}',
            'toast.plugin_load_fail': 'プラグイン読み込み失敗: {0}',
            'toast.plugin_name_invalid': 'プラグイン名に使用できるのは英数字・_・- のみです',
            'toast.plugin_name_prompt': 'プラグイン名を入力してください（英数字・_・-）',
            'toast.plugin_meta_missing': 'プラグインのメタ情報が見つかりません。再アップロードしてください',
            'toast.execution_accepted': '実行を受け付けました',
            'toast.execution_in_progress': '実行中です。完了までお待ちください。',
            'toast.execution_success': '成功 ({0}, {1}ms)',
            'toast.execution_failed': '失敗: {0}',
            'toast.execution_error': '実行失敗: {0}',
            'toast.execution_timeout': 'タイムアウト（結果が返ってきませんでした）',
            'toast.plugin_config_saved': 'プラグイン設定を保存しました',
            'toast.plugin_config_meta_missing': 'プラグインのメタ情報が見つかりません',
            'toast.required_field': '入力エラー: {0} は必須です',

            'validate.name_required': 'モジュール名は必須です',
            'validate.name_format': '英数字とアンダースコアのみ使用できます',
            'validate.interval_range': '10〜3600の範囲で指定してください',

            'error.title': '読み込みエラー',
        },
        en: {
            'nav.dashboard': 'Dashboard',
            'nav.modules': 'Modules',
            'nav.config': 'Config Editor',
            'nav.logs': 'Logs',
            'nav.users': 'Users',
            'nav.login': 'Log in',
            'account.guest': 'Guest',
            'account.logout': 'Log out',
            'account.change_icon': 'Change icon',
            'account.change_pw': 'Change password',

            'theme.dark': 'Dark',
            'theme.light': 'Light',
            'theme.toggle': 'Toggle theme',
            'lang.label': 'EN',
            'lang.toggle': 'Switch language',

            'status.connected': 'Connected',
            'status.connecting': 'Connecting...',
            'status.disconnected': 'Disconnected',

            'card.cpu': 'CPU',
            'card.memory': 'Memory',
            'card.reorder': 'Drag to reorder',
            'card.storage': 'Storage',
            'card.uptime': 'Uptime',
            'card.usage': 'Usage {0}%',
            'card.free': 'Free {0}',
            'card.free.unknown': 'Free --',
            'card.temp.na': 'N/A℃',
            'card.temp.na.tooltip': 'Sensor is initializing or unsupported',

            'uptime.boot_time': 'Boot Time',
            'uptime.load_avg': 'Load Average',
            'uptime.network': 'Network',
            'uptime.current_time': 'Current Time: {0}',
            'uptime.days_hours': '{0}d {1}h',
            'uptime.hours_minutes': '{0}h {1}m',
            'uptime.minutes_seconds': '{0}m {1}s',
            'uptime.seconds': '{0}s',

            'disk.health.ok': 'OK',
            'disk.health.fail': 'FAIL',

            // --- storage (CrystalDiskInfo-style modal) ---
            'storage.modal_title': 'Storage Details',
            'storage.col_path': 'Mount',
            'storage.col_model': 'Model',
            'storage.col_serial': 'Serial',
            'storage.col_health': 'Health',
            'storage.col_temp': 'Temp',
            'storage.col_write': 'Total Written',
            'storage.col_power_on': 'Power-On',
            'storage.col_capacity': 'Capacity',
            'storage.col_usage': 'Usage',
            'storage.none': 'No disk information',
            'storage.not_available': 'N/A',
            'storage.power_on_hours': '{0} h',
            'storage.hdd': 'HDD {0}rpm',
            'storage.ssd': 'SSD',
            'storage.smart_unavailable': 'smartctl not found; S.M.A.R.T data unavailable',
            'disk.health.unknown': 'N/A',
            'disk.free': 'Free {0}',

            'plugin.section_title': 'Plugin Status',
            'plugin.empty': 'No plugins registered',
            'plugin.last_run': 'Last Run',
            'plugin.next_run': 'Next Run',
            'plugin.standby': 'Standby',
            'plugin.status_label': 'Status',
            'plugin.not_run': 'Not run',
            'plugin.not_acquired': 'N/A',
            'plugin.module_count': 'Modules',
            'plugin.list_title': 'Registered Plugins',
            'plugin.enabled': 'Enabled',
            'plugin.disabled': 'Disabled',
            'plugin.none': 'No plugins registered',
            'alerts.title': 'Alert History',
            'alerts.empty': 'No alerts',
            'alerts.clear': 'Clear',
            'alerts.clear_confirm': 'Delete all alert history?',
            'alerts.cleared': 'Alert history cleared',
            'alerts.clear_failed': 'Failed to clear alert history',
            'history.title': 'Metrics History',
            'history.waiting': 'Collecting data...',
            'logs.title': 'Logs',
            'logs.desc': 'View Agent / Dashboard logs.',
            'logs.follow_off': 'Auto-refresh: OFF',
            'logs.follow_on': 'Auto-refresh: ON',
            'logs.reload': 'Reload',
            'logs.loading': 'Loading...',
            'logs.lines': 'Lines',
            'logs.no_logs': '(no logs)',
            'config.sidebar_title': 'Config Files',
            'config.insert_template': 'Insert Template',
            'config.format': 'Format',
            'config.validate': 'Validate',
            'config.save': 'Save',
            'config.valid_json': 'Valid JSON',
            'config.invalid_json': 'Invalid JSON',
            'config.chars': '{0} chars',
            'config.lines': '{0} lines',
            'config.confirm_replace': 'Replace the current content with the template?',
            'config.no_template': 'No template available for this config',
            'config.template_inserted': 'Template inserted',
            'config.formatted': 'Formatted JSON',
            'config.saved': 'Config saved',
            'config.load_failed': 'Failed to load config',
            'config.save_failed': 'Failed to save config',
            'config.placeholder': 'Edit the config JSON here...',
            'config.alerts_title': 'Alert Thresholds',
            'config.memory_warn': 'Memory Warn (%)',
            'config.memory_critical': 'Memory Critical (%)',
            'config.disk_warn': 'Disk Free Warn (%)',
            'config.disk_critical': 'Disk Free Critical (%)',
            'config.cpu_temp_warn': 'CPU Temp Warn (℃)',
            'config.cpu_temp_critical': 'CPU Temp Critical (℃)',
            'config.notify_recovery': 'Send recovery notifications',
            'config.mode_gui': 'GUI',
            'config.mode_json': 'JSON',
            'config.channels': 'Discord Notifications',
            'config.channel_not_editable': 'This channel type cannot be edited here (its settings are preserved).',
            'config.add_channel': 'Add Discord webhook',
            'config.channel_remove': 'Remove',
            'config.modules_gui_note': 'Modules (plugins) can be added, edited, and deleted on the Modules page. Only JSON editing is available here.',
            'config.modules_gui_note_short': 'Edit modules on the Modules page',
            'config.loading': 'Loading...',
            'config.file_agent': 'Agent Settings',
            'config.file_dashboard': 'Dashboard Settings',
            'config.file_modules': 'Plugin Settings',
            'config.file_sub_agent': 'agent_config.json',
            'config.file_sub_dashboard': 'dashboard_config.json',
            'config.file_sub_modules': 'modules.json',
            'config.gui_help_agent': 'Settings for the agent (the monitored side). Fill in each field and press Save at the bottom.',
            'config.gui_help_dashboard': 'Settings for the dashboard (the monitoring side). Fill in each field and press Save at the bottom.',

            // --- config editor GUI schema ---
            'config.g.agent.conn': 'Connection',
            'config.g.agent.behavior': 'Behavior',
            'config.g.agent.log': 'Logs and targets',
            'config.f.dashboard_url': 'Dashboard URL',
            'config.h.dashboard_url': 'Address of the monitoring server. Usually ws://host:8080/ws.',
            'config.f.interval': 'Send interval (seconds)',
            'config.h.interval': 'How often data is sent (1 second or more).',
            'config.f.agent_log_file': 'Log file',
            'config.h.agent_log_file': 'Defaults to logs/agent.log when empty.',
            'config.f.disk_path': 'Disk path to monitor',
            'config.h.disk_path': 'Path of the disk whose free space is monitored (e.g. /).',
            'config.g.dash.server': 'Server settings',
            'config.g.dash.auth': 'Auth / public viewer',
            'config.g.dash.log': 'Log',
            'config.g.dash.history': 'Alert history',
            'config.g.dash.notif_toggle': 'Notifications on/off',
            'config.g.dash.notif_timing': 'Notification timing',
            'config.g.dash.thresholds': 'Alert thresholds',
            'config.f.listen_addr': 'Listen address',
            'config.h.listen_addr': 'Address the dashboard accepts connections on (e.g. :8080).',
            'config.f.static_dir': 'Static files directory',
            'config.h.static_dir': 'Usually no change needed.',
            'config.f.plugins_dir': 'Plugins directory',
            'config.h.plugins_dir': 'When empty, the plugins folder next to the executable is used.',
            'config.c.plugins_upload_enabled': 'Allow plugin upload from the UI',
            'config.c.public_viewer': 'Allow guest login (view CPU/memory/disk usage without logging in)',
            'config.f.dash_log_file': 'Log file',
            'config.h.dash_log_file': 'When empty, logs go to standard output.',
            'config.f.log_level': 'Log level',
            'config.h.log_level': 'debug is most verbose, error the least.',
            'config.f.alert_history_file': 'Alert history file',
            'config.h.alert_history_file': 'Defaults to logs/alert_history.jsonl when empty.',
            'config.c.notif_enabled': 'Send alerts as notifications',
            'config.f.agent_timeout_sec': 'Agent response timeout (seconds)',
            'config.h.agent_timeout_sec': 'No response within this time is treated as "agent offline".',
            'config.f.hold_sec': 'Delay before notifying (seconds)',
            'config.h.hold_sec': 'Wait time so a momentary spike does not trigger a notification.',
            'config.f.cooldown_sec': 'Repeat suppression window (seconds)',
            'config.h.cooldown_sec': 'After notifying once, the same alert is not sent again within this time.',
            'config.f.recovery_hold_sec': 'Delay before recovery notice (seconds)',
            'config.f.memory_warn_pct': 'Memory usage warn (%)',
            'config.f.memory_critical_pct': 'Memory usage critical (%)',
            'config.f.disk_free_warn_pct': 'Disk free warn (%)',
            'config.h.disk_free_warn_pct': 'Warns when free space falls below this percentage.',
            'config.f.disk_free_critical_pct': 'Disk free critical (%)',
            'config.f.cpu_temp_warn_c': 'CPU temp warn (C)',
            'config.f.cpu_temp_critical_c': 'CPU temp critical (C)',
            'config.c.notify_recovery': 'Also notify on recovery',
            'config.f.webhook_url': 'Discord webhook URL',
            'config.h.webhook_url': 'Paste the webhook URL from Discord (Server Settings -> Integrations -> Webhooks).',
            'config.gui_help_modules': 'Settings for plugins (e.g. backup). Fill in each plugin and press Save at the bottom.',
            'config.module_enabled': 'Enable this plugin',
            'config.module_advanced': 'Advanced (JSON)',
            'config.module_no_schema': 'This plugin cannot show fields automatically. Edit the JSON directly.',
            'config.modules_empty': 'No plugins registered.',
            'common.auto': 'Auto',
            'modules.group_basic': 'Basic Settings',
            'modules.plugin_config_heading': 'Plugin Settings',

            'status.success': 'Success',
            'status.ok': 'OK',
            'status.completed': 'Completed',
            'status.running': 'Running',
            'status.processing': 'Processing',
            'status.alert': 'Alert',
            'status.warning': 'Warning',
            'status.failed': 'Failed',
            'status.error': 'Error',
            'status.critical': 'Critical',
            'status.unknown': 'Unknown',

            'process.title_cpu': 'Process List (Top 10 / by CPU)',
            'process.title_mem': 'Process List (Top 10 / by Memory)',
            'process.search_placeholder': 'Search by process name...',
            'process.sort_cpu': 'CPU',
            'process.sort_mem': 'Memory',
            'process.col_pid': 'PID',
            'process.col_name': 'Name',
            'process.col_cpu': 'CPU',
            'process.col_mem': 'Memory',
            'process.col_user': 'User',
            'process.col_freq': '{0}',
            'process.empty_search': 'No matching processes',
            'process.empty_waiting': 'Waiting for data...',
            'process.hint_cpu': 'Top 10 / CPU desc / refresh every 3s',
            'process.hint_mem': 'Top 10 / Memory desc / refresh every 3s',
            'process.close': 'Close',
            'process.card_hint': 'Click for details',

            'toast.mem_high': 'Memory usage reached {0}%',
            'toast.disk_low': 'Storage free space dropped below {0}%',
            'toast.cpu_temp_high': 'CPU temperature reached {0}℃',
            'toast.cpu_high': 'CPU usage reached {0}%',
            'toast.disconnected': 'Connection to the server was lost. Reconnecting...',
            'toast.reconnected': 'Reconnected to the server',

            'footer.text': 'Kizuna-Eye {0} · Real-time System Monitoring',

            'modules.page_title': 'Kizuna-Eye - Module Management',
            'modules.heading': 'Module Management',
            'modules.page_desc': 'Add, edit, and delete modules.',
            'modules.load_json': 'Load JSON',
            'modules.add_plugin': 'Add Plugin',
            'modules.export': 'Export',
            'modules.total': 'Total',
            'modules.enabled': 'Enabled',
            'modules.disabled': 'Disabled',
            'modules.loading': 'Loading...',
            'modules.empty_title': 'No modules',
            'modules.empty_desc': 'Add a module from the "Add Plugin" button',
            'modules.add_plugin_empty': 'Add Plugin',

            'modules.badge_enabled': 'Enabled',
            'modules.badge_disabled': 'Disabled',
            'modules.btn_run': '▶ Run',
            'modules.btn_enable': 'Enable',
            'modules.btn_disable': 'Disable',
            'toast.module_enabled': 'Module enabled',
            'toast.module_disabled': 'Module disabled',
            'modules.btn_open': '🌐 Open',
            'modules.btn_edit': 'Edit',
            'modules.btn_delete': 'Delete',
            'modules.label_last_run': 'Last Run',
            'modules.label_status': 'Status',
            'modules.label_size': 'Size',
            'modules.label_mode': 'Mode',
            'modules.label_target': 'Target Directory',
            'modules.label_remote': 'Remote Destination',
            'modules.label_interval': 'Interval: {0}s',
            'modules.label_log': 'Log: {0}',
            'security.label_state': 'Monitor',
            'security.state_active': 'Active',
            'security.state_inactive': 'Stopped',
            'security.label_watch': 'Watching',
            'security.label_notify': 'Notify level',
            'security.label_burst': 'Failed burst threshold',
            'security.label_last_detect': 'Last detection',
            'security.burst_value': '{0} times / {1} sec',

            'modules.modal_title_edit': 'Edit Module',
            'modules.form_name': 'Module Name',
            'modules.form_name_hint': 'Letters, digits, and underscores (_) only',
            'modules.form_name_placeholder': 'e.g. backup_production',
            'modules.form_type': 'Backup Type',
            'modules.form_type_pro': 'PRO',
            'modules.form_type_pro_desc': 'Config file (INI) / cron support',
            'modules.form_type_lite': 'LITE',
            'modules.form_type_lite_desc': 'Simple / CLI arguments',
            'modules.form_config_path': 'Config File Path',
            'modules.form_config_hint': 'PRO only. Leave blank for auto-detection',
            'modules.form_log_path': 'Log File Path',
            'modules.form_log_hint': 'Leave blank for auto-detection',
            'modules.form_interval': 'Check Interval (sec)',
            'modules.form_interval_hint': 'Range: 10-3600 seconds (1 hour)',
            'modules.form_target': 'Target Directory (display)',
            'modules.form_target_hint': 'Backup target directory (shown on dashboard)',
            'modules.form_remote': 'Remote Destination (display)',
            'modules.form_remote_hint': 'Backup destination (shown on dashboard)',
            'modules.form_enable': 'Enable this module',

            'modules.plugin_config_title': 'Plugin Settings: {0}',
            'modules.plugin_config_empty': 'This plugin declares no config fields.',

            'modules.delete_title': 'Delete Module',
            'modules.delete_message': 'Are you sure you want to delete module "{0}"?',
            'modules.delete_warning': 'This action cannot be undone.',

            'common.cancel': 'Cancel',
            'common.save': 'Save',
            'common.delete': 'Delete',

            // --- users ---
            'users.title': 'User Management',
            'users.add': '+ Add User',
            'users.col_username': 'Username',
            'users.col_role': 'Role',
            'users.col_created': 'Created',
            'users.col_actions': 'Actions',
            'users.change_pw': 'Change PW',
            'users.delete': 'Delete',
            'users.add_title': 'Add User',
            'users.username_hint': '3-32 chars: letters, digits, _ . -',
            'users.password': 'Password',
            'users.password_hint': 'At least 8 characters',
            'users.role': 'Role',
            'users.role_viewer': 'viewer (read only)',
            'users.role_operator': 'operator (operations)',
            'users.role_admin': 'admin (full access)',
            'users.create': 'Create',
            'users.prompt_new_pw': 'New password for {0} (at least 8 chars)',
            'users.pw_changed': 'Password changed',
            'users.confirm_delete': 'Delete {0}?',
            'users.load_failed': 'Failed to load',
            'users.need_admin': 'Administrator permission required',
            'users.change_failed': 'Failed to change',
            'users.delete_failed': 'Failed to delete',
            'users.create_failed': 'Failed to create',
            'users.network_error': 'Network error',

            'toast.modules_load_fail': 'Failed to fetch module list',
            'toast.modules_load_fail_msg': 'Failed to load modules: {0}',
            'toast.module_saved': 'Module updated',
            'toast.module_added': 'Module added',
            'toast.save_fail': 'Save failed: {0}',
            'toast.module_deleted': 'Module "{0}" deleted',
            'toast.delete_fail': 'Delete failed: {0}',
            'toast.invalid_input': 'There are input errors',
            'toast.json_loaded': 'Config loaded ({0} modules)',
            'toast.json_invalid': 'Invalid format (array required)',
            'toast.json_parse_error': 'JSON parse error: {0}',
            'toast.file_read_error': 'File read error',
            'toast.no_modules': 'No modules',
            'toast.export_done': 'Config exported',
            'toast.plugin_uploading': 'Uploading plugin...',
            'toast.plugin_registered': 'Plugin "{0}" registered',
            'toast.plugin_inspect_error': 'Inspect error: {0}',
            'toast.plugin_load_fail': 'Plugin load failed: {0}',
            'toast.plugin_name_invalid': 'Plugin name may only contain letters, digits, _ and -',
            'toast.plugin_name_prompt': 'Enter plugin name (letters, digits, _ or -)',
            'toast.plugin_meta_missing': 'Plugin metadata not found. Please re-upload.',
            'toast.execution_accepted': 'Execution accepted',
            'toast.execution_in_progress': 'Already running. Please wait until it finishes.',
            'toast.execution_success': 'Success ({0}, {1}ms)',
            'toast.execution_failed': 'Failed: {0}',
            'toast.execution_error': 'Execution failed: {0}',
            'toast.execution_timeout': 'Timeout (no result returned)',
            'toast.plugin_config_saved': 'Plugin settings saved',
            'toast.plugin_config_meta_missing': 'Plugin metadata not found',
            'toast.required_field': 'Input error: {0} is required',

            'validate.name_required': 'Module name is required',
            'validate.name_format': 'Only letters, digits, and underscores are allowed',
            'validate.interval_range': 'Specify a value between 10 and 3600',

            'error.title': 'Load Error',
        },
    };

    const STORAGE_KEY = 'kizuna-lang';

    function detectLang() {
        try {
            const url = new URL(window.location.href);
            const param = url.searchParams.get('lang');
            if (param && DICT[param]) return param;
        } catch (e) {}

        try {
            const saved = localStorage.getItem(STORAGE_KEY);
            if (saved && DICT[saved]) return saved;
        } catch (e) {}

        const nav = (navigator.language || 'ja').toLowerCase();
        if (nav.startsWith('ja')) return 'ja';
        if (nav.startsWith('en')) return 'en';
        return 'ja';
    }

    let currentLang = detectLang();

    function t(key) {
        const dict = DICT[currentLang] || DICT.ja;
        let str = dict[key];
        if (str === undefined) {
            str = DICT.ja[key];
            if (str === undefined) return key;
        }
        const args = Array.prototype.slice.call(arguments, 1);
        args.forEach(function (val, i) {
            // Use a function replacement so "$" in val (e.g. $&, $1) is treated
            // literally instead of as a special replacement pattern.
            str = str.replace(new RegExp('\\{' + i + '\\}', 'g'), function () { return String(val); });
        });
        return str;
    }

    function getLang() {
        return currentLang;
    }

    function setLang(lang) {
        if (!DICT[lang]) return;
        currentLang = lang;
        try {
            localStorage.setItem(STORAGE_KEY, lang);
        } catch (e) {}
        document.documentElement.setAttribute('lang', lang);
        applyI18n();
        window.dispatchEvent(new CustomEvent('kizuna-lang-change', { detail: { lang: lang } }));
    }

    function toggleLang() {
        setLang(currentLang === 'ja' ? 'en' : 'ja');
    }

    function applyI18n(root) {
        const scope = root || document;

        scope.querySelectorAll('[data-i18n]').forEach(function (el) {
            const key = el.getAttribute('data-i18n');
            if (key) el.textContent = t(key);
        });
        scope.querySelectorAll('[data-i18n-placeholder]').forEach(function (el) {
            const key = el.getAttribute('data-i18n-placeholder');
            if (key) el.setAttribute('placeholder', t(key));
        });
        scope.querySelectorAll('[data-i18n-title]').forEach(function (el) {
            const key = el.getAttribute('data-i18n-title');
            if (key) el.setAttribute('title', t(key));
        });
        scope.querySelectorAll('[data-i18n-aria-label]').forEach(function (el) {
            const key = el.getAttribute('data-i18n-aria-label');
            if (key) el.setAttribute('aria-label', t(key));
        });
    }

    function updateLangLabel() {
        const el = document.getElementById('langLabel');
        if (el) el.textContent = currentLang === 'ja' ? 'JA' : 'EN';
    }

    function bindLangToggle() {
        const btn = document.getElementById('langToggle');
        if (!btn) return;
        btn.addEventListener('click', toggleLang);
    }

    function init() {
        document.documentElement.setAttribute('lang', currentLang);
        applyI18n();
        bindLangToggle();
        updateLangLabel();
        window.addEventListener('kizuna-lang-change', updateLangLabel);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    window.KizunaI18n = {
        t: t,
        getLang: getLang,
        setLang: setLang,
        toggleLang: toggleLang,
        applyI18n: applyI18n,
        VERSION: VERSION,
    };
    window.t = t;

})();