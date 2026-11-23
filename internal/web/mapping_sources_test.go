package web

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"pdh/internal/core/mapsuggest"
)

func refsOf(src []mapsuggest.Source) []string {
	var out []string
	for _, s := range src {
		out = append(out, s.Ref)
	}
	return out
}

func TestQuerySources(t *testing.T) {
	qs := []connQuery{
		{ID: "q1", Name: "Maschine", Spec: map[string]string{"row": "last"}, LastResult: &queryResult{Columns: []string{"strom", "druck"}, Rows: [][]string{{"1", "2"}, {"3", "4"}}}},
		{ID: "q2", Name: "Leer"},
	}
	got := querySources(qs)
	if !reflect.DeepEqual(refsOf(got), []string{queryRef("q1", "strom"), queryRef("q1", "druck")}) {
		t.Fatalf("Refs: %v", refsOf(got))
	}
	if got[0].Label != "Maschine › strom" || got[0].Sample != "3" || got[1].Sample != "4" {
		t.Errorf("Label/Wert der letzten Zeile: %+v", got)
	}
}

func TestTabularSources(t *testing.T) {
	got := tabularSources([]string{"Strom", "Druck"}, []string{"12"})
	if !reflect.DeepEqual(refsOf(got), []string{"Strom", "Druck"}) || got[0].Sample != "12" || got[1].Sample != "" {
		t.Errorf("%+v", got)
	}
}

func TestMqttStatSources(t *testing.T) {
	got := mqttStatSources([]MqttTopicStat{
		{Topic: "t", Last: `{"a":{"b":1}}`, Fields: []JSONField{{Path: "a.b", Value: "1"}}},
		{Topic: "halle/1/temp", Last: "21.5"},
	})
	if !reflect.DeepEqual(refsOf(got), []string{"t", "t#a.b", "halle/1/temp"}) {
		t.Fatalf("Refs: %v", refsOf(got))
	}
	if got[1].Label != "t › a.b" || got[1].Sample != "1" || got[2].Sample != "21.5" {
		t.Errorf("%+v", got)
	}
}

func TestFilterSources(t *testing.T) {
	src := []mapsuggest.Source{{Ref: "a"}, {Ref: "b"}, {Ref: "a"}, {Ref: "c"}, {Ref: "d"}}
	if got := refsOf(filterSources(src, map[string]bool{"b": true}, 2)); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("%v", got)
	}
}

func TestSampleMqttTopicsBroker(t *testing.T) {
	srv, host, port := startTestBroker(t)
	if err := srv.Publish("halle/1/temp", []byte("21.5"), true, 0); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]string{"broker_mode": "external", "ext_broker_host": host, "ext_broker_port": port}
	stats, err := sampleMqttTopics(context.Background(), cfg, time.Second, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range stats {
		found = found || (s.Topic == "halle/1/temp" && s.Last == "21.5")
	}
	if !found {
		t.Errorf("Thema fehlt: %+v", stats)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closed, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	start := time.Now()
	_, err = sampleMqttTopics(context.Background(), map[string]string{"broker_mode": "external", "ext_broker_host": "127.0.0.1", "ext_broker_port": closed}, time.Second, 100)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("Broker aus: %v nach %v", err, time.Since(start))
	}
	if err != nil && strings.TrimSpace(err.Error()) == "" {
		t.Error("leere Fehlermeldung")
	}
}
