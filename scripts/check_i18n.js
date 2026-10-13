// i18n キー整合性チェッカー
// 使い方: node check_i18n.js <web/static のパス>
const fs = require('fs');
const path = require('path');

const dir = process.argv[2] || '.';
const i18n = fs.readFileSync(path.join(dir, 'i18n.js'), 'utf8');

// 辞書部分だけを切り出す（STORAGE_KEY より前）
const dictEnd = i18n.indexOf('const STORAGE_KEY');
const dict = i18n.slice(0, dictEnd);

const jaStart = dict.indexOf('ja: {');
const enStart = dict.indexOf('en: {');
const jaBlock = dict.slice(jaStart, enStart);
const enBlock = dict.slice(enStart);

// 行頭インデントされた 'key': のみをキー定義とみなす（三項演算子の誤検出防止）
function keys(block) {
  const set = new Set();
  const re = /^\s*'([a-zA-Z0-9_.]+)'\s*:/gm;
  let m;
  while ((m = re.exec(block)) !== null) set.add(m[1]);
  return set;
}

const ja = keys(jaBlock);
const en = keys(enBlock);
const defined = new Set([...ja, ...en]);

// 使用キー抽出
const used = new Set();
let m;
for (const f of fs.readdirSync(dir)) {
  const text = fs.readFileSync(path.join(dir, f), 'utf8');
  if (f.endsWith('.html')) {
    // data-i18n / data-i18n-placeholder / data-i18n-title / data-i18n-aria-label をすべて対象にする
    const re = /data-i18n(?:-[a-z-]+)?="([^"]+)"/g;
    while ((m = re.exec(text)) !== null) used.add(m[1]);
  } else if (f.endsWith('.js') && f !== 'i18n.js') {
    // t('key') / t("key") / t(`key`) を対象にする。
    // 引用符の直後が ')' か ','（第2引数あり）のときだけキーとして採用し、
    // 動的キー（t('level.' + x) / t(`a.${b}`)）は抽出しない。以前は
    // 'level.' のような連結の断片を未定義キーとして誤検出していた。
    const re = /\bt\(\s*(?:'([a-zA-Z0-9_.]+)'|"([a-zA-Z0-9_.]+)"|`([a-zA-Z0-9_.]+)`)\s*[,)]/g;
    while ((m = re.exec(text)) !== null) used.add(m[1] || m[2] || m[3]);
  }
}

let fail = false;

const missing = [...used].filter(k => !defined.has(k)).sort();
if (missing.length) {
  console.error('MISSING (used but undefined):');
  for (const k of missing) console.error('  ' + k);
  fail = true;
}

const onlyJa = [...ja].filter(k => !en.has(k)).sort();
if (onlyJa.length) {
  console.error('ONLY IN JA (en missing):');
  for (const k of onlyJa) console.error('  ' + k);
  fail = true;
}

const onlyEn = [...en].filter(k => !ja.has(k)).sort();
if (onlyEn.length) {
  console.error('ONLY IN EN (ja missing):');
  for (const k of onlyEn) console.error('  ' + k);
  fail = true;
}

if (fail) process.exit(1);
console.log('OK: ' + used.size + ' used / ' + ja.size + ' ja / ' + en.size + ' en keys — all consistent');
