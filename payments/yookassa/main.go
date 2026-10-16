// adapter-yookassa is the Hikari payment adapter for YooKassa (protocol v1, PROTOCOL.md).
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
		err = adapter.Run(&yooKassa{api: "https://api.yookassa.ru/v3", hc: adapter.HTTPClient(), manifest: m})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "adapter-yookassa:", err)
		os.Exit(1)
	}
}
