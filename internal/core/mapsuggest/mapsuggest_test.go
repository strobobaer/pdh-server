package mapsuggest

import (
	"reflect"
	"testing"
)

func TestTokens(t *testing.T) {
	want := map[string][]string{
		"Halle2/Presse_3/Öl-Temp": {"halle", "2", "presse", "3", "oel", "temp"},
		"motorStromL1":            {"motor", "strom", "l", "1"},
		"Abfrage › Spalte":        {"abfrage", "spalte"},
		"Linie 03 Straße":         {"linie", "3", "strasse"},
		"":                        nil,
	}
	for in, w := range want {
		if got := Tokens(in); !reflect.DeepEqual(got, w) {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestSuggestAsset(t *testing.T) {
	assets := []Asset{
		{"a3", "Presse 3", "Werk › Halle 2 › Presse 3"},
		{"a30", "Presse 30", "Werk › Halle 2 › Presse 30"},
		{"h2", "Halle 2", "Werk › Halle 2"},
	}
	got := Suggest([]Source{
		{Ref: "halle2/presse3/strom", Label: "halle2/presse3/strom", Sample: "12.5"},
		{Ref: "x", Label: "lager/temp"},
		{Ref: "p30", Label: "Presse_30/Öl-Temp"},
	}, assets)
	if len(got) != 3 {
		t.Fatalf("je Quelle ein Eintrag: %d", len(got))
	}
	if got[0].AssetID != "a3" || got[0].AssetPath != "Werk › Halle 2 › Presse 3" {
		t.Errorf("Presse 3 erwartet (nie Presse 30, tiefer als Halle 2): %+v", got[0])
	}
	if got[1].AssetID != "" || got[1].Score != 0 {
		t.Errorf("ohne Treffer leer: %+v", got[1])
	}
	if got[2].AssetID != "a30" {
		t.Errorf("Presse 30 erwartet: %+v", got[2])
	}
	if got[0].Ref != "halle2/presse3/strom" || got[0].Sample != "12.5" {
		t.Errorf("Quelle bleibt erhalten: %+v", got[0])
	}
}

func TestSuggestName(t *testing.T) {
	a := []Asset{{"a3", "Presse 3", "Werk › Halle 2 › Presse 3"}}
	got := Suggest([]Source{
		{Ref: "r", Label: "halle2/presse3/oel_temp", Sample: "41"},
		{Ref: "q", Label: "Maschinendaten › motorStromL1", Sample: "3"},
		{Ref: "j", Label: "halle2/presse3 › data.temp", Sample: "20"},
	}, a)
	if got[0].Name != "Oel Temp" || got[0].Note != "Einheit °C" {
		t.Errorf("Topic: %+v", got[0])
	}
	if got[1].Name != "Motor Strom L1" || got[1].Note != "Einheit A" {
		t.Errorf("Abfrage: %+v", got[1])
	}
	if got[2].Name != "Data Temp" {
		t.Errorf("JSON-Feld: %+v", got[2])
	}
}

func TestUnitHint(t *testing.T) {
	for in, w := range map[[2]string]string{
		{"strom_l1", "3.2"}:     "Einheit A",
		{"spannung", "230"}:     "Einheit V",
		{"wirkleistung", "4"}:   "Einheit kW",
		{"energie_kwh", "100"}:  "Einheit kWh",
		{"oeltemperatur", "20"}: "Einheit °C",
		{"druck", "6"}:          "Einheit bar",
		{"drehzahl", "1500"}:    "Einheit 1/min",
		{"feuchte", "40"}:       "Einheit %",
		{"frequenz", "50"}:      "Einheit Hz",
		{"laeuft", "1"}:         "Zustand (0/1)",
		{"status", "TRUE"}:      "Zustand (ein/aus)",
		{"name", "abc"}:         "",
	} {
		if got := UnitHint(in[0], in[1]); got != w {
			t.Errorf("%v: %q", in, got)
		}
	}
}

func TestExportFieldName(t *testing.T) {
	for in, w := range map[[2]string]string{
		{"Presse 3", "Öl-Temperatur"}: "Presse_3_Oel_Temperatur",
		{"", "Strom"}:                 "Strom",
		{"", "–"}:                     "Wert",
		{"Straße", "Ü"}:               "Strasse_Ue",
	} {
		if got := ExportFieldName(in[0], in[1]); got != w {
			t.Errorf("%v: %q", in, got)
		}
	}
}

// Schlussprüfung I1: Die Nummer gehört zum Wort davor – „presse30“ unter
// „linie3“ ist nicht „Presse 3“, „halle3/presse2“ nicht „Halle 2 › Presse 3“.
// Gleich gute Anlagen (gleicher Name in zwei Hallen) ergeben keinen Vorschlag.
func TestSuggestAssetNumberBelongsToWord(t *testing.T) {
	assets := []Asset{
		{"l3p3", "Presse 3", "Werk › Linie 3 › Presse 3"},
		{"l5p30", "Presse 30", "Werk › Linie 5 › Presse 30"},
		{"h2p3", "Presse 3", "Werk › Halle 2 › Presse 3"},
		{"h3p2", "Presse 2", "Werk › Halle 3 › Presse 2"},
		{"h4p3", "Presse 3", "Werk › Halle 4 › Presse 3"},
	}
	got := Suggest([]Source{
		{Label: "linie3/presse30/strom"},
		{Label: "halle3/presse2/strom"},
		{Label: "presse3/strom"},
		{Label: "halle4/presse_03/strom"},
	}, assets)
	if got[0].AssetID == "l3p3" {
		t.Errorf("presse30 darf nicht Presse 3 treffen: %+v", got[0])
	}
	if got[1].AssetID != "h3p2" {
		t.Errorf("halle3/presse2 → Halle 3 › Presse 2 erwartet: %+v", got[1])
	}
	if got[2].AssetID != "" || got[2].Score != 0 {
		t.Errorf("mehrdeutig (Presse 3 gibt es dreimal) → kein Vorschlag: %+v", got[2])
	}
	if got[3].AssetID != "h4p3" {
		t.Errorf("halle4/presse_03 → Halle 4 › Presse 3 erwartet: %+v", got[3])
	}
}
