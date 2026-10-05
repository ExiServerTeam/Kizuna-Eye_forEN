#!/usr/bin/env python3
"""A: read /var/spool/cron/crontabs via the read-only sudo helper, and stop
silently absorbing a file seen for the first time under a watched dir."""
import re
import shutil
import sys

CRONMON = "/samba/share/Kizuna-Security/plugin/cronmon.go"
dry = "--dry-run" in sys.argv

with open(CRONMON, encoding="utf-8") as fh:
    src = fh.read()
orig = src

def rep(old, new, label):
    global src
    if src.count(old) != 1:
        raise SystemExit("ERROR: anchor not unique/found (%d): %s" % (src.count(old), label))
    src = src.replace(old, new, 1)

# 1) imports
old = 'import (\n\t"encoding/json"\n\t"io/fs"\n\t"os"\n\t"path/filepath"\n\t"sync"\n\t"time"\n\n\t"Kizuna-Eye/pkg/module"\n)'
new = 'import (\n\t"context"\n\t"encoding/json"\n\t"io/fs"\n\t"os"\n\t"os/exec"\n\t"path/filepath"\n\t"strings"\n\t"sync"\n\t"time"\n\n\t"Kizuna-Eye/pkg/module"\n)'
rep(old, new, "imports")

# 2) scan(): use the helper for the crontabs dir
old = ('\t\tif info.IsDir() {\n'
       '\t\t\t_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {\n'
       '\t\t\t\tif err != nil || d.IsDir() {\n'
       '\t\t\t\t\treturn nil\n'
       '\t\t\t\t}\n'
       '\t\t\t\tif h, herr := hashFile(path); herr == nil {\n'
       '\t\t\t\t\tcurrent[path] = h\n'
       '\t\t\t\t}\n'
       '\t\t\t\treturn nil\n'
       '\t\t\t})\n'
       '\t\t\tcontinue\n'
       '\t\t}')
new = ('\t\tif info.IsDir() {\n'
       '\t\t\t// The crontabs directory is mode drwx-wx--T (root:crontab), so the\n'
       '\t\t\t// agent cannot list it. Read it through the read-only sudo helper\n'
       '\t\t\t// instead; if that fails, fall back to the (blind) walk and the\n'
       '\t\t\t// unreadable warning in checkUnreadable().\n'
       '\t\t\tif p == cronSudoDir {\n'
       '\t\t\t\tif entries, herr := readCronViaSudo(); herr == nil {\n'
       '\t\t\t\t\tfor name, h := range entries {\n'
       '\t\t\t\t\t\tcurrent[filepath.Join(p, name)] = h\n'
       '\t\t\t\t\t}\n'
       '\t\t\t\t\tcontinue\n'
       '\t\t\t\t}\n'
       '\t\t\t}\n'
       '\t\t\t_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {\n'
       '\t\t\t\tif err != nil || d.IsDir() {\n'
       '\t\t\t\t\treturn nil\n'
       '\t\t\t\t}\n'
       '\t\t\t\tif h, herr := hashFile(path); herr == nil {\n'
       '\t\t\t\t\tcurrent[path] = h\n'
       '\t\t\t\t}\n'
       '\t\t\t\treturn nil\n'
       '\t\t\t})\n'
       '\t\t\tcontinue\n'
       '\t\t}')
rep(old, new, "scan-dir")

# 3) report a file first seen under a watched dir (regex)
pat = re.compile(r"if !existed \{\n(\s*)if !c\.watched\[p\] \{\n")
if not pat.search(src):
    raise SystemExit("ERROR: existed regex not found")
src = pat.sub(
    "if !existed {\n"
    "// A file that appears under a watched directory (a new\n"
    "// /etc/cron.d entry, or a new user crontab) is a new cron\n"
    "// entry and must be announced. The old check only looked at\n"
    "// c.watched[p], which is false for a file seen for the very\n"
    "// first time, so such a file was absorbed into the baseline\n"
    "// silently and never reported.\n"
    "if !c.watched[p] && !c.underWatchedDir(p) {\n",
    src, count=1)

# 4) checkUnreadable(): skip warning when helper covers crontabs dir (regex)
pat4 = re.compile(
    r"(_, rerr := os\.ReadDir\(p\)\n\s*if rerr != nil \{\n)")
if not pat4.search(src):
    raise SystemExit("ERROR: checkUnreadable regex not found")
src = pat4.sub(
    r"\1"
    "\t\t\t\t// For the crontabs directory the sudo helper may still cover\n"
    "\t\t\t\t// it, in which case monitoring is not actually blind.\n"
    "\t\t\t\tif p == cronSudoDir {\n"
    "\t\t\t\t\tif _, herr := readCronViaSudo(); herr == nil {\n"
    "\t\t\t\t\t\tif c.unreadable[p] {\n"
    "\t\t\t\t\t\t\tdelete(c.unreadable, p)\n"
    "\t\t\t\t\t\t}\n"
    "\t\t\t\t\t\tcontinue\n"
    "\t\t\t\t\t}\n"
    "\t\t\t\t}\n",
    src, count=1)

# 5) append helper constants and functions
method = '''
// cronSudoDir is the per-user crontab directory. It is mode drwx-wx--T
// (root:crontab) and cannot be listed by the agent user.
const cronSudoDir = "/var/spool/cron/crontabs"

// cronSudoHelper is the read-only helper allowed via sudoers
// (/etc/sudoers.d/kizuna-security-cron). It prints "<name>\\t<sha256>" lines.
const cronSudoHelper = "/usr/local/bin/kizuna-cron-read.sh"

// readCronViaSudo runs the read-only helper with sudo -n and parses its
// output. It returns base name -> sha256. Any error (helper missing, sudo
// denied, timeout) is returned so the caller can fall back to the blind walk
// and the unreadable warning.
func readCronViaSudo() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", "-n", cronSudoHelper).Output()
	if err != nil {
		return nil, err
	}
	entries := make(map[string]string)
	for _, line := range strings.Split(string(out), "\\n") {
		line = strings.TrimRight(line, "\\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, h := parts[0], parts[1]
		if name == "" || h == "" {
			continue
		}
		// A helper must never be able to inject a path: only a plain file
		// name is accepted.
		if strings.ContainsAny(name, "/\\\\") || name == "." || name == ".." {
			continue
		}
		entries[name] = h
	}
	return entries, nil
}

// underWatchedDir reports whether p is a file directly under one of the
// configured watch directories.
func (c *CronMonitor) underWatchedDir(p string) bool {
	dir := filepath.Dir(p)
	for _, w := range c.paths {
		if w == dir {
			return true
		}
	}
	return false
}
'''
src = src.rstrip("\n") + "\n" + method

if src == orig:
    raise SystemExit("ERROR: no change")

if dry:
    print("DRY-RUN OK: cronmon.go A-patch matched (5 edits)")
    sys.exit(0)

shutil.copy2(CRONMON, CRONMON + ".prepatch-a")
with open(CRONMON, "w", encoding="utf-8") as fh:
    fh.write(src)
print("PATCHED: cronmon.go")
