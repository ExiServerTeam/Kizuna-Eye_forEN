// pluginsig パッケージはプラグイン（.so）ファイルの分離 Ed25519 署名検証を実装する。
// これはハードニング項目 A-3（G-3）の根本修正だ。Go プラグインのロードは
// init()/パッケージ初期化子を実行するため、plugin.Open を呼ぶ「前に」
// .so を認証しなければならない。
//
// 設計メモ
//
//   - 署名は分離形式だ。<plugin>.so を署名し、署名は隣に <plugin>.so.sig として
//     保存する（SigSuffix 参照）。.so 自体は決して変更しないため、dashboard UI の
//     ハッシュ/サイズの意味が変わらない。
//   - 署名は生のファイルバイト列を対象とする。正規化は行わない。ファイルは
//     署名側で 1 回、検証側で 1 回読まれる。
//   - 鍵素材は意図的にトランスポート非依存にしてあり、運用者が任意のツールで
//     作った鍵を貼り付けられる:
//     公開鍵 : PKIX PEM、hex、または base64（標準 or raw/パディング無し）。
//     秘密鍵 : PKCS#8 PEM、hex、または 64 バイト Ed25519 鍵か 32 バイトシードの base64。
//     署名   : base64（標準 or raw）または hex。'#' で始まるコメント行と
//     すべての空白は無視する。
//   - 検証は呼び出し側で fail-closed だ。公開鍵欠落、署名欠落、不一致は
//     警告ではなくエラーになる。
//
// Package pluginsig implements detached Ed25519 signature verification for
// plugin (.so) files. This is the root fix for hardening item A-3 (G-3):
// loading a Go plugin executes its init()/package initializers, so a .so must
// be authenticated BEFORE plugin.Open is called.
//
// Design notes
//
//   - Signatures are detached: <plugin>.so is signed and the signature is
//     stored next to it as <plugin>.so.sig (see SigSuffix). The .so itself is
//     never modified, so its hash/size in the dashboard UI keeps meaning the
//     same thing.
//   - The signature covers the raw file bytes. No canonicalisation is applied:
//     the file is read once by the signer and once by the verifier.
//   - Key material is deliberately transport-agnostic so an operator can paste
//     a key produced by any tool:
//     PUBLIC KEY : PKIX PEM, hex, or base64 (standard or raw/unpadded).
//     PRIVATE KEY: PKCS#8 PEM, hex, or base64 of a 64-byte Ed25519 key or a
//     32-byte seed.
//     SIGNATURE  : base64 (standard or raw) or hex; comment lines starting
//     with '#' and all whitespace are ignored.
//   - Verification is fail-closed at the call sites: a missing public key, a
//     missing signature, or a mismatch is an error, never a warning.
package pluginsig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"Kizuna-Eye/pkg/fsutil"
)

const (
	// SigSuffix はプラグインパスに付けて分離署名の位置を示す
	// （plugins/Foo.so -> plugins/Foo.so.sig）。
	// SigSuffix is appended to a plugin path to locate its detached
	// signature (plugins/Foo.so -> plugins/Foo.so.sig).
	SigSuffix = ".sig"

	// EmbedMagic は .so に追記する埋め込み署名トレーラの目印だ。
	// 署名済み .so は [ELF body][EmbedMagic (13 bytes)][Ed25519 sig (64 bytes)] だ。
	// ELF ローダは最終セクション以降のバイトを無視するため、署名済みファイルも
	// トレーラを剥がさずにロードできる。
	// EmbedMagic marks an embedded signature trailer appended to a .so.
	// A signed .so is [ELF body][EmbedMagic (13 bytes)][Ed25519 sig (64 bytes)].
	// The ELF loader ignores bytes after the last section, so a signed file
	// is still loadable without stripping the trailer first.
	EmbedMagic = "KIZUNA_SIG_V1"

	// EmbedTrailerLen はトレーラ（magic + 署名）の固定サイズだ。
	// EmbedTrailerLen is the fixed size of the trailer (magic + signature).
	EmbedTrailerLen = len(EmbedMagic) + ed25519.SignatureSize

	// サイズ上限。鍵と署名は極小で、上限は敵対的・破損ファイルがチェック開始前に
	// メモリへ読まれるのを防ぐ。
	// Size caps. Keys and signatures are tiny; the cap keeps a hostile or
	// corrupt file from being read into memory before the check starts.
	sigMaxSize  = 8 << 10 // 8 KiB
	keyMaxSize  = 8 << 10 // 8 KiB
	ed25519Pub  = ed25519.PublicKeySize
	ed25519Priv = ed25519.PrivateKeySize
	ed25519Seed = ed25519.SeedSize
	sigLen      = ed25519.SignatureSize
)

