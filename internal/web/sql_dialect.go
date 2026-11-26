package web

import (
	"fmt"
	"strings"
)

// SQL-Dialekte der Datenbank-Vorschau (sql_preview.go): SQL Server kennt kein LIMIT, sondern
// TOP, und Tabellen außerhalb von dbo heißen „Schema.Tabelle“ (0.82.69).

// sqlSchemaAndName trennt „erp.Artikel“ in Schema und Tabelle; ohne Punkt ist das Schema leer.
func sqlSchemaAndName(table string) (schema, name string) {
	if s, n, ok := strings.Cut(table, "."); ok && s != "" && n != "" {
		return s, n
	}
	return "", table
}

// quoteSQLTable setzt einen Tabellennamen in Anführung; bei SQL Server mit Schema („[erp].[Artikel]“).
func quoteSQLTable(kind, table string) string {
	if kind == "mssql" {
		if s, n := sqlSchemaAndName(table); s != "" {
			return quoteIdent(kind, s) + "." + quoteIdent(kind, n)
		}
	}
	return quoteIdent(kind, table)
}

// sqlPreviewQuery: die ersten n Zeilen einer Tabelle oder Sicht.
func sqlPreviewQuery(kind, table string, n int) string {
	if kind == "mssql" {
		return fmt.Sprintf("SELECT TOP %d * FROM %s", n, quoteSQLTable(kind, table))
	}
	return fmt.Sprintf("SELECT * FROM %s LIMIT %d", quoteSQLTable(kind, table), n)
}

// sqlLastRowQuery: die zuletzt eingefügte Zeile (absteigend nach orderExpr) mit den genannten Spalten.
func sqlLastRowQuery(kind, table string, columns []string, orderExpr string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = quoteIdent(kind, c)
	}
	cols, from := strings.Join(quoted, ", "), quoteSQLTable(kind, table)
	if kind == "mssql" {
		return fmt.Sprintf("SELECT TOP 1 %s FROM %s ORDER BY %s DESC", cols, from, orderExpr)
	}
	return fmt.Sprintf("SELECT %s FROM %s ORDER BY %s DESC LIMIT 1", cols, from, orderExpr)
}

// sqlKnownTables: die Tabellen der Datenbank als Menge; bei SQL Server zählen dbo-Tabellen auch
// ohne Schema (so hießen sie in Zuordnungen vor 0.82.69).
func sqlKnownTables(kind string, tables []string) map[string]bool {
	out := make(map[string]bool, len(tables))
	for _, t := range tables {
		out[t] = true
		if kind == "mssql" {
			if s, n := sqlSchemaAndName(t); strings.EqualFold(s, "dbo") {
				out[n] = true
			}
		}
	}
	return out
}

// splitSQLSourceRef zerlegt die Quelle „Tabelle.Spalte“ einer Zuordnung. Bei SQL Server ist die
// Tabelle der längste Anfang, den es in der Datenbank gibt (Schema und Punkte im Namen); ist er
// unbekannt, gilt wie bei den anderen Datenbanken der erste Punkt.
func splitSQLSourceRef(kind, ref string, known map[string]bool) (table, column string, ok bool) {
	if kind == "mssql" && len(known) > 0 {
		for i := strings.LastIndex(ref, "."); i > 0; i = strings.LastIndex(ref[:i], ".") {
			if known[ref[:i]] && i+1 < len(ref) {
				return ref[:i], ref[i+1:], true
			}
		}
	}
	table, column, ok = strings.Cut(ref, ".")
	return table, column, ok && table != "" && column != ""
}

// withHint hängt einen Hinweis an eine Fehlermeldung (ohne Hinweis unverändert).
func withHint(msg, hint string) string {
	if hint == "" {
		return msg
	}
	return msg + " – " + hint
}

// connErrorHint: verständlicher Hinweis zu bekannten Anmeldefehlern (leer, wenn keiner passt).
// SQL Server Fehler 18452 – Windows-Anmeldung, die die Domäne nicht bestätigt.
func connErrorHint(kind string, config map[string]string, err error) string {
	if kind != "mssql" || err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "nicht vertrauenswürdigen domäne") && !strings.Contains(msg, "untrusted domain") && !strings.Contains(msg, "18452") {
		return ""
	}
	if strings.Contains(config["username"], `\`) {
		return "Windows-Anmeldung abgelehnt. Prüfen: 1. vor dem „\\“ den kurzen Domänennamen eintragen (z. B. FIRMA\\name, nicht firma.local\\name); " +
			"2. Kennwort richtig, nicht abgelaufen, Konto nicht gesperrt; 3. die Domäne lässt die Anmeldung von diesem Server zu (IT: Ereignis 4625 am Domänencontroller). " +
			"Sicherer Weg: einen eigenen SQL-Benutzer nur für das PDH anlegen lassen und ohne Domäne eintragen."
	}
	return "Möglicherweise lässt der SQL Server nur Windows-Anmeldungen zu, oder Benutzer bzw. Kennwort stimmen nicht. Gilt das Erste, stellt die IT in SQL Server Management Studio unter Server › Eigenschaften › Sicherheit " +
		"„SQL Server- und Windows-Authentifizierungsmodus“ ein und startet den Dienst neu – oder es wird ein Windows-Konto im Format DOMÄNE\\name verwendet."
}
