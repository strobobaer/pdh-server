package maintenance

import "testing"

func fp(f float64) *float64 { return &f }

func TestMeasureInRange(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		min, max *float64
		want     *bool
	}{
		{"ohne Grenzen", "5", nil, nil, nil},
		{"keine Zahl", "abc", fp(1), fp(2), nil},
		{"leer", "", fp(1), fp(2), nil},
		{"innerhalb", "1,5", fp(1), fp(2), ptrBool(true)},
		{"auf Grenze", "2", fp(1), fp(2), ptrBool(true)},
		{"unter Min", "0.9", fp(1), fp(2), ptrBool(false)},
		{"nur Max, drüber", "12", nil, fp(10), ptrBool(false)},
		{"nur Min, drüber", "12", fp(10), nil, ptrBool(true)},
	}
	for _, c := range cases {
		got := MeasureInRange(c.value, c.min, c.max)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v, want %v", c.name, deref(got), deref(c.want))
		}
	}
}

func TestToTemplateItem(t *testing.T) {
	// Grenzen nur beim Messwert
	it, err := createChecklistTemplateItemInput{Label: "Sicht", ItemType: "checkbox", MinValue: fp(1), Unit: "bar"}.toTemplateItem()
	if err != nil || it.MinValue != nil || it.Unit != "" {
		t.Fatalf("checkbox: grenzen müssen entfallen, got %+v, %v", it, err)
	}
	it, err = createChecklistTemplateItemInput{Label: "Druck", ItemType: "number", TargetValue: fp(6), MinValue: fp(5), MaxValue: fp(7), Unit: " bar "}.toTemplateItem()
	if err != nil || it.Unit != "bar" || *it.TargetValue != 6 || it.IntervalDays != 1 {
		t.Fatalf("number: got %+v, %v", it, err)
	}
	if _, err := (createChecklistTemplateItemInput{Label: "x", ItemType: "number", MinValue: fp(8), MaxValue: fp(7)}).toTemplateItem(); err == nil {
		t.Fatal("min > max muss abgelehnt werden")
	}
	if _, err := (createChecklistTemplateItemInput{Label: "x", ItemType: "image"}).toTemplateItem(); err == nil {
		t.Fatal("unbekannter typ muss abgelehnt werden")
	}
}

func ptrBool(b bool) *bool { return &b }

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}
