package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionLine(t *testing.T) {
	l := versionLine()
	if !strings.HasPrefix(l, "pdh-data-client dev ") || !strings.HasSuffix(l, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Fatalf("unerwartete Versionszeile %q", l)
	}
}
