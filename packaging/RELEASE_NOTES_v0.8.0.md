# Kizuna-Eye v0.8.0 — Release Notes

Released: 2026-10-13

## Highlights

- **Plugin ABI safety**: the host and every plugin must be built under one combined go.work with -trimpath. A host-only rebuild can no longer break plugin.Open with "plugin was built with a different version of package". build-all.sh makes this the default path, and update.sh / scripts/build-plugin.sh follow the same rule.

- **Security hardening**: avatar serving refuses symlinks (CWE-59); the privileged action helper resolves the sudo binary by absolute path; plugin inspection runs under bubblewrap by default (fail-closed).

- **Secure defaults for new installs**: install.sh and the .deb (apt install) enable authentication on a brand-new config (auth.enabled: true, public_viewer: false) and generate a random agent_token. Existing installs are left unchanged (config_version keeps legacy behavior).

- **One-command install**: sudo apt install kizuna-eye performs the entire setup — system user, config generation, systemd units, and the privileged action socket.

- **Bilingual comments**: every comment is now plain Japanese followed by English, across Go, shell, JS/CSS, and systemd units. scripts/check_comments.cjs enforces the convention in CI.

## Added

- Security posture API and dashboard "Security status" section (pkg/insight, GET /api/insight): risk index, source-IP trends, and a config-hardening check. Operator+ only.
- Weekly security report (pkg/report + dashboard scheduler): Markdown briefing from the last 7 days of alert history, sent to a chosen channel (default: email). Opt-in.
- Config snapshots / time travel (pkg/configsnap): GFS-style generations with list and restore endpoints.
- build-all.sh for one-shot host + all-plugin rebuilds.
- scripts/check_comments.cjs bilingual comment checker.
- packaging/release.sh: one command from source to a verified APT repository.

## Changed

- Plugin ABI build model unified to a single combined go.work (host + every plugin) with -trimpath always on.
- Plugins are no longer bundled; install.sh / update.sh treat them as opt-in via KIZUNA_SEC_PLUGIN_DIR / KIZUNA_LITE_PLUGIN_DIR.
- Module card iframes carry the current theme (&theme=dark|light).
- Notification placeholder webhooks fail validation on save and are skipped with a warning on load.
- Repository name references unified to Kizuna-Eye.

## Removed

- Old installer leftovers (install.sh.bak-*) and stale documentation (v0.7.0 release notes, old work records).

## Fixed

- Dashboard unit confinement that made one-click remediation unusable. The privileged action helper was moved to a socket-activated root service (kizuna-action.socket / kizuna-action.service), so the dashboard no longer needs sudo. It therefore keeps full confinement: NoNewPrivileges, ProtectSystem=strict and PrivateTmp are now applied to the dashboard too, the same as the agent.
- install.sh granted the one-click helper sudoers to the wrong user on the source-install path.
- Plugin Name() / Interval() / RequeueSecurityEvents panics could crash the whole agent process.
- Log forwarding (pkg/logger/forward.go) Close/Write race (send-on-closed-channel).
- Backup plugin running state could get stuck permanently after a panic (6-hour stale timeout added).

## Security

- Avatar serving symlink fix (CWE-59).
- Privileged action helper sudo absolute-path resolution.
- Webhook placeholder / fake-Discord detection (12+ repeated characters).
- X-Content-Type-Options: nosniff on the log SSE stream.
- Go toolchain pinned to go1.27.2 (fixes 10 reachable net/http HTTP/2 vulnerabilities in go1.27.1).
- Secure defaults via config_version for new installs.
- Admin-only gating for code-execution-boundary module fields and alert-history clearing.

## Breaking changes

- None. config_version keeps existing configuration working as-is; new installs get the secure defaults.

## Supported

- Ubuntu Server 20.04 LTS and later / Debian 11 and later (amd64).

## Install

    # Register the signing key
    curl -fsSL https://exiserverteam.github.io/Kizuna-Eye/kizuna.gpg | sudo gpg --dearmor -o /usr/share/keyrings/kizuna.gpg
    # Add the repository
    echo "deb [signed-by=/usr/share/keyrings/kizuna.gpg] https://exiserverteam.github.io/Kizuna-Eye stable main" | sudo tee /etc/apt/sources.list.d/kizuna.list
    sudo apt update && sudo apt install kizuna-eye

First access: http://<host>:8080 -> create an admin at /setup.html.

---

See CHANGELOG.md for the full list of changes.