// ErrNoSignature は分離署名ファイルが存在しないことを報告する。
// 署名を必須とする呼び出し側はこれをハード失敗として扱う。
// ErrNoSignature reports that a detached signature file does not exist.
// Callers that require a signature treat it as a hard failure.
var ErrNoSignature = errors.New("署名ファイルが見つかりません")

// SigPath はプラグインパスの分離署名パスを返す。
// SigPath returns the detached-signature path for a plugin path.
func SigPath(soPath string) string { return soPath + SigSuffix }

// ============================================================
// 埋め込み署名（トレーラ形式）
// Embedded signature (trailer form)
// ============================================================
//
// 署名済みプラグインは署名を .so 自体に載せられるため、運用者は 1 ファイルだけを
// 配布すればよい。署名はトレーラの前の本体バイト列を対象とし、トレーラは
// [EmbedMagic][64-byte sig] だ。agent と dashboard はどちらも埋め込み署名を優先し、
// 後方互換のため分離 .sig にフォールバックする。
// A signed plugin can carry its signature inside the .so itself, so the
// operator only has to distribute one file. The signature covers the body
// bytes that precede the trailer; the trailer is [EmbedMagic][64-byte sig].
// Both the agent and the dashboard prefer the embedded signature and fall
// back to the detached .sig for backward compatibility.

// HasEmbeddedSignature は data が埋め込みトレーラで終わるかを返す。
// HasEmbeddedSignature reports whether data ends with the embedded trailer.
func HasEmbeddedSignature(data []byte) bool {
	return len(data) >= EmbedTrailerLen &&
		string(data[len(data)-EmbedTrailerLen:len(data)-ed25519.SignatureSize]) == EmbedMagic
}

// SplitEmbedded は data がトレーラで終わるとき本体（トレーラ前のバイト列）と
// 署名を返す。トレーラが無い場合 ok は false になり、呼び出し側は分離署名へ
// フォールバックできる。
// SplitEmbedded returns the body (bytes before the trailer) and the signature
// when data ends with the trailer. ok is false when no trailer is present, so
// the caller can fall back to the detached signature.
func SplitEmbedded(data []byte) (body, sig []byte, ok bool) {
	if !HasEmbeddedSignature(data) {
		return nil, nil, false
	}
	cut := len(data) - EmbedTrailerLen
	return data[:cut], data[len(data)-ed25519.SignatureSize:], true
}

// EmbedSignatureBytes は data にトレーラを付けて返す。署名は data そのものに対して
// 計算するため、結果は与えられた鍵に対して決定的だ。
// EmbedSignatureBytes returns data with the trailer appended. The signature is
// computed over data as it is, so the result is deterministic for a given key.
func EmbedSignatureBytes(data []byte, priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519Priv {
		return nil, errors.New("署名鍵が不正です")
	}
	sig := ed25519.Sign(priv, data)
	out := make([]byte, 0, len(data)+EmbedTrailerLen)
	out = append(out, data...)
	out = append(out, EmbedMagic...)
	out = append(out, sig...)
	return out, nil
}

