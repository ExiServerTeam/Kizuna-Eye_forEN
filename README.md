# Kizuna-Eye

A lightweight server monitoring tool written in Go, designed for low-spec machines.

**English** | [日本語](README.ja.md)

**Kizuna-Eye** is a lightweight server monitoring tool written in Go, specifically designed for low-spec servers. With a total memory footprint of only **~30–50 MB** for both the dashboard and agent combined, it runs smoothly on single-board computers like Raspberry Pi or older PC hardware.

## Features

- **Lightweight**: Combined footprint of ~30–50 MB (Dashboard + Agent)
- **Real-time Updates**: Reflects metric changes within 1 second via WebSockets
- **Browser-based Configuration**: Full setup and management directly from the UI without SSH
- **Plugin System**: Extend functionality using Go `.so` dynamic plugins
- **SNS Notifications**: Integrated alerts for Discord, Telegram, and LINE
- **Dark/Light Mode**: Seamless theme switching according to your preference
- **Process List**: Live process monitoring sorted by CPU or memory usage
- **Disk S.M.A.R.T**: Monitor disk health status and temperature
- **Network Bandwidth**: Real-time throughput metrics
- **Cron-style Scheduler**: Flexible execution scheduling for plugins

## Screenshots

<p align="center">
  <img src="docs/images/dashboard_dark.png.png" alt="Kizuna-Eye Dashboard Dark Mode" width="600">
  <br>
  <em>Real-time System Monitoring Dashboard (Dark Mode)</em>
</p>

### Theme Switch

| Dashboard (Dark) | Dashboard (Light) |
| :---: | :---: |
| <img src="docs/images/dashboard_dark.png.png" width="350" alt="Dashboard Dark Mode"> | <img src="docs/images/dashboard_light.png.png" width="350" alt="Dashboard Light Mode"> |

### Module Management

| Module Management (Dark) | Module Management (Light) |
| :---: | :---: |
| <img src="docs/images/module_management_dark.png.png" width="350" alt="Module Management Dark Mode"> | <img src="docs/images/module_management.png.png" width="350" alt="Module Management Light Mode"> |

## Architecture

Kizuna-Eye consists of two main components:

- **Agent**: Collects system metrics and executes plugins on the monitored server.
- **Dashboard**: Receives metrics from the Agent via WebSocket, provides a web UI, and sends notifications.

The Agent and Dashboard communicate over WebSocket. Plugins are loaded as `.so` shared objects by the Agent. When a plugin reports a backup status, the Agent aggregates it and forwards it to the Dashboard.

## Requirements

- **Go 1.27.1** or higher
- **Ubuntu Server** 20.04+ (compatible with other Linux distributions)
- **CGO_ENABLED=1** (when using the `plugin` package)
- **rsync** (when using SSH file transfers)
- **smartctl** (optional, for S.M.A.R.T disk health monitoring)

## Installation

### 1. Clone the repository

```bash
git clone https://github.com/sy815twty-spec/Kizuna-Eye.git
cd Kizuna-Eye
2. Download dependencies
bash
go mod download
3. Build
bash
./build.sh
Executing build.sh builds and outputs the following binaries to /opt/kizuna-eye/bin/:

agent_linux

dashboard_linux

plugin-inspect

4. Prepare Configuration Files
bash
cp dashboard_config.example.json dashboard_config.json
cp agent_config.example.json agent_config.json
cp modules.json.example modules.json
Edit each file as needed to fit your environment settings.

5. Launch
bash
./start.sh
Open your browser and navigate to http://<server-ip>:8080.

Configuration
dashboard_config.json
json
{
    "listen_addr": ":8080",
    "log_file": "dashboard.log",
    "static_dir": "./web/static",
    "plugins_dir": "/opt/kizuna-eye/bin/plugins",
    "notifications": {
        "enabled": true,
        "agent_timeout_sec": 30,
        "hold_sec": 5,
        "cooldown_sec": 300,
        "memory_warn_pct": 70,
        "memory_critical_pct": 85,
        "disk_free_warn_pct": 20,
        "disk_free_critical_pct": 10,
        "cpu_temp_warn_c": 70,
        "cpu_temp_critical_c": 85,
        "notify_recovery": true,
        "channels": [
            {
                "type": "discord",
                "enabled": true,
                "webhook_url": "https://discord.com/api/webhooks/YOUR_WEBHOOK_ID/YOUR_WEBHOOK_TOKEN"
            }
        ]
    }
}
agent_config.json
json
{
    "dashboard_url": "ws://localhost:8080/ws",
    "interval": 1.0,
    "log_file": "logs/agent.log",
    "disk_path": "/"
}
modules.json
json
[
  {
    "name": "kizuna_backup_lite",
    "type": "plugin",
    "enabled": true,
    "config": {
      "plugin_path": "/opt/kizuna-eye/bin/plugins/kizuna_backup_lite.so",
      "mode": "archive",
      "target_dir": "/path/to/backup/target",
      "interval_sec": 3600,
      "schedule_expr": "",
      "run_on_start": false,
      "local_temp_dir": "/tmp/kizuna-backups",
      "log_path": "./logs/kizuna-backup-lite.log",
      "keep_local": false,
      "rotation_keep": 5,
      "dry_run": false
    }
  }
]
SSH Transfer (mode: sync)
When mode is set to sync, Kizuna-Backup LITE uses rsync to transfer backups to a remote host over SSH.

json
{
  "mode": "sync",
  "remote_dest": "user@192.168.0.100",
  "remote_dir": "/backup/kizuna",
  "ssh_port": 22,
  "ssh_key": "/home/user/.ssh/id_ed25519"
}
Requirements:

SSH key-based authentication (password auth is not supported in automated mode)

rsync installed on both local and remote hosts

Remote directory must exist and be writable

Schedule Configuration
Kizuna-Backup LITE supports two scheduling modes:

1. Fixed Interval (interval_sec):

json
{
  "interval_sec": 3600
}
2. Cron Expression (schedule_expr, takes precedence over interval_sec):

json
{
  "schedule_expr": "0 3 * * *"
}
Cron examples:

Expression	Meaning
0 3 * * *	Every day at 3:00 AM
*/15 * * * *	Every 15 minutes
0 0 * * 0	Every Sunday at midnight
0 9,18 * * 1-5	Weekdays at 9:00 AM and 6:00 PM
Plugin Development
Plugins for Kizuna-Eye are built as .so shared objects using Go's standard plugin package.

Required Interfaces
go
type Module interface {
    Name() string
    Description() string
    Interval() time.Duration
    Run(ctx context.Context) error
    Init(ctx context.Context) error
}

type ConfigProvider interface {
    GetConfigFields() []ConfigField
}

type DisplayNameProvider interface {
    DisplayName() string
}
Building a Plugin
bash
CGO_ENABLED=1 go build -buildmode=plugin -o my_plugin.so .
Uploading a Plugin
Upload your .so binary through the Module Management tab on the web dashboard. The embedded plugin-inspect tool will automatically analyze the plugin, inspect configurable fields, and construct a dynamic configuration form.

Optional: Display Name
Implement DisplayName() to show a friendly name in the UI badge:

go
func (p *MyPlugin) DisplayName() string {
    return "My Custom Plugin"
}
If not implemented, the UI falls back to the plugin name.

License
MIT License. See LICENSE for details.

Author
sy815twty-spec (Exi Server Team)

GitHub: https://github.com/sy815twty-spec

Website: https://exi-server.site/

Acknowledgments
gopsutil - System metrics collection

gorilla/websocket - WebSocket implementation

robfig/cron - Cron expression parser & runner
