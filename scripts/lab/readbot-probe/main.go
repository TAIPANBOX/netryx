// Lab probe, not shipped: runs the console's own alerts.ReadBot on a router
// and prints only whether the Hermes bot was found and the reason
// (docs/flint2-measured.md section 8, D5). The token is never read (ReadBot
// only stats its file) and the chat id is not printed.
//
// Built on forge for the Flint 2 and run there once:
//
//	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/nxbotcheck ./scripts/lab/readbot-probe
//	scp -O /tmp/nxbotcheck flint2:/tmp/ && ssh flint2 '/tmp/nxbotcheck; rm -f /tmp/nxbotcheck'
package main

import (
	"fmt"
	"os"

	"netryx/internal/alerts"
)

func main() {
	b, why := alerts.ReadBot(alerts.HermesConfig, os.Stat)
	fmt.Printf("config %s found=%v reason=%q tokenfile=%s chat_set=%v\n", alerts.HermesConfig, why == "", why, b.TokenFile, b.Chat != "")
}