// EmbedSignatureFile は soPath を読み、埋め込みトレーラ付きで書き直す。
// 書き換えはアトミック（一時ファイル + rename）なので、クラッシュしても
// 書きかけのプラグインを残さない。
// EmbedSignatureFile reads soPath and rewrites it with the embedded trailer.
// The rewrite is atomic (temp file + rename) so a crash cannot leave a
// half-written plugin.
func EmbedSignatureFile(soPath string, priv ed25519.PrivateKey) error {
	data, err := os.ReadFile(soPath)
	if err != nil {
		return fmt.Errorf("署名対象の読み込みに失敗 (%s): %w", soPath, err)
	}
	// 既に署名済みのファイルを再署名すると古いトレーラも署名してしまう。
	// 先に剥がして操作を冪等にする。
	// Re-signing an already signed file would sign the old trailer too.
	// Strip it first so the operation is idempotent.
	if body, _, ok := SplitEmbedded(data); ok {
		data = body
	}
	signed, err := EmbedSignatureBytes(data, priv)
	if err != nil {
		return err
	}
	// fsutil.WriteFileAtomic: 同一ディレクトリに一意な一時ファイルを作り、
	// fsync してから rename する。以前は soPath + ".embed.tmp" という予測可能な
	// 名前を os.WriteFile で直接書いていたため、pluginsDir (agent ユーザーが
	// 書き込み可能) に同名の symlink を先に置かれると、そのリンク先を上書き
	// しうる（CWE-59）。atomic write は最終要素の symlink を拒否する。
	// fsutil.WriteFileAtomic: create a unique temp file in the same directory,
	// fsync, then rename. Previously a predictable name (soPath +
	// ".embed.tmp") was written directly with os.WriteFile, so a symlink with
	// the same name planted in pluginsDir (writable by the agent user) could
	// redirect the write to a target the attacker chose (CWE-59). The atomic
	// write refuses a symlink at the final component.
	if err := fsutil.WriteFileAtomic(soPath, signed, 0o644); err != nil {
		return fmt.Errorf("埋め込み署名の書き込みに失敗 (%s): %w", soPath, err)
	}
	return nil
}

// VerifyEmbedded は data 内の埋め込みトレーラを検証する（あれば）。トレーラが
// 無い場合 ok は false になる（呼び出し側は分離 .sig を試す）。存在するが不正な
// 署名は ok=true とエラーを返すため、改ざんされたファイルが黙って「署名無し」へ
// ダウングレードされることはない。
// VerifyEmbedded verifies the embedded trailer in data, if present. ok is
// false when there is no trailer (the caller then tries the detached .sig).
// A present-but-bad signature returns ok=true and an error, so a tampered
// file is never silently downgraded to "no signature".
func VerifyEmbedded(data []byte, pub ed25519.PublicKey) (ok bool, err error) {
	if len(pub) != ed25519Pub {
		return false, errors.New("検証鍵が不正です")
	}
	body, sig, present := SplitEmbedded(data)
	if !present {
		return false, nil
	}
	if !ed25519.Verify(pub, body, sig) {
		return true, errors.New("埋め込み署名が一致しません（改ざんまたは鍵不一致の可能性）")
	}
	return true, nil
}

// VerifyFileAny は soPath を、まず埋め込みトレーラで、次に分離 <file>.sig で検証する。
// 一致したソースを返し、呼び出し側がプラグインの認証方法をログできるようにする。
// VerifyFileAny verifies soPath with the embedded trailer first and the
// detached <file>.sig second. It returns the source that matched so callers
// can log how a plugin was authenticated.
func VerifyFileAny(soPath string, pub ed25519.PublicKey) (source string, err error) {
	data, err := os.ReadFile(soPath)
	if err != nil {
		return "", fmt.Errorf("署名対象の読み込みに失敗 (%s): %w", soPath, err)
	}
	if ok, verr := VerifyEmbedded(data, pub); ok {
		if verr != nil {
			return "embedded", verr
		}
		return "embedded", nil
	}
	sigPath := SigPath(soPath)
	if _, serr := os.Stat(sigPath); serr != nil {
		if os.IsNotExist(serr) {
			return "", fmt.Errorf("%w: %s", ErrNoSignature, sigPath)
		}
		return "", fmt.Errorf("署名ファイルを開けません (%s): %w", sigPath, serr)
	}
	sigBytes, rerr := os.ReadFile(sigPath)
	if rerr != nil {
		return "", fmt.Errorf("署名ファイルの読み込みに失敗 (%s): %w", sigPath, rerr)
	}
	if verr := VerifyBytes(data, sigBytes, pub, soPath); verr != nil {
		return "detached", verr
	}
	return "detached", nil
}

