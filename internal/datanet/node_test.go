package datanet

import (
	"os"
	"path/filepath"
	"testing"

	"tailscale.com/envknob"

	"pdh/internal/datanet/nodetest"
)

func TestHasState(t *testing.T) {
	d := t.TempDir()
	if HasState(d) {
		t.Fatal("leeres Verzeichnis hat keinen Zustand")
	}
	if err := os.WriteFile(filepath.Join(d, "tailscaled.state"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasState(d) {
		t.Fatal("Zustand nicht erkannt")
	}
}

func TestFakeIsNode(t *testing.T) { var _ Node = &nodetest.Fake{} }

func TestNewTSNodeDisablesLogUpload(t *testing.T) {
	if _, err := NewTSNode(NodeConfig{Dir: t.TempDir(), Hostname: "pdh-test"}); err != nil {
		t.Fatal(err)
	}
	if !envknob.NoLogsNoSupport() {
		t.Fatal("Protokollübertragung an log.tailscale.com ist nicht abgeschaltet")
	}
}
