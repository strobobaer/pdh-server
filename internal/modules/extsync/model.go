package extsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Zustände eines Profils.
const (
	StateDraft  = "entwurf"
	StateTested = "getestet"
	StateArmed  = "scharf"
	StateHalted = "angehalten"
)

// Richtungen einer Feldzuordnung.
const (
	DirIn   = "herein"
	DirOut  = "hinaus"
	DirBoth = "beides"
)

// Arten der Wartelisten-Einträge.
const (
	KindUpdate  = "update"
	KindComment = "comment"
)

// Arten des Ziel-Datensatzes.
const (
	RefTicket = "ticket"
	RefFault  = "fault"
)

// Standardwerte eines neuen Profils.
const (
	DefaultIntervalMinutes = 5
	DefaultMaxNew          = 200
)

// Fehlertexte an Verknüpfungen (sync_links.last_error), nach denen gesucht wird.
const (
	// LinkErrRecordGone: der verknüpfte PDH-Datensatz wurde gelöscht. Solche
	// Verknüpfungen werden nicht wiederholt (RetryLinks), nichts wird neu angelegt.
	LinkErrRecordGone = "Datensatz im PDH nicht mehr vorhanden"
	// LinkErrForeignGone: die Fremdzeile ist beim erneuten Versuch verschwunden.
	LinkErrForeignGone = "Meldung im Fremdsystem nicht mehr vorhanden"
	// LinkErrBadKey: der gespeicherte Schlüssel der Verknüpfung ist nicht lesbar.
	LinkErrBadKey = "Schlüssel der Verknüpfung ist ungültig"
	// LinkErrInterrupted: Ausgangswerte sind vorab gespeichert, das Schreiben ins
	// PDH ist noch nicht bestätigt. KEIN Endzustand: wird wiederholt.
	LinkErrInterrupted = "Übernahme unterbrochen"
	// LinkErrBackfillWaiting: rückwirkende Meldung, die über der Obergrenze je
	// Lauf lag; reserviert ohne Datensatz, ein späterer Lauf legt sie an. KEIN
	// Fehler und kein Endzustand: zählt nicht als fehlgeschlagen.
	LinkErrBackfillWaiting = "Rückwirkend, wartet auf Übernahme"
)

// ErrNotFound meldet, dass ein Datensatz nicht existiert.
var ErrNotFound = errors.New("abgleich: nicht gefunden")

// Profile ordnet eine Fremdtabelle einem Ticket- oder Störmeldungsbestand zu.
type Profile struct {
	ID, Name, ConnectionID string
	DepartmentID           *string
	TargetType             string
	SourceSchema           string
	SourceTable            string
	KeyColumns             []string
	ChangeColumn           string
	ChangeKind             string // none | timestamp | rowversion
	// CreatedColumn: Anlagedatum-Spalte der Fremdtabelle (Datum oder Zeit, leer =
	// keine). Neue Zeilen mit Anlagedatum vor NotifyFrom sind rückwirkend (Spec
	// 4.10, R24). Kein zugeordnetes Feld, nie Teil von seen_values.
	CreatedColumn          string
	FilterSQL              string
	WindowDays             int
	Defaults               map[string]string
	CommentTable           *CommentTable
	IntervalMinutes        int
	MaxNew                 int
	State, StateNote       string
	TestDryAt, TestWriteAt *time.Time
	// NotifyFrom ist der feste Stichtag für "rückwirkend" (R24): Tagesbeginn am
	// Ort des Servers des Tages, an dem das Profil ZUM ERSTEN MAL scharf wurde.
	// Gesetzt allein durch Store.Arm, genau einmal (nil -> Tagesbeginn). Weder
	// ein späteres Zurücksetzen auf Entwurf noch erneutes Scharfschalten noch
	// eine andere Anlagedatum-Spalte verschiebt ihn: alles seit dem ersten
	// Scharfschalten bleibt "neu" und benachrichtigt. nil = nie scharf gewesen.
	NotifyFrom *time.Time
	// TimeZone: Zeitzone der Datums-/Zeitwerte im Fremdsystem (Spec 4.11): '' =
	// Ortszeit des PDH-Servers, 'UTC' oder ein IANA-Name. Gilt für das
	// Anlagedatum und zugeordnete Zeitfelder, nicht für reine Datumsspalten und
	// nie für Änderungsspalte, Cursor oder Änderungsmarke (siehe ForeignLocation).
	// Eine Änderung setzt das Profil auf Entwurf und lässt jede Zeile einmal neu
	// vergleichen (seen_mark der Verknüpfungen wird geleert).
	TimeZone  string
	NextRunAt *time.Time
	Cursor    string
	FailCount int
}

// CommentTable beschreibt die Kommentartabelle im Fremdsystem.
type CommentTable struct {
	Schema, Table, FKColumn, TextColumn, AuthorColumn, TimeColumn string
}

// FieldMap ordnet eine Fremdspalte einem PDH-Feld zu.
type FieldMap struct {
	ID, ProfileID, ForeignColumn, PDHField, Direction string
	Translate                                         map[string]string
	FixedValue                                        *string
	Position                                          int
}

