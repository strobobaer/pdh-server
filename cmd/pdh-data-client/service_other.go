//go:build !windows && !linux

package main

import (
	"context"
	"errors"
	"path/filepath"
)

var errUnsupported = errors.New("Dienst-Einrichtung wird auf diesem Betriebssystem nicht unterstützt")

func configPath() string { return "pdh-data-client.json" }
func stateDir() string   { return filepath.Join(".", "pdh-data-client-state") }

func runAsService(func(context.Context) error) (bool, error) { return false, nil }
func installService(Config) error                             { return errUnsupported }
func uninstallService() error                                 { return errUnsupported }