// GenerateKeyPair は新しい Ed25519 鍵ペアを返す。
// GenerateKeyPair returns a fresh Ed25519 key pair.
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("鍵生成に失敗: %w", err)
	}
	return pub, priv, nil
}

// ============================================================
// エンコードヘルパー
// Encoding helpers
// ============================================================

// EncodePublicKey は pub の base64（標準、パディング有り）表現を返す。
// EncodePublicKey returns the base64 (standard, padded) form of pub.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// EncodePrivateKey は priv の base64（標準、パディング有り）表現を返す。
// EncodePrivateKey returns the base64 (standard, padded) form of priv.
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv)
}

// ============================================================
// パース
// Parsing
// ============================================================

// stripKeyText は PEM のヘッダ/フッタ、'#' コメント行、すべての空白を除去し、
// 残りのテキストを hex/base64 デコードできるようにする。
// stripKeyText removes PEM headers/footers, '#' comment lines and all
// whitespace so the remaining text can be hex/base64 decoded.
func stripKeyText(data []byte) []byte {
	var b bytes.Buffer
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
	}
	return b.Bytes()
}

// decodeBase64 は一般的な base64 アルファベットを試す。
// decodeBase64 tries the usual base64 alphabets.
func decodeBase64(s string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// ParsePublicKey は PEM、hex、base64 テキストから Ed25519 公開鍵をパースする。
// ParsePublicKey parses an Ed25519 public key from PEM, hex or base64 text.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	if len(data) == 0 {
		return nil, errors.New("公開鍵が空です")
	}
	if len(data) > keyMaxSize {
		return nil, fmt.Errorf("公開鍵ファイルが大きすぎます (%d bytes)", len(data))
	}

	if block, _ := pem.Decode(data); block != nil {
		// "PUBLIC KEY"（PKIX）を受け付ける。それ以外（例: 誤って貼られた秘密鍵）は
		// 明確なメッセージで拒否し、運用者が黙って使えない鍵を残さないようにする。
		// Accept "PUBLIC KEY" (PKIX). Anything else (e.g. a private key
		// pasted by mistake) is rejected with a clear message so the
		// operator is not left with a silently-unusable key.
		if block.Type == "PUBLIC KEY" {
			k, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("PKIX 公開鍵のパースに失敗: %w", err)
			}
			pub, ok := k.(ed25519.PublicKey)
			if !ok {
				return nil, errors.New("公開鍵が Ed25519 ではありません")
			}
			return pub, nil
		}
		return nil, fmt.Errorf("未対応の PEM 形式です: %s (PUBLIC KEY を使用してください)", block.Type)
	}

	text := stripKeyText(data)

	if b, err := hex.DecodeString(string(text)); err == nil && len(b) == ed25519Pub {
		return ed25519.PublicKey(b), nil
	}
	if b, err := decodeBase64(string(text)); err == nil && len(b) == ed25519Pub {
		return ed25519.PublicKey(b), nil
	}
	return nil, fmt.Errorf("公開鍵を解釈できません（Ed25519 の %d バイト鍵を PEM / hex / base64 で指定してください）", ed25519Pub)
}

