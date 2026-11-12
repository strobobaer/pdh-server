package web

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"pdh/internal/integrations/cloudflare"
)

// fakeCFRecords ist ein Cloudflare-Ersatz im Speicher.
type fakeCFRecords struct {
	recs    []cloudflare.Record
	next    int
	deleted []string
}

func (f *fakeCFRecords) CreateRecord(_ context.Context, _ string, r cloudflare.Record) (*cloudflare.Record, error) {
	f.next++
	r.ID = fmt.Sprintf("rec%d", f.next)
	f.recs = append(f.recs, r)
	return &r, nil
}

func (f *fakeCFRecords) DeleteRecord(_ context.Context, _, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeCFRecords) Records(context.Context, string) ([]cloudflare.Record, error) {
	return f.recs, nil
}

// Present legt TXT "_acme-challenge.nc.firma.de" mit TTL 60, nicht proxied, an und gibt die
// Kennung zurück; CleanUp löscht genau diese Kennung.
func TestCertDNSPresentCleanUp(t *testing.T) {
	api := &fakeCFRecords{}
	d := &certDNS{api: api, zoneID: "z1"}
	id, err := d.Present(context.Background(), "_acme-challenge.nc.firma.de", "wert")
	if err != nil {
		t.Fatal(err)
	}
	if id != "rec1" || len(api.recs) != 1 {
		t.Fatalf("id=%q recs=%d", id, len(api.recs))
	}
	r := api.recs[0]
	if r.Type != "TXT" || r.Name != "_acme-challenge.nc.firma.de" || r.Content != "wert" || r.TTL != 60 || r.Proxied {
		t.Fatalf("falscher Eintrag: %+v", r)
	}
	if err := d.CleanUp(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(api.deleted) != 1 || api.deleted[0] != "rec1" {
		t.Fatalf("gelöscht: %v", api.deleted)
	}
}

// lookupTXT liefert ["alt","wert"] → true; ["alt"] → false; Fehler "no such host" → false ohne Fehler.
func TestCertDNSVisible(t *testing.T) {
	cases := []struct {
		name string
		vals []string
		err  error
		want bool
		fail bool
	}{
		{"da", []string{"alt", "wert"}, nil, true, false},
		{"fehlt", []string{"alt"}, nil, false, false},
		{"nochNichtDa", nil, errors.New("lookup x: no such host"), false, false},
		{"echterFehler", nil, errors.New("Netz kaputt"), false, true},
	}
	for _, c := range cases {
		d := &certDNS{lookupTXT: func(context.Context, string) ([]string, error) { return c.vals, c.err }}
		got, err := d.Visible(context.Background(), "_acme-challenge.nc.firma.de", "wert")
		if got != c.want || (err != nil) != c.fail {
			t.Errorf("%s: got=%v err=%v", c.name, got, err)
		}
	}
}

// Zwei _acme-challenge-Einträge unter nc.firma.de, nur einer steht in remembered → nur dessen
// Kennung; der fremde Eintrag bleibt.
func TestCertLeftovers(t *testing.T) {
	recs := []cloudflare.Record{
		{ID: "a", Type: "TXT", Name: "_acme-challenge.nc.firma.de"},
		{ID: "b", Type: "TXT", Name: "_acme-challenge.nc.firma.de"},
		{ID: "c", Type: "A", Name: "nc.firma.de"},
	}
	got := certLeftovers(recs, map[string]string{"a": "_acme-challenge.nc.firma.de", "gone": "_acme-challenge.nc.firma.de"})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("got %v", got)
	}
}
