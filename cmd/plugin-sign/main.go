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
)

const usage = `plugin-sign - Kizuna-Eye プラグイン署名ツール (A-3)

使い方:
  plugin-sign -gen-key -key-dir DIR
      新しい Ed25519 鍵対を生成し、DIR/plugin_signing.key (0600) と
      DIR/plugin_signing.pub (0644) を書き出す。
      秘密鍵は共有上に置かず、オフホストで保管すること。

  plugin-sign -sign FILE -private-key KEYFILE [-out SIGFILE]
      FILE の分離署名を書き出す（既定: FILE.sig）。

  plugin-sign -sign-all DIR -private-key KEYFILE
      DIR 直下の *.so すべてに署名する。サブディレクトリは走査しない。

  plugin-sign -verify FILE -public-key PUBFILE
      FILE と FILE.sig を検証する（不一致なら exit 1）。

  plugin-sign -print-public-key -private-key KEYFILE
      秘密鍵から公開鍵を base64 で表示する（PUBFILE への配布用）。

鍵・署名の形式:
  公開鍵   : PKIX PEM / hex / base64
  秘密鍵   : PKCS#8 PEM / hex / base64（64 バイト鍵 または 32 バイト seed）
  署名     : base64 / hex（'#' 始まりのコメント行と空白は無視）
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
		signFile  = fs.String("sign", "", "署名する .so ファイル")
		signAll   = fs.String("sign-all", "", "ディレクトリ直下の全 .so に署名する")
		verify    = fs.String("verify", "", "検証する .so ファイル（<file>.sig を読む）")
		privKey   = fs.String("private-key", "", "秘密鍵ファイル")
		pubKey    = fs.String("public-key", "", "公開鍵ファイル")
		outPath   = fs.String("out", "", "署名の出力先（既定: <file>.sig）")
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
	if err := os.WriteFile(privPath, []byte(pluginsig.EncodePrivateKey(priv)+"\n"), 0o600); err != nil {
		return fmt.Errorf("秘密鍵の書き込みに失敗 (%s): %w", privPath, err)
	}
	if err := os.WriteFile(pubPath, []byte(pluginsig.EncodePublicKey(pub)+"\n"), 0o644); err != nil {
		return fmt.Errorf("公開鍵の書き込みに失敗 (%s): %w", pubPath, err)
	}

	fmt.Printf("🔑 鍵対を生成しました\n  秘密鍵: %s (0600, オフホストで保管してください)\n  公開鍵: %s\n",
		privPath, pubPath)
	fmt.Printf("  公開鍵 (base64): %s\n", pluginsig.EncodePublicKey(pub))
	return nil
}

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

// verifyOne verifies a file against its detached signature (<file>.sig).
func verifyOne(soPath, pubPath string) error {
	if pubPath == "" {
		return errors.New("-verify には -public-key が必要です")
	}
	pub, err := pluginsig.LoadPublicKey(pubPath)
	if err != nil {
		return err
	}
	sigPath := pluginsig.SigPath(soPath)
	if err := pluginsig.VerifyFile(soPath, sigPath, pub); err != nil {
		return fmt.Errorf("検証失敗: %w", err)
	}
	fmt.Printf("✅ 署名は有効です: %s (%s)\n", soPath, sigPath)
	return nil
}
