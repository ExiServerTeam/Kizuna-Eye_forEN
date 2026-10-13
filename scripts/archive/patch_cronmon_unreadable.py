#!/usr/bin/env python3
"""B: warn when a cron watch path exists but cannot be listed."""
import re
import shutil
import sys

PLUGIN_DIR = "/samba/share/Kizuna-Security/plugin"
CRONMON = PLUGIN_DIR + "/cronmon.go"
MESSAGES = PLUGIN_DIR + "/messages.go"

dry = "--dry-run" in sys.argv


def patch_cronmon(src: str) -> str:
    struct_re = re.compile(r"(\twatched\s+map\[string\]bool\n\tinitialized\s+bool\n)\}")
    if not struct_re.search(src):
        raise SystemExit("ERROR: cronmon struct anchor not found")
    src = struct_re.sub(
        r"\1"
        r"\n\t// unreadable remembers watch directories that exist but cannot be\n"
        r"\t// listed (e.g. /var/spool/cron/crontabs, mode drwx-wx--T). Changes\n"
        r"\t// under such a path are invisible, so the operator is warned once\n"
        r"\t// per path instead of the monitoring failing silently.\n"
        r"\tunreadable map[string]bool\n}",
        src, count=1)

    init_re = re.compile(r"(\t\twatched:\s+make\(map\[string\]bool\),\n)(\t\})")
    if not init_re.search(src):
        raise SystemExit("ERROR: NewCronMonitor anchor not found")
    src = init_re.sub(
        r"\1\t\tunreadable:   make(map[string]bool),\n\2", src, count=1)

    check_re = re.compile(
        r"(func \(c \*CronMonitor\) Check\(\) \{\n\tif c == nil \|\| len\(c\.paths\) == 0 \{\n\t\treturn\n\t\}\n)")
    if not check_re.search(src):
        raise SystemExit("ERROR: Check() anchor not found")
    src = check_re.sub(
        r"\1\n\t// Warn if a watch path is unreadable before scanning, so a blind\n"
        r"\t// spot (unreadable cron dir) is reported instead of silent success.\n"
        r"\tc.checkUnreadable()\n",
        src, count=1)

    method = (
        "\n// checkUnreadable emits a warning for each watch directory that exists\n"
        "// but cannot be listed. Such a directory is a blind spot: changes under\n"
        "// it (e.g. a new user crontab) are never detected. The warning is\n"
        "// emitted once per path so it does not spam every scan cycle; if the\n"
        "// path becomes readable again the flag is cleared.\n"
        "func (c *CronMonitor) checkUnreadable() {\n"
        "\tif c == nil {\n"
        "\t\treturn\n"
        "\t}\n"
        "\tfor _, p := range c.paths {\n"
        "\t\tinfo, err := os.Stat(p)\n"
        "\t\tif err != nil || !info.IsDir() {\n"
        "\t\t\tcontinue\n"
        "\t\t}\n"
        "\t\t_, rerr := os.ReadDir(p)\n"
        "\t\tif rerr != nil {\n"
        "\t\t\tif c.unreadable[p] {\n"
        "\t\t\t\tcontinue\n"
        "\t\t\t}\n"
        "\t\t\tc.unreadable[p] = true\n"
        "\t\t\tc.emitFn(module.SecurityEvent{\n"
        "\t\t\t\tCategory:  \"cron\",\n"
        "\t\t\t\tLevel:     \"warning\",\n"
        "\t\t\t\tTitle:     msg(c.lang, \"cron.unreadable.title\"),\n"
        "\t\t\t\tMessage:   msg(c.lang, \"cron.unreadable.msg\", p),\n"
        "\t\t\t\tSource:    p,\n"
        "\t\t\t\tTimestamp: time.Now(),\n"
        "\t\t\t})\n"
        "\t\t\tif c.logger != nil {\n"
        "\t\t\t\tc.logger.Warn(\"Kizuna-Security cron: 監視不能ディレクトリ: %s\", p)\n"
        "\t\t\t}\n"
        "\t\t} else if c.unreadable[p] {\n"
        "\t\t\tdelete(c.unreadable, p)\n"
        "\t\t}\n"
        "\t}\n"
        "}\n"
    )
    src = src.rstrip("\n") + "\n" + method
    return src


def patch_messages(src: str) -> str:
    # Anchor on the KEY only (avoid matching the Japanese string literal,
    # which depends on exact bytes). Insert right after the cron.delete.msg
    # line that belongs to each catalog.
    # ja block: find 'cron.delete.msg' line, insert after it.
    lines = src.split("\n")
    out = []
    ja_done = False
    en_done = False
    for line in lines:
        out.append(line)
        stripped = line.strip()
        if stripped.startswith('"cron.delete.msg"'):
            # Decide ja/en by the presence of Japanese chars in the line.
            is_ja = any('\u3040' <= ch <= '\u30ff' or '\u4e00' <= ch <= '\u9fff' for ch in line)
            indent = line[:len(line) - len(line.lstrip())]
            if is_ja and not ja_done:
                out.append(indent + '"cron.unreadable.title": "cron 監視が機能していません",')
                out.append(indent + '"cron.unreadable.msg":   "cron監視が機能していません: %s が読めません（権限不足）。",')
                ja_done = True
            elif (not is_ja) and not en_done:
                out.append(indent + '"cron.unreadable.title": "cron monitoring is not working",')
                out.append(indent + '"cron.unreadable.msg":   "cron monitoring is not working: %s cannot be read (insufficient permissions).",')
                en_done = True
    if not ja_done:
        raise SystemExit("ERROR: ja cron.delete.msg not found")
    if not en_done:
        raise SystemExit("ERROR: en cron.delete.msg not found")
    return "\n".join(out)


with open(CRONMON, encoding="utf-8") as fh:
    cm_src = fh.read()
with open(MESSAGES, encoding="utf-8") as fh:
    ms_src = fh.read()

cm_new = patch_cronmon(cm_src)
ms_new = patch_messages(ms_src)

if cm_new == cm_src:
    raise SystemExit("ERROR: cronmon.go unchanged")
if ms_new == ms_src:
    raise SystemExit("ERROR: messages.go unchanged")

if dry:
    print("DRY-RUN OK: cronmon.go and messages.go patches matched")
    sys.exit(0)

shutil.copy2(CRONMON, CRONMON + ".prepatch")
shutil.copy2(MESSAGES, MESSAGES + ".prepatch")
with open(CRONMON, "w", encoding="utf-8") as fh:
    fh.write(cm_new)
with open(MESSAGES, "w", encoding="utf-8") as fh:
    fh.write(ms_new)
print("PATCHED: cronmon.go, messages.go")