// ParsePrivateKey は PKCS#8 PEM、hex、base64 から Ed25519 秘密鍵をパースする。
// 32 バイト値はシードとして、64 バイト値は完全な鍵として扱う。
// ParsePrivateKey parses an Ed25519 private key from PKCS#8 PEM, hex or base64.
// A 32-byte value is treated as a seed; a 64-byte value as a full key.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	if len(data) == 0 {
		return nil, errors.New("秘密鍵が空です")
	}
	if len(data) > keyMaxSize {
		return nil, fmt.Errorf("秘密鍵ファイルが大きすぎます (%d bytes)", len(data))
	}

	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "PRIVATE KEY" {
			return nil, fmt.Errorf("未対応の PEM 形式です: %s (PRIVATE KEY を使用してください)", block.Type)
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("PKCS#8 秘密鍵のパースに失敗: %w", err)
		}
		priv, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("秘密鍵が Ed25519 ではありません")
		}
		return priv, nil
	}

	text := stripKeyText(data)

	if b, err := hex.DecodeString(string(text)); err == nil {
		return keyFromBytes(b)
	}
	if b, err := decodeBase64(string(text)); err == nil {
		return keyFromBytes(b)
	}
	return nil, errors.New("秘密鍵を解釈できません（PKCS#8 PEM / hex / base64 で指定してください）")
}

func keyFromBytes(b []byte) (ed25519.PrivateKey, error) {
	switch len(b) {
	case ed25519Seed:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519Priv:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("秘密鍵の長さが不正です: %d バイト（%d または %d を期待）", len(b), ed25519Seed, ed25519Priv)
	}
}

// ParseSignature は base64 または hex テキストから分離署名をパースする。
// ParseSignature parses a detached signature from base64 or hex text.
func ParseSignature(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("署名が空です")
	}
	if len(data) > sigMaxSize {
		return nil, fmt.Errorf("署名ファイルが大きすぎます (%d bytes)", len(data))
	}
	text := stripKeyText(data)
	if b, err := hex.DecodeString(string(text)); err == nil && len(b) == sigLen {
		return b, nil
	}
	if b, err := decodeBase64(string(text)); err == nil && len(b) == sigLen {
		return b, nil
	}
	return nil, fmt.Errorf("署名を解釈できません（Ed25519 の %d バイト署名を base64 / hex で指定してください）", sigLen)
}

// ============================================================
// ファイル指向ヘルパー
// File-oriented helpers
// ============================================================

// LoadPublicKey は公開鍵ファイルを読み、パースする。
// LoadPublicKey reads and parses a public key file.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("公開鍵ファイルを開けません (%s): %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("公開鍵のパスがディレクトリです: %s", path)
	}
	if info.Size() > keyMaxSize {
		return nil, fmt.Errorf("公開鍵ファイルが大きすぎます (%s: %d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("公開鍵ファイルの読み込みに失敗 (%s): %w", path, err)
	}
	pub, err := ParsePublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pub, nil
}

// LoadPrivateKey は秘密鍵ファイルを読み、パースする。
// LoadPrivateKey reads and parses a private key file.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("秘密鍵ファイルを開けません (%s): %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("秘密鍵のパスがディレクトリです: %s", path)
	}
	if info.Size() > keyMaxSize {
		return nil, fmt.Errorf("秘密鍵ファイルが大きすぎます (%s: %d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("秘密鍵ファイルの読み込みに失敗 (%s): %w", path, err)
	}
	priv, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return priv, nil
}

// ============================================================
// 署名 / 検証
// Sign / Verify
// ============================================================

