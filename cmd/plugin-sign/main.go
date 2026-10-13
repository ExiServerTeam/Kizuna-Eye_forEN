// plugin-sign コマンドは Kizuna-Eye プラグイン（.so）ファイルの分離 Ed25519 署名を
// 作成・検証する。これはハードニング項目 A-3 の運用者向けの半分だ。agent と
// dashboard は、設定済み公開鍵に対して署名が検証できない .so のロードを拒否する。
//
// 使い方:
//
//	# 1) 鍵対を作る（秘密鍵はオフホスト保管。共有に置かないこと）
//	plugin-sign -gen-key -key-dir /secure/kizuna
//
//	# 2) プラグインに署名する（<file>.so.sig を書き出す）
//	plugin-sign -sign /opt/kizuna-eye/bin/plugins/Foo.so -private-key /secure/kizuna/plugin_signing.key
//
//	# 3) 検証する（配置前の確認）
//	plugin-sign -verify /opt/kizuna-eye/bin/plugins/Foo.so -public-key /etc/kizuna-eye/plugin_signing.pub
//
//	# まとめて署名する
//	plugin-sign -sign-all /opt/kizuna-eye/bin/plugins -private-key /secure/kizuna/plugin_signing.key
//
// 終了コード: 0 = OK、1 = エラー、2 = 使い方。
// Command plugin-sign creates and verifies detached Ed25519 signatures for
// Kizuna-Eye plugin (.so) files. It is the operator-facing half of hardening
// item A-3: the agent and the dashboard refuse to load a .so whose signature
// does not verify against the configured public key.
//
// Usage:
//
//	# 1) 鍵対を作る（秘密鍵はオフホスト保管。共有に置かないこと）
//	plugin-sign -gen-key -key-dir /secure/kizuna
//
//	# 2) プラグインに署名する（<file>.so.sig を書き出す）
//	plugin-sign -sign /opt/kizuna-eye/bin/plugins/Foo.so -private-key /secure/kizuna/plugin_signing.key
//
//	# 3) 検証する（配置前の確認）
//	plugin-sign -verify /opt/kizuna-eye/bin/plugins/Foo.so -public-key /etc/kizuna-eye/plugin_signing.pub
//
//	# まとめて署名する
//	plugin-sign -sign-all /opt/kizuna-eye/bin/plugins -private-key /secure/kizuna/plugin_signing.key
//
// Exit codes: 0 = OK, 1 = error, 2 = usage.
package main

import (
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"Kizuna-Eye/internal/pluginsig"
	"Kizuna-Eye/pkg/fsutil"
)

