package web

import (
	"reflect"
	"testing"
)

func TestEffectiveCardNotify(t *testing.T) {
	cases := []struct {
		name   string
		stored []string
		want   []string
	}{
		{"leer = Vorgabe", nil, []string{"overlay"}},
		{"leere Liste", []string{}, []string{"overlay"}},
		{"Reihenfolge fest", []string{"banner", "toast"}, []string{"toast", "banner"}},
		{"alle", []string{"front", "banner", "overlay", "toast"}, []string{"toast", "overlay", "banner", "front"}},
		{"off allein", []string{"off"}, []string{"off"}},
		{"doppelt", []string{"toast", "toast"}, []string{"toast"}},
	}
	for _, c := range cases {
		if got := effectiveCardNotify(c.stored); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeCardNotify(t *testing.T) {
	if got, err := normalizeCardNotify([]string{"banner", "toast", "banner"}); err != nil || !reflect.DeepEqual(got, []string{"toast", "banner"}) {
		t.Errorf("normalisieren: %v %v", got, err)
	}
	if got, err := normalizeCardNotify(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("leer: %#v %v", got, err)
	}
	if got, err := normalizeCardNotify([]string{"off"}); err != nil || !reflect.DeepEqual(got, []string{"off"}) {
		t.Errorf("off: %v %v", got, err)
	}
	for _, bad := range [][]string{{"off", "toast"}, {"quatsch"}, {"toast", "quatsch"}, {""}} {
		if _, err := normalizeCardNotify(bad); err == nil {
			t.Errorf("%v wurde nicht abgelehnt", bad)
		}
	}
}
