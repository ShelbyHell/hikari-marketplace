// adapter-cardlink is the Hikari payment adapter for Cardlink (protocol v1, PROTOCOL.md).
package main

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"

	"github.com/ShelbyHell/hikari-marketplace/internal/adapter"
	"github.com/ShelbyHell/hikari-marketplace/internal/catalog"
)

//go:embed adapter.json
var manifestJSON []byte

func main() {
	m, err := catalog.ParseManifest(manifestJSON)
	if err == nil {
		log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		err = adapter.Run(&cardlink{api: "https://cardlink.link/api/v1", hc: adapter.HTTPClient(), manifest: m, log: log})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "adapter-cardlink:", err)
		os.Exit(1)
	}
}
