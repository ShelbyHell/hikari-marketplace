// adapter-cryptobot is the Hikari payment adapter for CryptoBot's Crypto Pay
// (protocol v1, PROTOCOL.md).
package main

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/ShelbyHell/hikari-marketplace/internal/adapter"
	"github.com/ShelbyHell/hikari-marketplace/internal/catalog"
)

//go:embed adapter.json
var manifestJSON []byte

func main() {
	m, err := catalog.ParseManifest(manifestJSON)
	if err == nil {
		err = adapter.Run(&cryptoBot{api: "https://pay.crypt.bot/api", testAPI: "https://testnet-pay.crypt.bot/api",
			hc: adapter.HTTPClient(), manifest: m})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "adapter-cryptobot:", err)
		os.Exit(1)
	}
}
