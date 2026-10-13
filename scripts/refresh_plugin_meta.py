#!/usr/bin/env python3
"""Regenerate a plugin's meta.json from the current .so via plugin-inspect.

Used after the .so is updated in place (outside the dashboard upload flow) so
newly-declared metadata (e.g. dashboard_cards) reaches /api/plugins. Fields
that only the upload path knows (origin, user_label, uploaded_at,
uploader_ip, has_web_ui) are preserved.

Throwaway migration helper; the plugin lives in a separate repo.
"""
import json
import subprocess
import sys

meta_path, so_path = sys.argv[1], sys.argv[2]
inspect_bin = sys.argv[3] if len(sys.argv) > 3 else "/opt/kizuna-eye/bin/plugin-inspect"

out = subprocess.run([inspect_bin, "--so", so_path], capture_output=True, text=True)
if out.returncode != 0:
    print("inspect failed:", out.stderr, file=sys.stderr)
    sys.exit(1)
data = json.loads(out.stdout)
if not data.get("success"):
    print("inspect reported failure:", data.get("error_message"), file=sys.stderr)
    sys.exit(1)

with open(meta_path, encoding="utf-8") as f:
    meta = json.load(f)

# Fields that come from the current .so (overwrite).
for k in ("name", "display_name", "tag", "description", "interval_sec",
          "fields", "is_backup", "dashboard_cards", "web_ui_path"):
    if k in data:
        meta[k] = data[k]
    elif k in ("dashboard_cards", "web_ui_path"):
        # Remove a stale declaration if the new .so no longer declares it.
        meta.pop(k, None)

with open(meta_path, "w", encoding="utf-8") as f:
    json.dump(meta, f, ensure_ascii=False, indent=2)
    f.write("\n")

print("META_UPDATED", json.dumps(meta.get("dashboard_cards"), ensure_ascii=False))
