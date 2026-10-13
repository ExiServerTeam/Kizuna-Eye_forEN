#!/usr/bin/env node
/*
 * Kizuna-Eye: バイリンガルコメント検査。英語のみのコメントブロックを検出する。
 * Kizuna-Eye: bilingual comment checker. Reports English-only comment blocks.
 *
 * 使い方 / Usage:
 *   node scripts/check_comments.cjs --strict --no-tests cmd internal pkg
 *   node scripts/check_comments.cjs --lang js web/static
 *   node scripts/check_comments.cjs --lang sh .
 */
'use strict';
const fs = require('fs');
const path = require('path');

const argv = process.argv.slice(2);
const strict = argv.includes('--strict');
const noTests = argv.includes('--no-tests');
let lang = 'go';
const li = argv.indexOf('--lang');
if (li >= 0 && argv[li + 1]) lang = argv[li + 1];
const roots = argv.filter((a, i) => !a.startsWith('--') && argv[i - 1] !== '--lang');

// 日本語文字（ひらがな/カタカナ/漢字）を含むか。バックスラッシュ不要の判定。
// Whether the text contains Japanese (kana/kanji). Backslash-free test.
function hasJA(s) {
  for (const ch of s) {
    const c = ch.codePointAt(0);
    if ((c >= 0x3040 && c <= 0x30ff) || (c >= 0x3400 && c <= 0x4dbf) || (c >= 0x4e00 && c <= 0x9fff)) return true;
  }
  return false;
}
function hasEN(s) { return /[A-Za-z]/.test(s); }

const CONF = {
  go: { skip: ['.git', 'vendor', 'node_modules'], file: (n) => n.endsWith('.go') },
  js: { skip: ['.git', 'node_modules', 'vendor'], file: (n) => n.endsWith('.js') || n.endsWith('.css') },
  sh: { skip: ['.git', '.gnupg-apt', 'apt-repo', 'node_modules', 'vendor', 'tmp', 'dist'], file: (n) => n.endsWith('.sh') },
};
const cfg = CONF[lang];
if (!cfg) { console.error('unknown --lang ' + lang + ' (go|js|sh)'); process.exit(2); }

// コメント行かどうか（js は /* ... */ の継続行も含む）と、除外行かを返す。
// Returns isComment (js includes /* */ continuation lines) and exempt flags.
function analyze(lines) {
  const n = lines.length;
  const isC = new Array(n).fill(false);
  const ex = new Array(n).fill(false);
  let inBlock = false;
  for (let i = 0; i < n; i++) {
    const raw = lines[i];
    const t = raw.trimStart();
    if (lang === 'go') {
      if (t.startsWith('//')) {
        isC[i] = true;
        const b = t.slice(2).trimStart();
        ex[i] = b.startsWith('go:') || b.startsWith('+build') || b.startsWith('nolint') || b.startsWith('go:generate') || b.startsWith('Code generated') || b.startsWith('SPDX-') || b.startsWith('!') || b === '';
      }
    } else if (lang === 'sh') {
      if (t.startsWith('#')) {
        isC[i] = true;
        const b = t.slice(1).trimStart();
        ex[i] = b.startsWith('!') || b.startsWith('shellcheck');
      }
    } else {
      if (inBlock) {
        isC[i] = true;
        if (raw.indexOf('*/') >= 0) inBlock = false;
      } else if (t.startsWith('//')) {
        isC[i] = true;
      } else if (t.startsWith('/*')) {
        isC[i] = true;
        if (raw.indexOf('*/') < 0) inBlock = true;
      }
    }
  }
  return { isC, ex };
}

const search = roots.length ? roots : (lang === 'js' ? ['web/static'] : lang === 'sh' ? ['.'] : ['cmd', 'internal', 'pkg']);

function walk(dir, out) {
  let ents;
  try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
  for (const e of ents) {
    if (cfg.skip.includes(e.name)) continue;
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (cfg.file(e.name)) out.push(p);
  }
}

const files = [];
for (const r of search) {
  let st; try { st = fs.statSync(r); } catch { continue; }
  if (st.isDirectory()) walk(r, files); else files.push(r);
}

let offenders = 0;
let total = 0;
const perFile = {};
const detail = [];
for (const f of files) {
  if (noTests && f.endsWith('_test.go')) continue;
  let lines;
  try { lines = fs.readFileSync(f, 'utf8').split(/\r?\n/); } catch { continue; }
  const { isC, ex } = analyze(lines);
  let block = [];
  let start = 0;
  const flush = () => {
    if (block.length === 0) return;
    const allEx = block.every((x) => ex[x] || lines[x].trim() === '');
    const text = block.map((x) => lines[x]).join('\n');
    if (!allEx && hasEN(text)) {
      total += 1;
      if (!hasJA(text)) {
        offenders += 1;
        perFile[f] = (perFile[f] || 0) + 1;
        if (detail.length < 80) detail.push(f + ':' + start + '-' + (start + block.length - 1));
      }
    }
    block = [];
  };
  for (let i = 0; i < lines.length; i++) {
    if (isC[i]) { if (block.length === 0) start = i + 1; block.push(i); }
    else flush();
  }
  flush();
}

for (const [f, c] of Object.entries(perFile).sort((a, b) => b[1] - a[1]).slice(0, 25)) console.log(c + '\t' + f);
console.log('\n[' + lang + '] ' + offenders + ' offending / ' + total + ' EN-bearing (' + files.length + ' files)');
if (process.env.SHOW_DETAIL) for (const d of detail) console.log('  ' + d);
if (strict && offenders > 0) process.exit(1);
