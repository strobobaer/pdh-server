// Package stillstand verwaltet Stillstandsgründe (Katalog, Zuordnung zu Anlagen mit
// Vererbung nach unten) und die Stillstände selbst (Beginn, Ende, Grund) samt Auswertung
// je Grund. Aufbau wie bei den Rüsttasten (Paket ruesten).
package stillstand

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Herkunft eines Stillstands.
const (
	OriginTerminal = "terminal"
	OriginManual   = "manual"
)

var (
	ErrNotFound         = errors.New("stillstandsgrund nicht gefunden")
	ErrReasonInUse      = errors.New("der Grund wird noch verwendet")
	ErrAlreadyRunning   = errors.New("an dieser Anlage läuft bereits ein Stillstand")
	ErrNotRunning       = errors.New("an dieser Anlage läuft kein Stillstand")
	ErrReasonNotAllowed = errors.New("der Grund gilt an dieser Anlage nicht")
)

// Reason ist ein Stillstandsgrund aus dem Katalog.
type Reason struct {
	ID, Key, Name string
	Sort          int
	Active        bool
	InheritedFrom string // nur InfraReasons: Name der übergeordneten Anlage ("" = direkt an der Anlage)
}

// Downtime ist ein Stillstand.
type Downtime struct {
	ID, InfraID, ReasonID, ReasonName string
	StartedAt                         time.Time
	EndedAt                           *time.Time
	ActorName                         string
}

// Store liest und schreibt über DB.
type Store struct{ DB *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{DB: db} }

// ── Katalog ──────────────────────────────────────────────────────────────

const reasonSelect = `SELECT id::text, key, name, sort, active FROM downtime_reasons`

func scanReasons(rows pgx.Rows) ([]Reason, error) {
	defer rows.Close()
	var out []Reason
	for rows.Next() {
		var r Reason
		if err := rows.Scan(&r.ID, &r.Key, &r.Name, &r.Sort, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reasons liefert den ganzen Katalog in Reihenfolge.
func (s *Store) Reasons(ctx context.Context) ([]Reason, error) {
	rows, err := s.DB.Query(ctx, reasonSelect+` ORDER BY sort, lower(name), id`)
	if err != nil {
		return nil, err
	}
	return scanReasons(rows)
}

// SaveReason legt einen Grund an (ID leer, ans Ende; Key wird gesetzt) oder ändert Name und Status.
func (s *Store) SaveReason(ctx context.Context, r *Reason) error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len([]rune(r.Name)) > 80 {
		return errors.New("der Name des Grundes fehlt oder ist länger als 80 Zeichen")
	}
	if r.ID != "" {
		tag, err := s.DB.Exec(ctx, `UPDATE downtime_reasons SET name = $2, active = $3, updated_at = now() WHERE id = $1`, r.ID, r.Name, r.Active)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	}
	key, err := s.freeKey(ctx, Key(r.Name))
	if err != nil {
		return err
	}
	return s.DB.QueryRow(ctx, `INSERT INTO downtime_reasons (key, name, sort, active)
		VALUES ($1, $2, (SELECT COALESCE(max(sort), 0) + 10 FROM downtime_reasons), $3) RETURNING id::text, key, sort`,
		key, r.Name, r.Active).Scan(&r.ID, &r.Key, &r.Sort)
}

// Key bildet aus dem Namen einen Schlüssel (a–z, 0–9, Unterstrich).
func Key(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == 'ä':
			b.WriteString("ae")
		case c == 'ö':
			b.WriteString("oe")
		case c == 'ü':
			b.WriteString("ue")
		case c == 'ß':
			b.WriteString("ss")
		default:
			if s := b.String(); s != "" && !strings.HasSuffix(s, "_") {
				b.WriteByte('_')
			}
		}
	}
	if k := strings.Trim(b.String(), "_"); k != "" {
		return k
	}
	return "grund"
}

