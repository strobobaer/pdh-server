package database

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReadOnlyTracerMarks(t *testing.T) {
	tr := NewReadOnlyTracer()

	ctx, mark := ReadOnlyMark(context.Background())
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505"}})
	if mark.Load() {
		t.Fatal("anderer SQLSTATE darf nicht markieren")
	}
	qctx := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "UPDATE users SET last_seen_at = NOW()"})
	tr.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "25006"}})
	if !mark.Load() {
		t.Fatal("25006 muss die Anfrage markieren")
	}
	if ReadOnlyMarkOf(ctx) != mark {
		t.Fatal("ReadOnlyMarkOf liefert nicht die Marke aus dem Kontext")
	}

	// ohne Marke: keine Panik
	tr.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "25006"}})
	if ReadOnlyMarkOf(context.Background()) != nil {
		t.Fatal("ohne Marke muss nil kommen")
	}
}

func TestReadOnlyTracerBatchAndCopy(t *testing.T) {
	tr := NewReadOnlyTracer()
	bt, ok := tr.(pgx.BatchTracer)
	if !ok {
		t.Fatal("Tracer muss auch Batches abdecken")
	}
	ctx, mark := ReadOnlyMark(context.Background())
	bt.TraceBatchQuery(ctx, nil, pgx.TraceBatchQueryData{SQL: "INSERT INTO x VALUES (1)", Err: &pgconn.PgError{Code: "25006"}})
	if !mark.Load() {
		t.Fatal("Batch mit 25006 muss markieren")
	}
	ct, ok := tr.(pgx.CopyFromTracer)
	if !ok {
		t.Fatal("Tracer muss auch CopyFrom abdecken")
	}
	ctx, mark = ReadOnlyMark(context.Background())
	ct.TraceCopyFromEnd(ctx, nil, pgx.TraceCopyFromEndData{Err: &pgconn.PgError{Code: "25006"}})
	if !mark.Load() {
		t.Fatal("CopyFrom mit 25006 muss markieren")
	}
}

// writeSiteCaller steht für einen Handler, der schreibt.
func writeSiteCaller() string { return writeSite() }

func TestWriteSiteNamesCaller(t *testing.T) {
	s := writeSiteCaller()
	if !strings.HasPrefix(s, "database.writeSiteCaller readonly_tracer_test.go:") || !strings.Contains(s, " < database.TestWriteSiteNamesCaller ") {
		t.Fatalf("Aufrufstelle: %q", s)
	}
}
