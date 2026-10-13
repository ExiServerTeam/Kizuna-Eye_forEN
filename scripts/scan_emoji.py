import os, re

# Emoji / pictographic ranges (exclude plain arrows U+2190-21FF and box drawing).
ranges = [
    (0x1F300, 0x1FAFF),  # symbols & pictographs, emoticons, transport, supplemental
    (0x1F000, 0x1F0FF),  # mahjong/cards
    (0x1F1E6, 0x1F1FF),  # regional indicators
    (0x2600, 0x26FF),    # misc symbols
    (0x2700, 0x27BF),    # dingbats
    (0x2B00, 0x2BFF),    # misc symbols and arrows
    (0xFE0F, 0xFE0F),    # variation selector-16
    (0x200D, 0x200D),    # ZWJ
    (0x23E9, 0x23FA),    # media control
]
pat = re.compile('[' + ''.join('\\U%08X-\\U%08X' % r for r in ranges) + ']')

# Keep star comment markers.
skip = {'\u2605', '\u2606'}

out = []
for root, dirs, files in os.walk('.'):
    dirs[:] = [d for d in dirs if d not in ('.git', 'node_modules', '.webcode')]
    for f in files:
        if not f.endswith(('.go', '.js', '.html', '.css', '.md', '.json', '.txt')):
            continue
        p = os.path.join(root, f)
        try:
            with open(p, encoding='utf-8') as fh:
                for i, line in enumerate(fh, 1):
                    chars = pat.findall(line)
                    if chars and any(c not in skip for c in chars):
                        out.append('%s:%d: %s' % (p.replace(os.sep, '/'), i, line.rstrip()[:140]))
        except Exception:
            pass

with open('emoji_report.txt', 'w', encoding='utf-8') as w:
    w.write('\n'.join(out))
    w.write('\n--- total lines: %d ---\n' % len(out))
print('wrote %d lines' % len(out))
