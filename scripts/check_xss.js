// ============================================================
// フロントエンド XSS 静的検査（scripts/check_xss.js）
// 使い方: node check_xss.js [web/static のパス]
//
// web/static/*.js が innerHTML へ HTML を組み立てる際の「取り決め」を機械的に
// 検証する。プラグイン由来データ（.so の meta.json、エージェント応答、ログ）は
// 信頼できない入力であり、HTML 文字列へ埋め込む前に必ず無害化する必要がある。
//
//   1. escapeHtml / escapeAttr / cssClass の定義は escape.js の1箇所だけか
//      （各画面にコピー定義が復活すると、片方だけ直して穴が残る）
//   2. escape.js が、それを使うスクリプトより先に読み込まれているか
//   3. 属性値（class/id/href/title/style など）内の ${...} が安全な式か
//      - escapeHtml(...) / escapeAttr(...) / cssClass(...) / encodeURIComponent(...)
//        などの無害化関数で包まれている
//      - 数字確定の式（Number(...) / Math.min(...) / x.toFixed(1)）である
//      - 文字列リテラルだけの三項演算子である
//      - 上記いずれかを初期値とするローカル変数である
//   4. 生成 HTML にインラインイベントハンドラ（onclick= 等）や
//      javascript: URL が無いか（CSP を骨抜きにする書き方の検出）
// ============================================================
'use strict';

const fs = require('fs');
const path = require('path');

const dir = process.argv[2] || path.join(__dirname, '..', 'web', 'static');

// 検査対象（画面ごとの HTML ビルダー）
const SCRIPT_FILES = ['app.js', 'modules.js', 'config-editor.js', 'users.js'];
const SHARED_FILE = 'escape.js';
const SANITIZERS = ['escapeHtml', 'escapeAttr', 'cssClass'];

