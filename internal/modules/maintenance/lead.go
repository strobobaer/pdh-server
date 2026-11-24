package maintenance

import (
	"context"
	"time"

	"pdh/internal/core/workdays"
	"pdh/pkg/appsettings"
)

// Vorlauf des Durchführungsauftrags in Arbeitstagen (Wartungsregeln):
// Vorgabe in app_settings, je Plan überschreibbar.

// DefaultLead: Vorgabe der Wartungsregeln (5, außerhalb 0–60 → 5).
func (r *Repository) DefaultLead(ctx context.Context) int {
	n := appsettings.GetInt(ctx, r.db, appsettings.KeyMaintenanceLeadWorkdays, appsettings.DefaultMaintenanceLeadWorkdays)
	if n < 0 || n > 60 {
		return appsettings.DefaultMaintenanceLeadWorkdays
	}
	return n
}

// EffectiveLead: Vorlauf des Plans, ohne eigenen Wert die Vorgabe.
func (r *Repository) EffectiveLead(ctx context.Context, planLead *int) int {
	if planLead != nil {
		return *planLead
	}
	return r.DefaultLead(ctx)
}

// OrderDay: Tag, an dem der Auftrag für einen Termin am Tag day entsteht.
func (r *Repository) OrderDay(ctx context.Context, day time.Time, planLead *int) time.Time {
	return workdays.LeadDay(ctx, r.db, day, r.EffectiveLead(ctx, planLead))
}

// civil: Kalendertag einer DATE-Spalte (pgx: Mitternacht UTC) als 0 Uhr Ortszeit.
func civil(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
}