// Link verbindet eine Fremdzeile mit einem PDH-Datensatz. RefID nil = reserviert.
type Link struct {
	ID, ProfileID, ForeignKey, RefType string
	RefID                              *string
	SeenMark                           string
	// SeenValues: zuletzt gesehene Fremdwerte (PDH-Form) aller hereinkommenden Felder.
	// WrittenValues: Ausgangswert im PDH je Feld – der PDH-Wert, den der Abgleich
	// selbst festgelegt hat, wo er vom Fremdwert abweicht (Vorgabe ergänzt, leerer
	// Fremdwert übergangen, Umsetzung wie closed→resolved, Lösungstext von
	// QuickResolve) bzw. den das Hinausschreiben zuletzt geschrieben hat. Ein
	// fehlender Schlüssel heißt "kein Ausgangswert"; ist der PDH-Wert gleich dem
	// Fremdwert, wird der Eintrag entfernt. Decide prüft damit "p == w": PDH ist
	// seitdem nicht von einem Benutzer geändert worden.
	SeenValues, WrittenValues map[string]string
	LastError                 string
}

// OutboxItem ist ein Eintrag der Warteliste (PDH -> Fremdsystem).
type OutboxItem struct {
	ID        int64
	LinkID    string
	Kind      string
	Payload   json.RawMessage
	Attempts  int
	LastError string
}

// Run ist ein Protokolleintrag eines Laufs.
type Run struct {
	ID, ProfileID, Direction string
	StartedAt                time.Time
	FinishedAt               *time.Time
	// New: neu angelegte Datensätze mit Benachrichtigung (Anlagedatum ab dem
	// Stichtag oder unbekannt). Backfilled: still angelegte rückwirkende
	// Datensätze (nicht in New). Archived: davon gleich erledigt und archiviert.
	// Alle drei stehen in sync_runs (n_new, n_backfill, n_archived).
	// CountTruncated (nur Probelauf, nur im Speicher): die Zähler sind nicht
	// vollständig (Zeilengrenze dryCountCap, Kontext beendet oder Überlastung mit
	// RunOptions.Limit) – die Oberfläche zeigt "mindestens".
	New, Changed, Written int
	Backfilled, Archived  int
	CountTruncated        bool
	Failed                int
	SkippedReason         string
	Errors                []RunError
	// Cursor ist die Fortschrittsmarke nach einem hereinkommenden Lauf; der
	// Aufrufer speichert sie (SetSchedule), sync_runs kennt sie nicht. Bei
	// Änderungserkennung "none" immer leer.
	Cursor string
}

// RunError beschreibt einen einzelnen Fehler in einem Lauf.
type RunError struct {
	ForeignKey, Field, Message string
}

// PDHField beschreibt ein Feld, das mit dem Fremdsystem abgeglichen werden kann.
type PDHField struct {
	Key, Label string
	Required   bool
	MaxLen     int    // 0 = unbegrenzt
	Kind       string // text | enum | time
	Values     []string
}

var (
	priorityValues     = []string{"low", "medium", "high", "critical"}
	ticketStatusValues = []string{"open", "in_progress", "pending", "resolved", "closed"}
	faultStatusValues  = []string{"detected", "analyzing", "in_progress", "pending", "resolved", "closed"}
)

// Fields liefert den Feldkatalog für "ticket" oder "fault"; sonst nil.
// Die Spalten und Aufzählungswerte entsprechen den Tabellen tickets/faults
// (Migrationen 001, 002, 034, 044, 098).
func Fields(targetType string) []PDHField {
	var level PDHField
	var status PDHField
	switch targetType {
	case RefTicket:
		level = PDHField{Key: "priority", Label: "Priorität", Kind: "enum", Values: priorityValues}
		status = PDHField{Key: "status", Label: "Status", Kind: "enum", Values: ticketStatusValues}
	case RefFault:
		level = PDHField{Key: "severity", Label: "Schweregrad", Kind: "enum", Values: priorityValues}
		status = PDHField{Key: "status", Label: "Status", Kind: "enum", Values: faultStatusValues}
	default:
		return nil
	}
	return []PDHField{
		{Key: "title", Label: "Titel", Required: true, MaxLen: 255, Kind: "text"},
		{Key: "description", Label: "Beschreibung", Kind: "text"},
		level,
		status,
		{Key: "due_date", Label: "Fällig am", Kind: "time"},
		{Key: "resolution", Label: "Lösung", Kind: "text"},
		{Key: "root_cause", Label: "Ursache", Kind: "text"},
	}
}

// EncodeKey bildet die Schlüsselwerte einer Fremdzeile eindeutig als JSON-Feld ab,
// auch bei mehreren Spalten und Sonderzeichen: ["17"], ["a b","x|y"].
func EncodeKey(values []string) string {
	if values == nil {
		values = []string{}
	}
	b, _ := json.Marshal(values)
	return string(b)
}

// DecodeKey kehrt EncodeKey um und lehnt alles ab, was kein JSON-Feld aus Texten ist.
func DecodeKey(key string) ([]string, error) {
	var raw []*string
	if err := json.Unmarshal([]byte(key), &raw); err != nil {
		return nil, fmt.Errorf("abgleich: schlüssel ungültig: %w", err)
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		if v == nil {
			return nil, errors.New("abgleich: schlüssel ungültig: null-Wert")
		}
		out[i] = *v
	}
	return out, nil
}
