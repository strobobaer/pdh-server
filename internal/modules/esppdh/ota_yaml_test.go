package esppdh

import (
	"strings"
	"testing"
)

func otaSample() Device {
	d := Samples()[0].Device
	d.OTAPassword = "ota-kennwort-1"
	return d
}

func TestGenerateHasOTAAndSafeMode(t *testing.T) {
	cfg, sec, err := Generate(otaSample())
	if err != nil {
		t.Fatal(err)
	}
	c := string(cfg)
	for _, want := range []string{"ota:", "platform: esphome", "password: !secret ota_password", "safe_mode:"} {
		if !strings.Contains(c, want) {
			t.Errorf("YAML ohne %q:\n%s", want, c)
		}
	}
	if !strings.Contains(string(sec), "ota_password") || strings.Contains(c, "ota-kennwort-1") {
		t.Errorf("Kennwort gehört nur in die Geheimnis-Datei")
	}
}

func TestFirmwareIDNotInConfigHash(t *testing.T) {
	a, b := otaSample(), otaSample()
	a.FirmwareID, b.FirmwareID = "01a1ffff-0000-7000-8000-00000000000a", "01a1ffff-0000-7000-8000-00000000000b"
	if ConfigHash(a) != ConfigHash(b) || ConfigHash(a) == "" {
		t.Errorf("Firmware-Kennung darf die Prüfsumme nicht ändern")
	}
	ca, _, _ := Generate(a)
	cb, _, _ := Generate(b)
	if string(ca) == string(cb) || !strings.Contains(string(ca), a.FirmwareID) {
		t.Errorf("Firmware-Kennung steht nicht in der YAML")
	}
}

func TestFirmwareTopic(t *testing.T) {
	d := otaSample()
	ts, err := Topics(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range ts {
		if ti.Topic == "esppdh/"+d.Slug+"/info/firmware" {
			if ti.Group != GroupDevice || ti.Interval != 0 {
				t.Errorf("Firmware-Thema: %+v", ti)
			}
			return
		}
	}
	t.Errorf("Firmware-Thema fehlt: %+v", ts)
}

func TestValidateDeviceNeedsOTAPassword(t *testing.T) {
	d := otaSample()
	d.OTAPassword = ""
	msgs := ValidateDevice(d)
	if !strings.Contains(strings.Join(msgs, "\n"), "Das Kennwort für Updates übers Netz fehlt.") {
		t.Errorf("Meldungen: %v", msgs)
	}
}
