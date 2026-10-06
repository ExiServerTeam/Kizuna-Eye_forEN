# Kizuna-Eye v0.7.0

A lightweight server monitoring tool written in Go, designed for low-spec machines.

This release focuses on **a simpler installation experience**, **APT package distribution**, and a **pre-release security audit pass**.

## Features

- **Lightweight**: ~30–50 MB (Dashboard + Agent)
- **Real-time updates** via WebSockets
- **Browser-based configuration** (no SSH required)
- **Authentication (optional)**: roles (admin / operator / viewer), bcrypt, sessions
- **Plugin system** using Go `.so` dynamic plugins (Ed25519 signed)
- **Notifications**: Discord / Slack / Telegram / LINE / Email
- **Dark / Light mode**
- **Disk S.M.A.R.T**, network bandwidth, metrics history
- **Persistent alert history** with runtime thresholds
- **i18n**: Japanese / English UI (default language selectable at install)

## Requirements

- Go 1.27.1 or higher
- Ubuntu Server 20.04+ / Debian 11+ (other Linux distributions work too)
- CGO_ENABLED=1 (when using the plugin package)

## Installation

### APT (recommended, Debian/Ubuntu)

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

Uninstall: `sudo apt remove kizuna-eye` (keep data) / `sudo apt purge kizuna-eye` (full removal).

### From source

See README.md for details.

## What's Changed

### Added
- **APT package distribution**: install with `apt install kizuna-eye` (GPG-signed repository on GitHub Pages)
- **`uninstall.sh`**: removes services, sudoers, helpers; `--purge` for full removal
- **One-shot `install.sh`**: packages → build → plugin signing → systemd registration → start
- **Plugin signing**: `install.sh` / `update.sh` sign official plugins (Ed25519)
- **UI default language (EN/JA)**: selected at install time (default EN), served via `/lang.js`
- **EN/JA logs** for install / uninstall / migrate / start / stop
- **Secret scanner** (`scripts/check_secrets.sh`) and a static-analysis CI job
- **Static analysis CI**: shellcheck / XSS / i18n / secret checks run on every push

### Changed
- `install.sh` is now idempotent (re-run restarts instead of re-migrating)
- `update.sh` / `safe_update.sh` detect and restart both dashboard and agent
- Dedicated agent user unified to `kizuna-eye` (`kizuna-eye-agent.service`)
- `systemd/kizuna-dashboard.service` gains `SupplementaryGroups=kizuna-eye`
- Executable bits normalized across the tree (`.sh` = 100755, everything else = 100644)

### Fixed
- Build failure on newer releases (Ubuntu 26.04 and later), where `. /etc/os-release` overwrote `VERSION` — Ubuntu 20.04–24.04 were unaffected
- cron / action helper install skipped when run as root
- `update.sh` plugin re-signing looked for keys under `/root/.kizuna-eye`
- Agent binary exec permission (203/EXEC) now re-applied on every install
- `check_i18n.js` failed with ENOENT when run from the repository root
- shellcheck warnings in `apt-repo.sh` / `safe_update.sh`

### Security
- Agent runs as a dedicated user (H-1); keys and state live under `/var/lib/kizuna-eye` (0700)
- cron monitoring reads crontabs directly via `CAP_DAC_READ_SEARCH` (no sudo/sudoers)
- Plugin `.so` Ed25519 signature verification (fail-closed with `require_signature`)
- `restore_file` helper rejects symlinked restore targets and backups (parent verified via `realpath`)
- Agent systemd unit backup uses `mktemp` instead of a predictable `/tmp` name (CWE-59)
- `SECURITY.md`: documents the default `listen_addr`, forbids `git push --mirror`, and defines the secret-rotation policy

## Changelog

See CHANGELOG.md for the full history.

## Attachments

- `kizuna-eye_0.7.0_amd64.deb` — for Ubuntu 20.04+ / Debian 11+
