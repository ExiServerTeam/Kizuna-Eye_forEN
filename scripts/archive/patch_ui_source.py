#!/usr/bin/env python3
"""Stage1 UI: show a source/trusted badge in renderAlerts."""
import shutil
import sys

APP = "/samba/share/Kizuna-Eye/web/static/app.js"
CSS = "/samba/share/Kizuna-Eye/web/static/style.css"
dry = "--dry-run" in sys.argv


def read(p):
    with open(p, encoding="utf-8") as f:
        return f.read()


def write(p, s):
    with open(p, "w", encoding="utf-8") as f:
        f.write(s)


app = read(APP)
orig_app = app

old = (
    "            const time = a.timestamp ? new Date(a.timestamp).toLocaleString() : '';\n"
    "            html += `\n"
    "                <div class=\"alert-item ${escapeHtml(level)}\">\n"
    "                    <span class=\"alert-level-badge ${escapeHtml(level)}\">${escapeHtml(levelLabel)}</span>\n"
)
new = (
    "            const time = a.timestamp ? new Date(a.timestamp).toLocaleString() : '';\n"
    "            // Source badge: trusted (dashboard-generated) vs untrusted (agent/log-derived).\n"
    "            let srcBadge = '';\n"
    "            if (a.source) {\n"
    "                const trusted = a.trusted === true;\n"
    "                const cls = trusted ? 'trusted' : 'untrusted';\n"
    "                const label = trusted ? t('alerts.source_trusted') : t('alerts.source_untrusted');\n"
    "                srcBadge = `<span class=\"alert-source-badge ${cls}\" title=\"${escapeHtml(a.source)}\">${escapeHtml(label)}</span>`;\n"
    "            }\n"
    "            html += `\n"
    "                <div class=\"alert-item ${escapeHtml(level)}\">\n"
    "                    <span class=\"alert-level-badge ${escapeHtml(level)}\">${escapeHtml(levelLabel)}</span>\n"
    "                    ${srcBadge}\n"
)
if app.count(old) != 1:
    raise SystemExit("ERROR: app.js anchor count=%d" % app.count(old))
app = app.replace(old, new, 1)

css = read(CSS)
orig_css = css
badge_css = (
    "\n/* Phase V5: alert source badge (trusted=dashboard / untrusted=agent) */\n"
    ".alert-source-badge {\n"
    "    display: inline-flex;\n"
    "    align-items: center;\n"
    "    padding: 2px 8px;\n"
    "    border-radius: 8px;\n"
    "    font-size: 0.68rem;\n"
    "    font-weight: 700;\n"
    "    white-space: nowrap;\n"
    "}\n"
    ".alert-source-badge.trusted {\n"
    "    background: rgba(34, 197, 94, 0.18);\n"
    "    color: #22c55e;\n"
    "}\n"
    ".alert-source-badge.untrusted {\n"
    "    background: rgba(234, 179, 8, 0.20);\n"
    "    color: #eab308;\n"
    "}\n"
)
if "alert-source-badge" not in css:
    css = css.rstrip("\n") + "\n" + badge_css

if app == orig_app and css == orig_css:
    raise SystemExit("ERROR: no change")

if dry:
    print("DRY-RUN OK: app.js + style.css badge patch matched")
    sys.exit(0)

shutil.copy2(APP, APP + ".prepatch-src")
shutil.copy2(CSS, CSS + ".prepatch-src")
write(APP, app)
write(CSS, css)
print("PATCHED: app.js, style.css")
