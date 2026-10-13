#!/usr/bin/env node
/*
 * Kizuna-Eye: バイリンガルコメント検査。
 * 英語のみの Go コメントブロックを検出し、日本語（常体）＋英語の2行規約が
 * 未適用の箇所を一覧化する。--strict で違反があれば終了コード 1 を返す。
 *
 * Kizuna-Eye: bilingual comment checker.
 * Reports English-only Go comment blocks that still need the Japanese
 * (plain-style) + English two-line convention. --strict exits 1 on any hit.
 */
'use strict';
const fs = require('fs');
const path = require('path');

const args = process.argv.slice(2);
const strict = args.includes('--strict');
const noTests = args.includes('--no-tests');
const roots = args.filter((a) => !a.startsWith('--'));
if (roots.length === 0) roots.push('cmd', 'internal', 'pkg');

const JA = /[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff]/;
const EN = /[A-Za-z]/;
const EXEMPT = /^\s*\/\/\s*(go:|go:build|\+build|nolint|line\b|export\b|sys\b|go:generate|Code generated|SPDX-|#|\*|!)/i;

function walk(dir, out) {
  let ents;
  try { ents = fs.readdirSync(dir, { withFileTypes: true }); } catch { return; }
  for (const e of ents) {
    if (e.name === '.git' || e.name === 'vendor' || e.name === 'node_modules') continue;
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.go')) out.push(p);
  }
}

const files = [];
for (const r of roots) walk(r, files);

let offenders = 0;
let total = 0;
for (const f of files) {
  if (noTests && f.endsWith('_test.go')) continue;
  let lines;
  try { lines = fs.readFileSync(f, 'utf8').split(/\r?\n/); } catch { continue; }
  let block = [];
  const flush = () => {
    if (block.length === 0) return;
    if (block.every((l) => l.trim() === '//' || EXEMPT.test(l))) { block = []; return; }
    const text = block.join('\n');
    if (!EN.test(text)) { block = []; return; }
    total += 1;
    if (!JA.test(text)) {
      offenders += 1;
      if (offenders <= 40) console.log(`${f}: English-only comment block (needs Japanese)`);
    }
    block = [];
  };
  for (const line of lines) {
    if (line.replace(/^\s+/, '').startsWith('//')) block.push(line);
    else flush();
  }
  flush();
}

console.log(`\n${offenders} offending block(s) / ${total} English-bearing block(s)`);
if (strict && offenders > 0) process.exit(1);