func (s *Store) freeKey(ctx context.Context, key string) (string, error) {
	k := key
	for n := 2; ; n++ {
		var used bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM downtime_reasons WHERE key = $1)`, k).Scan(&used); err != nil {
			return "", err
		}
		if !used {
			return k, nil
		}
		k = key + "_" + itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// MoveReason verschiebt einen Grund im Katalog um eine Stelle.
func (s *Store) MoveReason(ctx context.Context, id string, up bool) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM downtime_reasons ORDER BY sort, lower(name), id FOR UPDATE`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	pos := -1
	for i, x := range ids {
		if x == id {
			pos = i
		}
	}
	if pos < 0 {
		return ErrNotFound
	}
	other := pos + 1
	if up {
		other = pos - 1
	}
	if other >= 0 && other < len(ids) {
		ids[pos], ids[other] = ids[other], ids[pos]
	}
	for i, x := range ids {
		if _, err := tx.Exec(ctx, `UPDATE downtime_reasons SET sort = $2 WHERE id = $1 AND sort <> $2`, x, (i+1)*10); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteReason löscht einen Grund samt Zuordnungen; ein Grund, an dem Stillstände hängen, bleibt (ErrReasonInUse).
func (s *Store) DeleteReason(ctx context.Context, id string) error {
	var used bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM downtimes WHERE reason_id = $1)`, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrReasonInUse
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM downtime_reasons WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Uses: an wie vielen Anlagen jeder Grund direkt zugeordnet ist.
func (s *Store) Uses(ctx context.Context) (map[string]int, error) {
	rows, err := s.DB.Query(ctx, `SELECT reason_id::text, count(*) FROM infra_downtime_reasons GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ── Zuordnung zu Anlagen (mit Vererbung nach unten) ──────────────────────

// InfraReasons liefert die aktiven Gründe einer Anlage: direkt zugeordnete und von
// übergeordneten Anlagen geerbte (InheritedFrom = Name der nächsten übergeordneten Anlage).
func (s *Store) InfraReasons(ctx context.Context, infraID string) ([]Reason, error) {
	rows, err := s.DB.Query(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, name, 0 AS d FROM infrastructure WHERE id = $1::uuid
			UNION ALL SELECT i.id, i.parent_id, i.name, up.d + 1 FROM infrastructure i JOIN up ON i.id = up.parent_id WHERE up.d < 50)
		SELECT r.id::text, r.key, r.name, r.sort, r.active, CASE WHEN a.d = 0 THEN '' ELSE a.name END
		  FROM downtime_reasons r
		  JOIN LATERAL (SELECT up.name, up.d FROM infra_downtime_reasons x JOIN up ON up.id = x.infrastructure_id
		                 WHERE x.reason_id = r.id ORDER BY up.d LIMIT 1) a ON true
		 WHERE r.active
		 ORDER BY r.sort, lower(r.name), r.id`, infraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reason
	for rows.Next() {
		var r Reason
		if err := rows.Scan(&r.ID, &r.Key, &r.Name, &r.Sort, &r.Active, &r.InheritedFrom); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OwnReasons: die direkt an der Anlage zugeordneten Gründe (IDs).
func (s *Store) OwnReasons(ctx context.Context, infraID string) (map[string]bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT reason_id::text FROM infra_downtime_reasons WHERE infrastructure_id = $1::uuid`, infraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SetInfraReasons ersetzt die direkt zugeordneten Gründe einer Anlage.
func (s *Store) SetInfraReasons(ctx context.Context, infraID string, reasonIDs []string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM infra_downtime_reasons WHERE infrastructure_id = $1`, infraID); err != nil {
		return err
	}
	for _, id := range reasonIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO infra_downtime_reasons (infrastructure_id, reason_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, infraID, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ── Stillstände ──────────────────────────────────────────────────────────

// Running liefert den laufenden Stillstand der Anlage (nil = keiner).
func (s *Store) Running(ctx context.Context, infraID string) (*Downtime, error) {
	var d Downtime
	err := s.DB.QueryRow(ctx, `SELECT id::text, infrastructure_id::text, COALESCE(reason_id::text, ''), reason_name, started_at, actor_name
		  FROM downtimes WHERE infrastructure_id = $1::uuid AND ended_at IS NULL AND archived_at IS NULL`, infraID).
		Scan(&d.ID, &d.InfraID, &d.ReasonID, &d.ReasonName, &d.StartedAt, &d.ActorName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Start beginnt einen Stillstand mit dem Grund (muss an der Anlage gelten, auch geerbt).
// actorID ist die handelnde Person (Karte) oder der Terminal-Benutzer, actorName das freiwillige Kürzel.
func (s *Store) Start(ctx context.Context, infraID, reasonID, actorID, actorName, origin string) (*Downtime, error) {
	reasons, err := s.InfraReasons(ctx, infraID)
	if err != nil {
		return nil, err
	}
	var name string
	ok := false
	for _, r := range reasons {
		if r.ID == reasonID {
			name, ok = r.Name, true
		}
	}
	if !ok {
		return nil, ErrReasonNotAllowed
	}
	if origin != OriginManual {
		origin = OriginTerminal
	}
	if rn := []rune(actorName); len(rn) > 40 {
		actorName = string(rn[:40])
	}
	d := &Downtime{InfraID: infraID, ReasonID: reasonID, ReasonName: name, ActorName: actorName}
	err = s.DB.QueryRow(ctx, `INSERT INTO downtimes (infrastructure_id, reason_id, reason_name, started_at, origin, actor_name, created_by)
		VALUES ($1::uuid, $2::uuid, $3, now(), $4, $5, NULLIF($6, '')::uuid) RETURNING id::text, started_at`,
		infraID, reasonID, name, origin, actorName, actorID).Scan(&d.ID, &d.StartedAt)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Finish beendet den laufenden Stillstand der Anlage.
func (s *Store) Finish(ctx context.Context, infraID, actorID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE downtimes SET ended_at = GREATEST(now(), started_at + interval '1 second'), ended_by = NULLIF($2, '')::uuid
		 WHERE infrastructure_id = $1::uuid AND ended_at IS NULL AND archived_at IS NULL`, infraID, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotRunning
	}
	return nil
}

// ── Auswertung ───────────────────────────────────────────────────────────

// ReasonStat: Stillstand je Grund (Anzahl und Gesamtdauer; laufende zählen bis jetzt).
type ReasonStat struct {
	Name    string
	Count   int
	Minutes float64
}

// StatsByReason wertet die Stillstände der Anlagen ids, die seit since begonnen haben, je Grund aus
// (absteigend nach Dauer). Ohne Grund (ältere Datensätze, gelöschter Grund): Name aus reason_name,
// sonst „ohne Grund“.
func (s *Store) StatsByReason(ctx context.Context, ids []string, since time.Time) ([]ReasonStat, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT COALESCE(NULLIF(r.name, ''), NULLIF(t.reason_name, ''), 'ohne Grund') AS n,
		       COUNT(*)::int, COALESCE(SUM(EXTRACT(EPOCH FROM (COALESCE(t.ended_at, now()) - t.started_at)) / 60), 0)::float8
		  FROM downtimes t LEFT JOIN downtime_reasons r ON r.id = t.reason_id
		 WHERE t.infrastructure_id = ANY($1::uuid[]) AND t.archived_at IS NULL AND t.started_at >= $2
		 GROUP BY 1 ORDER BY 3 DESC, 1`, ids, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReasonStat
	for rows.Next() {
		var x ReasonStat
		if err := rows.Scan(&x.Name, &x.Count, &x.Minutes); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RunningCount: laufende Stillstände der Anlagen.
func (s *Store) RunningCount(ctx context.Context, ids []string) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM downtimes WHERE infrastructure_id = ANY($1::uuid[]) AND ended_at IS NULL AND archived_at IS NULL`, ids).Scan(&n)
	return n, err
}