// 属性値の ${...} で「安全」とみなす呼び出し。
//   fieldElementId: modules.js の ID 正規化関数（[A-Za-z0-9_-] 以外を _ に落とす）
//   format*: 数値・日時の整形で、HTML 特殊文字を出力しない
const SAFE_CALL = new RegExp(
  '^(?:' + ['escapeHtml', 'escapeAttr', 'cssClass', 'encodeURIComponent', 'encodeURI',
    'fieldElementId', 'Number', 'String', 'Boolean', 'parseInt', 'parseFloat',
    'formatBytes', 'formatTimestamp', 'formatStorage', 'formatMemMB', 'clamp'].join('|') + ')\\s*\\('
);
const SAFE_MATH = /^Math\.(?:min|max|round|trunc|floor|ceil)\s*\(/;
const SAFE_NUMERIC_METHOD = /^[A-Za-z_$][\w$]*\.toFixed\s*\(/;

// 属性値に現れてよい文字列リテラル（引用符・山括弧・&・バッククォートを含まない）
const SAFE_LITERAL_BODY = /^[A-Za-z0-9_\-.:% /#]*$/;

// ------------------------------------------------------------
// ユーティリティ
// ------------------------------------------------------------
function readFile(name) {
  return fs.readFileSync(path.join(dir, name), 'utf8');
}

// テンプレート式 value の中から、ネストを考慮して "${...}" を取り出す。
function extractInterpolations(value) {
  const out = [];
  let i = 0;
  while (i < value.length) {
    const start = value.indexOf('${', i);
    if (start < 0) break;
    let depth = 1;
    let j = start + 2;
    let inQuote = '';
    while (j < value.length) {
      const ch = value[j];
      if (inQuote) {
        if (ch === '\\') j++;
        else if (ch === inQuote) inQuote = '';
      } else if (ch === '"' || ch === "'" || ch === '`') {
        inQuote = ch;
      } else if (ch === '{') {
        depth++;
      } else if (ch === '}') {
        depth--;
        if (depth === 0) break;
      }
      j++;
    }
    out.push(value.slice(start + 2, j));
    i = j + 1;
  }
  return out;
}

// 文字列リテラル1個だけの式か（'enabled' など）。
function isSafeLiteral(expr) {
  const s = expr.trim();
  const m = /^(['"])([\s\S]*)\1$/.exec(s);
  return !!m && SAFE_LITERAL_BODY.test(m[2]);
}

// 括弧・クォートを考慮して、トップレベルの区切り文字を探す。
function topLevelIndexOf(s, ch) {
  let depth = 0;
  let inQuote = '';
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inQuote) {
      if (c === '\\') i++;
      else if (c === inQuote) inQuote = '';
      continue;
    }
    if (c === '"' || c === "'" || c === '`') inQuote = c;
    else if (c === '(' || c === '[' || c === '{') depth++;
    else if (c === ')' || c === ']' || c === '}') depth--;
    else if (c === ch && depth === 0) return i;
  }
  return -1;
}

// 属性値に埋め込んで安全と言い切れる式か。
// safeLocals: 無害化済みと判定したローカル変数名の集合
function isSafeAttrExpr(expr, safeLocals, depth) {
  const s = expr.trim();
  if (s === '') return false;
  if (depth > 6) return false;
  if (isSafeLiteral(s)) return true;
  if (SAFE_CALL.test(s)) return true;
  if (SAFE_MATH.test(s)) return true;
  if (SAFE_NUMERIC_METHOD.test(s)) return true;

  // cond ? a : b —— cond は真偽値の選択にしか使われないので何でもよい。
  // a / b にデータが入ると危険なので、それぞれを再帰的に検査する。
  const q = topLevelIndexOf(s, '?');
  if (q >= 0) {
    const rest = s.slice(q + 1);
    const c = topLevelIndexOf(rest, ':');
    if (c < 0) return false;
    return isSafeAttrExpr(rest.slice(0, c), safeLocals, depth + 1) &&
           isSafeAttrExpr(rest.slice(c + 1), safeLocals, depth + 1);
  }

  // 単純な識別子（無害化済みローカルとして登録済みのものだけ許可）
  const m = /^([A-Za-z_$][\w$]*)(?:[.\[][\s\S]*)?$/.exec(s);
  if (m) return safeLocals.has(m[1]);
  return false;
}

// "const NAME = <式>;" を、クォート状態を追いながら切り出す。
function collectDeclarations(text) {
  const out = [];
  const re = /(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*/g;
  let m;
  while ((m = re.exec(text)) !== null) {
    const start = re.lastIndex;
    let inQuote = '';
    let depth = 0;
    let j = start;
    for (; j < text.length; j++) {
      const c = text[j];
      if (inQuote) {
        if (c === '\\') j++;
        else if (c === inQuote) inQuote = '';
        continue;
      }
      if (c === '"' || c === "'" || c === '`') inQuote = c;
      else if (c === '(' || c === '[' || c === '{') depth++;
      else if (c === ')' || c === ']' || c === '}') depth--;
      else if (c === ';' && depth === 0) break;
    }
    out.push({ name: m[1], init: text.slice(start, j) });
    re.lastIndex = j;
  }
  return out;
}

// 無害化済みローカルの集合を、依存関係が収束するまで反復して求める。
function computeSafeLocals(text) {
  const decls = collectDeclarations(text);
  const safe = new Set();
  for (let pass = 0; pass < 6; pass++) {
    let changed = false;
    for (const d of decls) {
      if (safe.has(d.name)) continue;
      if (isSafeAttrExpr(d.init, safe, 0)) {
        safe.add(d.name);
        changed = true;
      }
    }
    if (!changed) break;
  }
  return safe;
}

// ------------------------------------------------------------
// 検査
// ------------------------------------------------------------
const problems = [];
const notes = [];

// 1. 無害化関数の定義が escape.js だけにあるか
for (const fn of SANITIZERS) {
  const defRe = new RegExp(
    '(?:function\\s+' + fn + '|' + fn + '\\s*=\\s*function|(?:const|let|var)\\s+' + fn + '\\s*=)', 'g');
  for (const f of fs.readdirSync(dir).filter(n => n.endsWith('.js'))) {
    const hits = (readFile(f).match(defRe) || []).length;
    if (f === SHARED_FILE) {
      if (hits !== 1) problems.push(`${SHARED_FILE}: ${fn} の定義が ${hits} 個（1個である必要があります）`);
    } else if (hits > 0) {
      problems.push(`${f}: ${fn} を再定義しています（escape.js に一元化してください）`);
    }
  }
  notes.push(fn);
}

// 2. escape.js の読み込み順（使うスクリプトより前にあるか）
for (const html of fs.readdirSync(dir).filter(n => n.endsWith('.html'))) {
  const text = readFile(html);
  const esc = text.search(new RegExp('(?:src|href)="[^"]*' + SHARED_FILE + '"'));
  if (esc < 0) continue;
  for (const s of SCRIPT_FILES) {
    const idx = text.search(new RegExp('(?:src|href)="[^"]*/' + s + '"'));
    if (idx >= 0 && idx < esc) {
      problems.push(`${html}: ${s} が ${SHARED_FILE} より先に読み込まれています`);
    }
  }
}

// 3./4. 各スクリプトの属性値補間とインラインハンドラ
const ATTR_RE = /\b([A-Za-z][\w-]*)="([^"]*)"/g;
for (const f of SCRIPT_FILES) {
  const text = readFile(f);
  const safeLocals = computeSafeLocals(text);
  text.split('\n').forEach((line, i) => {
    if (/\bon(?:click|dblclick|error|load|mouseover|mouseout|submit|change|input)\s*=\s*["']/i.test(line)) {
      problems.push(`${f}:${i + 1}: インラインイベントハンドラ（CSP 違反）: ${line.trim().slice(0, 80)}`);
    }
    if (/javascript\s*:/i.test(line)) {
      problems.push(`${f}:${i + 1}: javascript: URL: ${line.trim().slice(0, 80)}`);
    }

    ATTR_RE.lastIndex = 0;
    let m;
    while ((m = ATTR_RE.exec(line)) !== null) {
      const attr = m[1];
      for (const expr of extractInterpolations(m[2])) {
        if (!isSafeAttrExpr(expr, safeLocals, 0)) {
          problems.push(`${f}:${i + 1}: ${attr}="..." の \${${expr.trim()}} が無害化されていません` +
            '（escapeHtml/escapeAttr/cssClass 等で包んでください）');
        }
      }
    }
  });
}

if (problems.length) {
  console.error('UNSAFE (frontend XSS guard):');
  for (const p of problems) console.error('  ' + p);
  process.exit(1);
}
console.log('OK: escape.js 一元化 (' + notes.join('/') + ') + ' + SCRIPT_FILES.length +
  ' ファイルの属性補間・インラインハンドラ — すべて安全');

