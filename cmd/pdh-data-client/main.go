// pdh-data-client läuft beim Kunden neben der Fremddatenbank und verbindet sie
// über das Datennetz mit dem PDH-Server.
package main

import (
	"fmt"
	"os"
	"runtime"
)

// version wird beim Bau per -ldflags "-X main.version=…" gesetzt.
var version = "dev"

func versionLine() string {
	return fmt.Sprintf("pdh-data-client %s %s/%s", version, runtime.GOOS, runtime.GOARCH)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(versionLine())
		return
	}
	fmt.Fprintln(os.Stderr, "Aufruf: pdh-data-client version")
	os.Exit(2)
}
