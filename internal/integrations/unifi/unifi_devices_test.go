package unifi

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testSiteID = "88f7af54-98f8-306a-a1c7-c9349722b1f6"

func TestDevicesPaged(t *testing.T) {
	f := newFakeUDM(t)
	devs, err := f.client().Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 {
		t.Fatalf("Geräte: %+v", devs)
	}
	want := Device{ID: "0b7a1c52-3d11-4f0e-9a43-1e6d5c2a8b01", Name: "Switch Halle 1", Model: "USW-Pro-24-PoE",
		MAC: "94:2a:6f:26:c6:ca", IP: "192.168.1.10", Firmware: "7.1.26", State: "ONLINE", Features: []string{"switching"}}
	if !reflect.DeepEqual(devs[0], want) {
		t.Errorf("Gerät 1: %+v", devs[0])
	}
	if !devs[1].FirmwareUpdatable || devs[1].State != "OFFLINE" || devs[1].MAC != "94:2A:6F:26:C6:CB" {
		t.Errorf("Gerät 2: %+v", devs[1])
	}
	if devs[2].Name != "UDM Pro" || len(devs[2].Features) != 2 {
		t.Errorf("Gerät 3: %+v", devs[2])
	}
	var pages int
	for _, c := range f.calls {
		if c == "GET /proxy/network/integration/v1/sites/"+testSiteID+"/devices" {
			pages++
		}
	}
	if pages != 2 {
		t.Errorf("Seitenabrufe: %d, Aufrufe: %v", pages, f.calls)
	}
	for _, k := range f.keys {
		if k != testKey {
			t.Errorf("X-API-KEY: %q", k)
		}
	}
}

func TestDeviceDetailPorts(t *testing.T) {
	f := newFakeUDM(t)
	d, err := f.client().DeviceDetail(context.Background(), "0b7a1c52-3d11-4f0e-9a43-1e6d5c2a8b01")
	if err != nil {
		t.Fatal(err)
	}
	if d.UplinkDeviceID != "0b7a1c52-3d11-4f0e-9a43-1e6d5c2a8b03" || d.Name != "Switch Halle 1" {
		t.Errorf("Detail: %+v", d)
	}
	if !reflect.DeepEqual(d.Features, []string{"switching"}) {
		t.Errorf("Features: %v", d.Features)
	}
	if len(d.Ports) != 3 {
		t.Fatalf("Ports: %+v", d.Ports)
	}
	if d.Ports[0] != (Port{Idx: 1, Connector: "RJ45", State: "UP", SpeedMbps: 1000, MaxSpeedMbps: 2500, PoEState: "UP"}) {
		t.Errorf("Port 1: %+v", d.Ports[0])
	}
	if d.Ports[1].State != "DOWN" || d.Ports[1].SpeedMbps != 0 || d.Ports[1].PoEState != "DOWN" {
		t.Errorf("Port 2: %+v", d.Ports[1])
	}
	if d.Ports[2].PoEState != "" || d.Ports[2].Connector != "SFPPLUS" {
		t.Errorf("Port 25 ohne PoE: %+v", d.Ports[2])
	}
	if _, err := f.client().DeviceDetail(context.Background(), " "); err == nil {
		t.Error("leere Kennung muss abgelehnt werden")
	}
}

func TestClientsTypes(t *testing.T) {
	f := newFakeUDM(t)
	cl, err := f.client().Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cl) != 3 {
		t.Fatalf("Clients: %+v", cl)
	}
	if cl[0] != (NetClient{ID: "5c1d0e9f-1a2b-4c3d-8e4f-0a1b2c3d4e01", Name: "SPS Presse 2", MAC: "00:1B:1B:AA:BB:CC",
		IP: "192.168.1.60", Type: "WIRED", UplinkDeviceID: "0b7a1c52-3d11-4f0e-9a43-1e6d5c2a8b01"}) {
		t.Errorf("WIRED: %+v", cl[0])
	}
	if cl[1].Type != "WIRELESS" || cl[1].MAC != "a4:83:e7:11:22:33" {
		t.Errorf("WIRELESS: %+v", cl[1])
	}
	if cl[2].Type != "VPN" || cl[2].MAC != "" || cl[2].UplinkDeviceID != "" {
		t.Errorf("VPN: %+v", cl[2])
	}
}

func TestDevicesLegacyUnsupported(t *testing.T) {
	f := newFakeUDM(t)
	f.legacy = true
	c := f.client()
	ctx := context.Background()
	if _, err := c.Devices(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Devices: %v", err)
	}
	if _, err := c.DeviceDetail(ctx, "x"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("DeviceDetail: %v", err)
	}
	if _, err := c.Clients(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Clients: %v", err)
	}
}

func TestDevicesWrongPin(t *testing.T) {
	f := newFakeUDM(t)
	c := New(f.srv.URL, testKey, "", strings.Repeat("ab", 32))
	ctx := context.Background()
	if _, err := c.Devices(ctx); !errors.Is(err, ErrPinChanged) {
		t.Errorf("Devices: %v", err)
	}
	if _, err := c.DeviceDetail(ctx, "x"); !errors.Is(err, ErrPinChanged) {
		t.Errorf("DeviceDetail: %v", err)
	}
	if _, err := c.Clients(ctx); !errors.Is(err, ErrPinChanged) {
		t.Errorf("Clients: %v", err)
	}
	if f.hits != 0 {
		t.Fatalf("Server sah %d Anfragen – Schlüssel darf nicht gesendet werden", f.hits)
	}
}