const usage = `plugin-sign - Kizuna-Eye プラグイン署名ツール (A-3)

使い方:
  plugin-sign -gen-key -key-dir DIR
      新しい Ed25519 鍵対を生成し、DIR/plugin_signing.key (0600) と
      DIR/plugin_signing.pub (0644) を書き出す。
      秘密鍵は共有上に置かず、オフホストで保管すること。

  plugin-sign -embed FILE -private-key KEYFILE
      FILE (.so) の末尾に署名を埋め込む。配布ファイルは .so 一つで済む。
      再実行は冪等（既存の埋め込み署名を剥がして署名し直す）。

  plugin-sign -embed-all DIR -private-key KEYFILE
      DIR 直下の *.so すべてに埋め込み署名する。

  plugin-sign -sign FILE -private-key KEYFILE [-out SIGFILE]
      FILE の分離署名を書き出す（既定: FILE.sig）。後方互換用。

  plugin-sign -sign-all DIR -private-key KEYFILE
      DIR 直下の *.so すべてに分離署名する。

  plugin-sign -verify FILE -public-key PUBFILE
      FILE を検証する。埋め込み署名を優先し、無ければ FILE.sig を読む。

  plugin-sign -print-public-key -private-key KEYFILE
      秘密鍵から公開鍵を base64 で表示する（PUBFILE への配布用）。

鍵・署名の形式:
  公開鍵   : PKIX PEM / hex / base64
  秘密鍵   : PKCS#8 PEM / hex / base64（64 バイト鍵 または 32 バイト seed）
  署名     : base64 / hex（'#' 始まりのコメント行と空白は無視）

埋め込み署名のレイアウト:
  [.so 本体][マジック 13B "KIZUNA_SIG_V1"][Ed25519 署名 64B]
  署名は本体のみを対象とする。ELF ローダは末尾の余分バイトを無視するため、
  署名付き .so もそのまま plugin.Open できる。
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("plugin-sign", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	var (
		genKey    = fs.Bool("gen-key", false, "新しい Ed25519 鍵対を生成する")
		keyDir    = fs.String("key-dir", ".", "-gen-key の保存先ディレクトリ")
		signFile  = fs.String("sign", "", "分離署名する .so ファイル")
		signAll   = fs.String("sign-all", "", "ディレクトリ直下の全 .so に分離署名する")
		embedFile = fs.String("embed", "", "埋め込み署名する .so ファイル")
		embedAll  = fs.String("embed-all", "", "ディレクトリ直下の全 .so に埋め込み署名する")
		verify    = fs.String("verify", "", "検証する .so ファイル（埋め込み優先、無ければ <file>.sig）")
		privKey   = fs.String("private-key", "", "秘密鍵ファイル")
		pubKey    = fs.String("public-key", "", "公開鍵ファイル")
		outPath   = fs.String("out", "", "分離署名の出力先（既定: <file>.sig）")
		printPub  = fs.Bool("print-public-key", false, "秘密鍵から公開鍵を表示する")
		overwrite = fs.Bool("force", false, "既存の鍵・署名を上書きする")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	switch {
	case *genKey:
		return generateKeyPair(*keyDir, *overwrite)
	case *printPub:
		return printPublicKey(*privKey)
	case *embedFile != "":
		return embedOne(*embedFile, *privKey)
	case *embedAll != "":
		return embedAllIn(*embedAll, *privKey)
	case *signFile != "":
		return signOne(*signFile, *privKey, *outPath)
	case *signAll != "":
		return signAllIn(*signAll, *privKey, *overwrite)
	case *verify != "":
		return verifyOne(*verify, *pubKey)
	default:
		fs.Usage()
		return errors.New("実行する操作を指定してください")
	}
}

// generateKeyPair は新しい鍵対を dir へ書き出す。
// generateKeyPair writes a fresh key pair into dir.
func generateKeyPair(dir string, force bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("鍵ディレクトリを作成できません (%s): %w", dir, err)
	}
	privPath := filepath.Join(dir, "plugin_signing.key")
	pubPath := filepath.Join(dir, "plugin_signing.pub")

	if !force {
		for _, p := range []string{privPath, pubPath} {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s が既に存在します（上書きするなら -force）", p)
			}
		}
	}

	pub, priv, err := pluginsig.GenerateKeyPair()
	if err != nil {
		return err
	}
	// 秘密鍵は全プラグイン署名の信頼の根なので、素の os.WriteFile では決して
	// 書かない。それは最終パス要素の symlink を辿るため、privPath に仕込まれた
	// リンクが鍵を攻撃者の選んだファイルへ誘導し得る（CWE-59）。
	// The private key is the root of trust for every plugin signature, so it
	// is never written with a plain os.WriteFile: that follows a symlink at
	// the final path component, so a link planted at privPath would redirect
	// the key into a file the attacker chose (CWE-59).
	if err := writeKeyFile(privPath, []byte(pluginsig.EncodePrivateKey(priv)+"\n"), 0o600, force); err != nil {
		return fmt.Errorf("秘密鍵の書き込みに失敗 (%s): %w", privPath, err)
	}
	if err := fsutil.WriteFileAtomic(pubPath, []byte(pluginsig.EncodePublicKey(pub)+"\n"), 0o644); err != nil {
		return fmt.Errorf("公開鍵の書き込みに失敗 (%s): %w", pubPath, err)
	}

	fmt.Printf("🔑 鍵対を生成しました\n  秘密鍵: %s (0600, オフホストで保管してください)\n  公開鍵: %s\n",
		privPath, pubPath)
	fmt.Printf("  公開鍵 (base64): %s\n", pluginsig.EncodePublicKey(pub))
	return nil
}

// writeKeyFile は symlink を決して辿らずに鍵ファイルを書く。
//
// force=false は O_CREATE|O_EXCL を使い、既存パス（symlink 含む）を拒否し、
// 存在確認と書き込みの間の TOCTOU 窓を閉じる。force=true は共有のアトミック
// ライタを使い、これも symlink ターゲットを拒否しつつ既存ファイルの置き換えを
// 許す。
// writeKeyFile writes a key file without ever following a symlink.
//
// force=false uses O_CREATE|O_EXCL, which refuses an existing path (including
// a symlink) and closes the TOCTOU window between the existence check and the
// write. force=true uses the shared atomic writer, which also rejects a
// symlink target but allows replacing an existing file.
func writeKeyFile(path string, data []byte, perm os.FileMode, force bool) error {
	if force {
		return fsutil.WriteFileAtomic(path, data, perm)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// printPublicKey は秘密鍵ファイルから公開鍵を導出して表示する。
// printPublicKey derives and prints the public key from a private key file.
func printPublicKey(privPath string) error {
	if privPath == "" {
		return errors.New("-print-public-key には -private-key が必要です")
	}
	priv, err := pluginsig.LoadPrivateKey(privPath)
	if err != nil {
		return err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("秘密鍵から公開鍵を取り出せません")
	}
	fmt.Println(pluginsig.EncodePublicKey(pub))
	return nil
}

// signOne は 1 ファイルに署名する。
// signOne signs a single file.
func signOne(soPath, privPath, out string) error {
	if privPath == "" {
		return errors.New("-sign には -private-key が必要です")
	}
	if out == "" {
		out = pluginsig.SigPath(soPath)
	}
	priv, err := pluginsig.LoadPrivateKey(privPath)
	if err != nil {
		return err
	}
	if err := pluginsig.SignFile(soPath, out, priv); err != nil {
		return err
	}
	fmt.Printf("✍️  署名しました: %s -> %s\n", soPath, out)
	return nil
}

// signAllIn は dir 直下のすべての *.so に署名する。
// signAllIn signs every *.so directly under dir.
func signAllIn(dir, privPath string, force bool) error {
	if privPath == "" {
		return errors.New("-sign-all には -private-key が必要です")
	}
	priv, err := pluginsig.LoadPrivateKey(privPath)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("ディレクトリを読めません (%s): %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".so") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return fmt.Errorf("%s に .so がありません", dir)
	}
	sort.Strings(names)

	failed := 0
	for _, n := range names {
		so := filepath.Join(dir, n)
		sig := pluginsig.SigPath(so)
		if !force {
			if _, err := os.Stat(sig); err == nil {
				fmt.Printf("⏭️  既存の署名を保持: %s\n", sig)
				continue
			}
		}
		if err := pluginsig.SignFile(so, sig, priv); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			failed++
			continue
		}
		fmt.Printf("✍️  %s -> %s\n", so, sig)
	}
	if failed > 0 {
		return fmt.Errorf("%d 件の署名に失敗しました", failed)
	}
	return nil
}

// embedOne は .so ファイルに埋め込み署名トレーラを追記する。
// embedOne appends an embedded signature trailer to a .so file.
func embedOne(soPath, privPath string) error {
	if privPath == "" {
		return errors.New("-embed には -private-key が必要です")
	}
	if !strings.HasSuffix(soPath, ".so") {
		return fmt.Errorf("埋め込み署名の対象は .so のみです: %s", soPath)
	}
	priv, err := pluginsig.LoadPrivateKey(privPath)
	if err != nil {
		return err
	}
	if err := pluginsig.EmbedSignatureFile(soPath, priv); err != nil {
		return err
	}
	fmt.Printf("📦 埋め込み署名しました: %s\n", soPath)
	return nil
}

// embedAllIn は dir 直下のすべての *.so に署名を埋め込む。
// embedAllIn embeds a signature into every *.so directly under dir.
func embedAllIn(dir, privPath string) error {
	if privPath == "" {
		return errors.New("-embed-all には -private-key が必要です")
	}
	priv, err := pluginsig.LoadPrivateKey(privPath)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("ディレクトリを読めません (%s): %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".so") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return fmt.Errorf("%s に .so がありません", dir)
	}
	sort.Strings(names)
	failed := 0
	for _, n := range names {
		so := filepath.Join(dir, n)
		if err := pluginsig.EmbedSignatureFile(so, priv); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			failed++
			continue
		}
		fmt.Printf("📦 %s\n", so)
	}
	if failed > 0 {
		return fmt.Errorf("%d 件の埋め込み署名に失敗しました", failed)
	}
	return nil
}

// verifyOne はファイルを検証する。埋め込みトレーラを先に試し、無ければ分離
// <file>.sig を使う。一致したソースを表示する。
// verifyOne verifies a file. The embedded trailer is tried first; if there is
// none, the detached <file>.sig is used. The source that matched is printed.
func verifyOne(soPath, pubPath string) error {
	if pubPath == "" {
		return errors.New("-verify には -public-key が必要です")
	}
	pub, err := pluginsig.LoadPublicKey(pubPath)
	if err != nil {
		return err
	}
	source, err := pluginsig.VerifyFileAny(soPath, pub)
	if err != nil {
		return fmt.Errorf("検証失敗: %w", err)
	}
	fmt.Printf("✅ 署名は有効です: %s (%s)\n", soPath, source)
	return nil
}
