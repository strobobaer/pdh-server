// pdh-data-client läuft beim Kunden neben der Fremddatenbank und verbindet sie
// über das Datennetz mit dem PDH-Server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"pdh/internal/datanet"
)

// version wird beim Bau per -ldflags "-X main.version=…" gesetzt. Der Client
// meldet sie an den PDH-Server; Selbst-Updates folgen mit Paket 2.
var version = "dev"

const (
	serviceName        = "pdh-data-client"
	serviceDisplayName = "PDH-Daten-Client"
	serviceDescription = "Verbindet eine Fremddatenbank über das PDH-Datennetz mit dem PDH-Server."
)

func versionLine() string {
	return fmt.Sprintf("pdh-data-client %s %s/%s", version, runtime.GOOS, runtime.GOARCH)
}

const usage = `Aufruf:
  pdh-data-client install --id <uuid> --token <token> --pdh <ip>[,<ip>] --target <port[=host:port]> [--target …] [--authkey tskey-…] [--hostname <name>]
  pdh-data-client uninstall
  pdh-data-client run [--config <datei>]
  pdh-data-client version`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(versionLine())
	case "install":
		err = cmdInstall(os.Args[2:])
	case "uninstall":
		err = uninstallService()
	case "run":
		err = cmdRun(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var c Config
	var pdh string
	var targets multiFlag
	fs.StringVar(&c.ClientID, "id", "", "Client-ID (UUID)")
	fs.StringVar(&c.Token, "token", "", "Client-Token")
	fs.StringVar(&pdh, "pdh", "", "Datennetz-Adresse(n) des PDH-Servers, durch Komma getrennt")
	fs.Var(&targets, "target", "freizugebender Port, optional =host:port (mehrfach möglich)")
	fs.StringVar(&c.AuthKey, "authkey", "", "Einmal-Schlüssel (tskey-…)")
	fs.StringVar(&c.Hostname, "hostname", "", "Name im Datennetz")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.Hostname == "" {
		c.Hostname, _ = os.Hostname()
	}
	c.PDH = normalizePDH(strings.Split(pdh, ","))
	for _, s := range targets {
		t, err := datanet.ParseTarget(s)
		if err != nil {
			return err
		}
		c.Targets = append(c.Targets, t)
	}
	c.StateDir = stateDir()
	if err := c.Validate(); err != nil {
		return err
	}
	return installService(c)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	path := fs.String("config", configPath(), "Konfigurationsdatei")
	if err := fs.Parse(args); err != nil {
		return err
	}
	handled, err := runAsService(func(ctx context.Context) error { return runFromConfig(ctx, *path) })
	if handled {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runFromConfig(ctx, *path)
}

func runFromConfig(ctx context.Context, path string) error {
	c, err := LoadConfig(path)
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("Konfiguration %s: %w", path, err)
	}
	node, err := datanet.NewTSNode(datanet.NodeConfig{Dir: c.StateDir, Hostname: c.Hostname, AuthKey: c.AuthKey})
	if err != nil {
		return err
	}
	defer node.Close()
	log.Printf("pdh-data-client %s gestartet", version)
	err = Run(ctx, c, node, func(c Config) error { return SaveConfig(path, c) })
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
