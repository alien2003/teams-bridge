//go:build tools

// Package deps pins the module's direct dependencies before the code that uses them exists.
package deps

import (
	_ "github.com/BurntSushi/toml"
	_ "github.com/chromedp/cdproto"
	_ "github.com/chromedp/chromedp"
	_ "golang.org/x/crypto/chacha20poly1305"
	_ "rsc.io/qr"
)