// SignFile は soPath のファイルの分離署名を sigPath へ書く。
// 署名ファイルは 0644 で作成する。公開データであり、A-4 のユーザー分離後は
// agent (kizuna-eye) がプラグインを検証するために読めなければならない。
// SignFile writes a detached signature of the file at soPath to sigPath.
// The signature file is created 0644: it is public data, and after the A-4
// user split the agent (kizuna-eye) must be able to read it to verify the
// plugin.
func SignFile(soPath, sigPath string, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519Priv {
		return errors.New("署名鍵が不正です")
	}
	data, err := os.ReadFile(soPath)
	if err != nil {
		return fmt.Errorf("署名対象の読み込みに失敗 (%s): %w", soPath, err)
	}
	sig := ed25519.Sign(priv, data)
	// fsutil.WriteFileAtomic は最終パス要素の symlink を拒否し、temp+fsync+rename で
	// 書く。以前の os.WriteFile は symlink を辿っていたため、sigPath に仕込まれた
	// リンク（plugins ディレクトリは agent ユーザーが書き込み可能）が署名を攻撃者の
	// 選んだ任意ファイルへ誘導した（CWE-59）。dashboard のアップロード経路は既に
	// atomic writer を使っており、取り残されていたのはこの CLI 経路だけだった。
	//
	// 0600 ではなく 0644。署名は公開データであり、A-4 の分離後は agent
	// (kizuna-eye) がプラグイン検証のために読めなければならない。これは
	// アップロード経路が既に書いているモードと一致する。
	// fsutil.WriteFileAtomic rejects a symlink at the final path component and
	// writes via temp+fsync+rename. The previous os.WriteFile followed a
	// symlink, so a link planted at sigPath (the plugins directory is
	// writable by the agent user) redirected the signature into an arbitrary
	// file the attacker chose (CWE-59). The dashboard's upload path already
	// used the atomic writer; only this CLI path was left behind.
	//
	// 0644, not 0600: the signature is public data, and after the A-4
	// separation the agent (kizuna-eye) must be able to read it to verify a
	// plugin. This matches the mode the upload path already writes.
	if err := fsutil.WriteFileAtomic(sigPath, []byte(EncodeSignature(sig)+"\n"), 0o644); err != nil {
		return fmt.Errorf("署名ファイルの書き込みに失敗 (%s): %w", sigPath, err)
	}
	return nil
}

// VerifyFile は sigPath の分離署名を soPath のファイルに対して検証する。
// 署名ファイル欠落は ErrNoSignature を包むエラーを返す。
// VerifyFile checks the detached signature at sigPath against the file at
// soPath. A missing signature file returns an error wrapping ErrNoSignature.
func VerifyFile(soPath, sigPath string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519Pub {
		return errors.New("検証鍵が不正です")
	}
	if _, err := os.Stat(sigPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNoSignature, sigPath)
		}
		return fmt.Errorf("署名ファイルを開けません (%s): %w", sigPath, err)
	}
	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("署名ファイルの読み込みに失敗 (%s): %w", sigPath, err)
	}
	data, err := os.ReadFile(soPath)
	if err != nil {
		return fmt.Errorf("署名対象の読み込みに失敗 (%s): %w", soPath, err)
	}
	return VerifyBytes(data, sigBytes, pub, soPath)
}

// VerifyBytes はメモリ上に保持した分離署名を data に対して検証する。
// アップロード経路で使う。署名は multipart パートとして届き、まだ .sig ファイルが
// 存在しない。
// VerifyBytes checks a detached signature held in memory against data. It is
// used by the upload path, where the signature arrives as a multipart part and
// no .sig file exists yet.
func VerifyBytes(data, sigBytes []byte, pub ed25519.PublicKey, label string) error {
	if len(pub) != ed25519Pub {
		return errors.New("検証鍵が不正です")
	}
	sig, err := ParseSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("署名の解釈に失敗 (%s): %w", label, err)
	}
	if !ed25519.Verify(pub, data, sig) {
		return fmt.Errorf("署名が一致しません (%s)", label)
	}
	return nil
}

// EncodeSignature は .sig ファイルへ書く base64 表現を返す。
// EncodeSignature returns the base64 form written into a .sig file.
func EncodeSignature(sig []byte) string {
	return base64.StdEncoding.EncodeToString(sig)
}
