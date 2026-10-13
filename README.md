# Kizuna-Eye

A lightweight server monitoring tool written in Go, designed for low-spec machines.

**English** | [日本語](README.ja.md)

**Kizuna-Eye** is a lightweight server monitoring tool written in Go, specifically designed for low-spec servers. With a total memory footprint of only **~30–50 MB** for the dashboard and agent combined, it runs smoothly on single-board computers like a Raspberry Pi or older PC hardware.

## Features

- **Lightweight**: Combined footprint of ~30–50 MB (Dashboard + Agent)
- **Real-time updates**: Metric changes are reflected within ~1 second over WebSockets
- **Browser-based configuration**: Full setup and management from the UI, no SSH required
- **Authentication (optional)**: Roles (admin / operator / viewer), bcrypt passwords, session cookies, per-IP login rate limiting
- **Plugin system**: Extend functionality with Go `.so` dynamic plugins
- **Notifications**: Discord, Slack, Telegram, LINE, and Email (SMTP)
- **Dark / light mode** with OS preference detection
- **Process list**: Live processes sorted by CPU or memory
- **Disk S.M.A.R.T**: Disk health and temperature (via `smartctl`, optional)
- **Network bandwidth**: Real-time throughput
- **Metrics history**: In-memory CPU / memory / disk trend for the last ~1 hour
- **Persistent alert history**: Survives restarts, viewable and clearable from the UI
- **Runtime alert thresholds**: Change alert thresholds from the UI without a restart
- **Prometheus metrics**: `GET /api/metrics`
- **Log rotation**: Size-based rotation keeps log files bounded
- **i18n**: Japanese / English UI
- **Self-contained**: No external dependencies; intended for use inside a trusted network / VPN

## Screenshots

<p align="center">
  <img src="docs/images/dashboard_dark.png" alt="Kizuna-Eye dashboard (dark mode)" width="600">
  <br>
  <em>Real-time system monitoring dashboard (dark mode)</em>
</p>

### Theme switch

| Dashboard (Dark) | Dashboard (Light) |
| :---: | :---: |
| <img src="docs/images/dashboard_dark.png" width="350" alt="Dashboard dark mode"> | <img src="docs/images/dashboard_light.png" width="350" alt="Dashboard light mode"> |

### Module management

| Module management (Dark) | Module management (Light) |
| :---: | :---: |
| <img src="docs/images/module_management_dark.png" width="350" alt="Module management dark mode"> | <img src="docs/images/module_management.png" width="350" alt="Module management light mode"> |

## Architecture

Kizuna-Eye has two components:

- **Agent**: Collects system metrics on the monitored server and runs plugins.
- **Dashboard**: Receives metrics from the Agent over WebSocket, serves the web UI, and sends notifications.

The Agent and Dashboard communicate over WebSocket. Plugins are loaded as `.so` shared objects by the Agent. When a plugin reports a status or a security event, the Agent aggregates and forwards it to the Dashboard.

## Requirements

- **Go 1.27.1** or higher
- **Ubuntu Server** 20.04+ (other Linux distributions work too)
- **CGO_ENABLED=1** (required by the `plugin` package)
- **rsync** (when using SSH file transfers)
- **smartctl** (optional, for S.M.A.R.T disk health)

## Security notes

Kizuna-Eye is intended for use inside a trusted network / VPN. The following settings can lead to **effective remote code execution (RCE)** and deserve special care.

- **Do not combine `plugins_upload_enabled: true` with `auth.enabled: false`.** In that combination anyone on the network can upload and run a `.so` plugin and take over the server. Always enable authentication when plugin upload is on (the server also logs a warning at startup).
- **Config files contain secrets.** Never commit `dashboard_config.json` (`agent_token` / `webhook_url`) or `agent_config.json` (`token`); keep them at mode 0600 (they are in `.gitignore`).
- **`/api/config` masks secrets** when returning them. Saving the masked value (`***`) back preserves the existing secret, but treat these files like `users.json`.
- **Only place trusted plugin `.so` files.** A Go plugin runs with the same privileges as the server itself.
- **Automatic updates are a supply-chain risk.** When `auto_update.enabled` is true, the agent watches GitHub Releases and, on a newer version, runs `safe_update.sh` (git pull + rebuild + restart). If the GitHub repository or account is compromised, the server can be made to **build and run attacker code automatically (effective RCE)**. It is **disabled by default**; enable it only for a repository you fully trust, or update manually with `./update.sh`. The agent logs a warning at startup when it is enabled.
- **Notification bodies contain externally derived values.** Every channel escapes and disables mentions.

