package calc

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"pdh/internal/core/formula"
)

func TestErrorTexts(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&PlannedExistsError{Date: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}, "Für den 01.12.2026 gibt es schon eine geplante Fassung – bitte diese ändern"},
		{&CycleError{Names: []string{"Summe", "Anteil", "Summe"}}, "Kreisbezug: Summe → Anteil → Summe"},
		{&InUseError{Users: []string{"Summe", "Anteil"}}, "wird als Eingangswert gebraucht von „Summe“, „Anteil“"},
		{&UnknownInputError{Short: "C"}, "Die Formel nennt „C“ – das ist kein Eingangswert"},
		{&InputGoneError{Short: "B"}, "Den Eingangswert „B“ gibt es nicht mehr"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%T: %q", c.err, got)
		}
	}
}

func TestUserError(t *testing.T) {
	_, syn := formula.Parse("A +")
	for _, err := range []error{ErrBadName, ErrPastDate, ErrVersionFixed, ErrTooManyInputs, ErrNotFound, syn,
		&ShortNameError{Short: "MIN"}, &CycleError{}, &InUseError{}, &PlannedExistsError{},
		fmt.Errorf("speichern: %w", ErrBadDecimals)} {
		if !UserError(err) {
			t.Errorf("%v sollte für die Person gedacht sein", err)
		}
	}
	for _, err := range []error{nil, errors.New("pq: connection refused")} {
		if UserError(err) {
			t.Errorf("%v sollte kein Anwenderfehler sein", err)
		}
	}
}
