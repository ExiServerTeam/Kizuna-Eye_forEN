#!/usr/bin/env python3
"""Fix the FIM self-deadlock: lang()/SetLang() re-lock f.mu which Check()
already holds, hanging the plugin run loop (Go mutexes are not reentrant).
Move language guarding to a dedicated langMu."""
import re
import shutil
import sys

DEFAULT_PATH = "/samba/share/Kizuna-Security/plugin/fim.go"

# Parse args: --dry-run is a flag; a non-flag arg is the target path.
dry = "--dry-run" in sys.argv
paths = [a for a in sys.argv[1:] if not a.startswith("--")]
path = paths[0] if paths else DEFAULT_PATH

with open(path, encoding="utf-8") as fh:
    src = fh.read()
orig = src

# 1) add langMu field to the FIM struct.
struct_re = re.compile(r"(\n\tlanguage\s+string[^\n]*\n)\}")
if not struct_re.search(src):
    print("ERROR: struct anchor not found")
    sys.exit(2)
src = struct_re.sub(
    r"\1\n\t// langMu guards language only. It must be separate from mu: Check()\n"
    r"\t// holds mu while emitting events that read the language, so sharing mu\n"
    r"\t// would make lang() re-lock a mutex Check() already holds (Go mutexes\n"
    r"\t// are not reentrant) and deadlock the plugin's run loop.\n"
    r"\tlangMu sync.RWMutex\n}",
    src, count=1)

# 2) lang(): use langMu instead of mu.
lang_re = re.compile(r"\tf\.mu\.Lock\(\)\n\tdefer f\.mu\.Unlock\(\)\n\tif f\.language == \"\" \{")
if not lang_re.search(src):
    print("ERROR: lang anchor not found")
    sys.exit(3)
src = lang_re.sub("\tf.langMu.RLock()\n\tdefer f.langMu.RUnlock()\n\tif f.language == \"\" {", src, count=1)

# 3) SetLang(): use langMu instead of mu.
set_re = re.compile(r"\tf\.mu\.Lock\(\)\n\tf\.language = lang\n\tf\.mu\.Unlock\(\)")
if not set_re.search(src):
    print("ERROR: SetLang anchor not found")
    sys.exit(4)
src = set_re.sub("\tf.langMu.Lock()\n\tf.language = lang\n\tf.langMu.Unlock()", src, count=1)

if src == orig:
    print("ERROR: no change produced")
    sys.exit(5)

if dry:
    print("DRY-RUN OK: 3 replacements matched for", path)
    sys.exit(0)

shutil.copy2(path, path + ".bak-deadlock")
with open(path, "w", encoding="utf-8") as fh:
    fh.write(src)
print("PATCHED:", path)