## Plugin rebuild note (important)

Go's `plugin` package requires the plugin and the host to agree on a **hash of every shared package**. If you change either of the following, you must **rebuild the Agent and all plugins (`.so`) from the same source at the same time**. Rebuilding only one side fails at load time with `plugin was built with a different version of package ...`.

- `pkg/module` (shared plugin types / interfaces: `Module`, `ConfigField`, `SecurityEvent`, ...)
- `pkg/status` (shared types such as `SystemStatus`)

Steps: (1) rebuild the host with `./build.sh`; (2) rebuild each plugin from the same source, e.g. `cd plugins/Kizuna-Security/plugin && GOWORK=off CGO_ENABLED=1 go build -buildmode=plugin -o kizuna_security.so .`; (3) deploy the `.so` into `plugins/` and restart the agent.

## Installation

### Install via APT (recommended, Debian/Ubuntu)

No build required; dependencies are installed automatically.

    # Register the public key (dearmor is required)
    curl -fsSL https://exiserverteam.github.io/Kizuna-Eye_forEN/kizuna.gpg \
      | sudo gpg --dearmor -o /usr/share/keyrings/kizuna.gpg

    # Add the repository
    echo "deb [signed-by=/usr/share/keyrings/kizuna.gpg] https://exiserverteam.github.io/Kizuna-Eye_forEN stable main" \
      | sudo tee /etc/apt/sources.list.d/kizuna.list

    sudo apt update
    sudo apt install kizuna-eye

First access: `http://<host>:8080` → create an admin account at `/setup`.
Uninstall with `sudo apt remove kizuna-eye` (keep data) or `sudo apt purge kizuna-eye` (full removal).

> Built for Ubuntu 20.04+ / Debian 11+.

### Install from source

### 1. Clone the repository

    git clone https://github.com/ExiServerTeam/Kizuna-Eye_forEN.git
    cd Kizuna-Eye_forEN

### 2. Download dependencies

    go mod download

### 3. Build

    ./build.sh

`build.sh` outputs the following binaries to `/opt/kizuna-eye/bin/`:

- `agent_linux`
- `dashboard_linux`
- `plugin-inspect`
- `plugin-sign`

### 4. Prepare configuration files

    cp dashboard_config.example.json dashboard_config.json
    cp agent_config.example.json agent_config.json
    cp modules.json.example modules.json

Edit each file for your environment. The full example files (`*.example.json`) list every option; a minimal setup is shown below.

`dashboard_config.json`:

    {
        "listen_addr": ":8080",
        "log_file": "dashboard.log",
        "static_dir": "./web/static",
        "plugins_dir": "/opt/kizuna-eye/bin/plugins",
        "plugins_upload_enabled": false,
        "alert_history_file": "logs/alert_history.jsonl",
        "auth": {
            "enabled": false,
            "secure_cookies": false,
            "session_ttl_hours": 12,
            "users_file": "users.json",
            "agent_token": "CHANGE_ME_TO_A_LONG_RANDOM_STRING"
        },
        "notifications": {
            "enabled": true,
            "agent_timeout_sec": 30,
            "hold_sec": 5,
            "cooldown_sec": 300,
            "recovery_hold_sec": 30,
            "memory_warn_pct": 70,
            "memory_critical_pct": 85,
            "disk_free_warn_pct": 20,
            "disk_free_critical_pct": 10,
            "cpu_temp_warn_c": 70,
            "cpu_temp_critical_c": 85,
            "notify_recovery": true,
            "discord_max_retries": 5,
            "discord_backoff_max_sec": 60,
            "batch_enabled": true,
            "batch_window_sec": 5,
            "batch_exclude_critical": true,
            "channels": [
                {
                    "type": "discord",
                    "enabled": true,
                    "webhook_url": "https://discord.com/api/webhooks/YOUR_WEBHOOK_ID/YOUR_WEBHOOK_TOKEN"
                }
            ]
        }
    }

Notification reliability: `discord_max_retries` (default 5, `0` disables) retries
HTTP 429/5xx with exponential backoff capped by `discord_backoff_max_sec`
(default 60s). `batch_enabled` groups alerts that arrive within
`batch_window_sec` (default 5s) into one message so a burst does not trip the
rate limit, while `batch_exclude_critical` (default true) still sends critical
alerts immediately.

