package esppdh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testUploadJob() UploadJob {
	j := testJob()
	return UploadJob{ID: j.ID, Slug: j.Slug, Address: "192.0.2.50", Config: j.Config, Secrets: j.Secrets,
		Firmware: []byte("OTA-BYTES"), SecretValues: j.SecretValues}
}

func TestUploadArgs(t *testing.T) {
	r, work, out, _ := fakeRunner(t, "upload")
	log, err := r.Upload(context.Background(), testUploadJob(), nil)
	if err != nil {
		t.Fatalf("Upload: %v\n%s", err, log)
	}
	var info struct {
		Args     []string `json:"args"`
		Firmware string   `json:"firmware"`
	}
	b, _ := os.ReadFile(filepath.Join(out, "upload.json"))
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatalf("Attrappe nicht aufgerufen: %v", err)
	}
	want := []string{"upload", "./esppdh.yaml", "--device", "192.0.2.50", "--ota-platform", "esphome", "--file", "firmware.ota.bin"}
	if !slices.Equal(info.Args, want) {
		t.Errorf("Argumente %v, erwartet %v", info.Args, want)
	}
	if info.Firmware != "OTA-BYTES" {
		t.Errorf("Firmware-Datei: %q", info.Firmware)
	}
	assertGone(t, work)
}

func TestUploadRedacts(t *testing.T) {
	r, _, _, _ := fakeRunner(t, "upload")
	log, err := r.Upload(context.Background(), testUploadJob(), nil)
	if err != nil || strings.Contains(log, testSecret) || !strings.Contains(log, "OTA successful") {
		t.Errorf("Verlauf (%v):\n%s", err, log)
	}
}

func TestUploadRejectsBadAddress(t *testing.T) {
	r, _, _, calls := fakeRunner(t, "upload")
	j := testUploadJob()
	for _, a := range []string{"evil; rm", "", "::1", "esppdh.local", "192.0.2.300"} {
		j.Address = a
		if _, err := r.Upload(context.Background(), j, nil); err == nil {
			t.Errorf("Adresse %q angenommen", a)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("ESPHome trotz ungültiger Adresse aufgerufen (%d)", calls.Load())
	}
}

func TestUploadFailureReturnsError(t *testing.T) {
	r, work, _, _ := fakeRunner(t, "uploadfail")
	log, err := r.Upload(context.Background(), testUploadJob(), nil)
	if err == nil || !strings.Contains(log, "timed out") {
		t.Errorf("Fehler nicht gemeldet (%v):\n%s", err, log)
	}
	assertGone(t, work)
}
