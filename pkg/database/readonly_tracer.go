package database

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
)

// Spiegelmodus: Schreibzugriffe am PostgreSQL-Hot-Standby scheitern mit
// SQLSTATE 25006 (read_only_sql_transaction). Der Tracer am Pool markiert dann
// die Anfrage; die Web-Middleware ersetzt deren 5xx-Antwort durch die
// Sperrmeldung (internal/web/mirror.go).

type readOnlyMarkKey struct{}
type readOnlySQLKey struct{}

// ReadOnlyMark hängt eine Marke an den Kontext; der Tracer setzt sie bei 25006.
func ReadOnlyMark(ctx context.Context) (context.Context, *atomic.Bool) {
	m := new(atomic.Bool)
	return context.WithValue(ctx, readOnlyMarkKey{}, m), m
}

// ReadOnlyMarkOf liefert die Marke aus dem Kontext (nil, wenn keine gesetzt ist).
func ReadOnlyMarkOf(ctx context.Context) *atomic.Bool {
	m, _ := ctx.Value(readOnlyMarkKey{}).(*atomic.Bool)
	return m
}

// readOnlyTracer implementiert pgx.QueryTracer, BatchTracer und CopyFromTracer.
type readOnlyTracer struct{}

// NewReadOnlyTracer für poolCfg.ConnConfig.Tracer.
func NewReadOnlyTracer() pgx.QueryTracer { return readOnlyTracer{} }

func (readOnlyTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, readOnlySQLKey{}, data.SQL)
}

func (readOnlyTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(readOnlySQLKey{}).(string)
	readOnlyCheck(ctx, data.Err, sql)
}

func (readOnlyTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

func (readOnlyTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	readOnlyCheck(ctx, data.Err, data.SQL)
}

// TraceBatchEnd: der Fehler wurde schon je Abfrage gemeldet – nur noch markieren.
func (readOnlyTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	if m := ReadOnlyMarkOf(ctx); m != nil && isReadOnlyErr(data.Err) {
		m.Store(true)
	}
}

func (readOnlyTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromStartData) context.Context {
	return context.WithValue(ctx, readOnlySQLKey{}, "COPY "+data.TableName.Sanitize())
}

func (readOnlyTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	sql, _ := ctx.Value(readOnlySQLKey{}).(string)
	readOnlyCheck(ctx, data.Err, sql)
}

// readOnlyCheck: bei 25006 Marke setzen und den SQL-Anfang protokollieren.
func readOnlyCheck(ctx context.Context, err error, sql string) {
	if !isReadOnlyErr(err) {
		return
	}
	m := ReadOnlyMarkOf(ctx)
	if m != nil {
		m.Store(true)
	}
	log.Warn().Str("bereich", "datenbank").Str("sql", sqlHead(sql)).Bool("anfrage", m != nil).Msg("schreibzugriff im spiegel")
}

func isReadOnlyErr(err error) bool {
	var pgErr *pgconn.PgError
	return err != nil && errors.As(err, &pgErr) && pgErr.Code == "25006"
}

// sqlHead: erste 80 Zeichen, Leerraum zusammengezogen.
func sqlHead(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}