`agent_config.json`:

    {
        "dashboard_url": "ws://localhost:8080/ws",
        "interval": 1.0,
        "log_file": "logs/agent.log",
        "disk_path": "/",
        "token": "CHANGE_ME_TO_A_LONG_RANDOM_STRING"
    }

`modules.json`:

    [
      {
        "name": "example_plugin",
        "type": "plugin",
        "enabled": true,
        "config": {
          "plugin_path": "/opt/kizuna-eye/bin/plugins/example_plugin.so"
        }
      }
    ]

### 5. Launch

    ./start.sh

Open `http://<server-ip>:8080` in your browser.

## Authentication (optional)

Kizuna-Eye works with or without authentication. Authentication is **disabled by default**. To enable it, set `enabled: true` in the `auth` block of `dashboard_config.json` (`secure_cookies` / `session_ttl_hours` / `agent_token` are also available).

When enabled, the first visit redirects to `/setup.html` to create the first user. **The first user created becomes the administrator.**

### Roles

| Role | Access |
|---|---|
| viewer | Dashboard and logs (read only) |
| operator | + module management, manual backup runs, alert threshold changes |
| admin | Everything (config editor, plugin upload, user management) |

Admins can add or remove users from the "Users" page in the header. Passwords are stored as **bcrypt hashes** in `users.json`, and the last admin cannot be deleted or demoted.

### Agent token

Set `auth.agent_token` in `dashboard_config.json` and `token` in `agent_config.json` to the same value. The Agent authenticates when it opens the WebSocket, so a browser can no longer impersonate it. An empty token falls back to the legacy heuristic detection.

### HTTPS

Never send credentials over plain HTTP. Terminate TLS in a reverse proxy in front of the dashboard:

- Bind the dashboard to localhost (`listen_addr` = `127.0.0.1:8080`)
- Terminate TLS with Caddy or nginx and proxy to `127.0.0.1:8080`
- Set `secure_cookies: true` (the cookie is then only sent over HTTPS)

## Notifications

`notifications.channels` supports `discord`, `slack`, `telegram`, `line`, and `email`. See `dashboard_config.example.json` for the fields each type requires. Alert thresholds can also be changed at runtime from the Config Editor (Admin) without a restart.

## Plugin development

Plugins are built as `.so` shared objects using Go's standard `plugin` package.

Required interfaces:

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

Build:

    CGO_ENABLED=1 go build -buildmode=plugin -o my_plugin.so .

Upload the `.so` from the **Module Management** tab. The bundled `plugin-inspect` tool analyses the plugin, inspects its configurable fields, and builds a dynamic configuration form.

When signatures are required (`plugins.require_signature: true`), create `my_plugin.so.sig` with `plugin-sign -sign my_plugin.so -private-key <key>` and select it together with the `.so` in the same upload dialog. The signature is verified **before** the `.so` is loaded (i.e. before `plugin-inspect` runs the plugin's `init()`).

Optionally implement `DisplayName()` to show a friendly name in the UI badge; otherwise the UI falls back to the plugin name.

### Example plugin

Kizuna-Backup LITE: archive / sync modes, rsync-over-SSH transfer, SHA-256 verification (separate project). It lives in its own repository, so `install.sh` / `update.sh` skip it by default; set `KIZUNA_LITE_PLUGIN_DIR=/path/to/Kizuna-Backup-LITE/plugin` when running them to build and deploy it together with the bundled Security plugin.

## Documentation

- [Procedure manual](docs/PROCEDURE.md) - setup, configuration, operation, and verification
- [Project roadmap](docs/ROADMAP.md) - open issues and priorities
- [Changelog](CHANGELOG.md)

## Testing

    go test ./...

On a Linux host with `gcc`:

    CGO_ENABLED=1 go test -race ./...

Static analysis (recommended before a release):

    go vet ./...
    staticcheck ./...
    govulncheck ./...

## License

MIT License. See [LICENSE](LICENSE) for details.

## Author

sy815twty-spec (Exi Server Team)

- GitHub: https://github.com/sy815twty-spec
- Website: https://exi-server.site/

## Acknowledgments

- [gopsutil](https://github.com/shirou/gopsutil) - system metrics collection
- [gorilla/websocket](https://github.com/gorilla/websocket) - WebSocket implementation
