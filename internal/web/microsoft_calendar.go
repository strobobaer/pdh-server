package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type microsoftCalendarPreferences struct {
	Shifts        bool
	Tasks         bool
	Maintenance   bool
	TicketsFaults bool
	ImportBusy    bool
}

type microsoftCalendarSource struct {
	Type   string
	ID     string
	Title  string
	Start  time.Time
	End    time.Time
	AllDay bool
}

type microsoftCalendarView struct {
	ID    string `json:"id"`
	Start struct {
		DateTime string `json:"dateTime"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
	} `json:"end"`
	ShowAs      string `json:"showAs"`
	IsAllDay    bool   `json:"isAllDay"`
	IsCancelled bool   `json:"isCancelled"`
}

type microsoftCalendarPage struct {
	Events   []microsoftCalendarView `json:"value"`
	NextLink string                  `json:"@odata.nextLink"`
}

type microsoftGraphEvent struct {
	ID string `json:"id"`
}

type microsoftGraphStatusError struct {
	StatusCode int
}

func (e microsoftGraphStatusError) Error() string {
	return fmt.Sprintf("Microsoft Graph returned HTTP %d", e.StatusCode)
}

func (h *Handler) SaveMicrosoftCalendarPreferences(w http.ResponseWriter, r *http.Request) {
	kind := microsoftAccountKind(r)
	var connected bool
	if err := h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM microsoft_user_connections WHERE user_id=$1::uuid AND account_kind=$2)`, getUser(r).ID, kind).Scan(&connected); err != nil || !connected {
		http.Redirect(w, r, "/account?notice=Zuerst+ein+Microsoft-Konto+verbinden", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Kalendereinstellungen konnten nicht gelesen werden", http.StatusBadRequest)
		return
	}
	_, err := h.db.Exec(r.Context(), `
		INSERT INTO microsoft_calendar_preferences
			(user_id, account_kind, sync_shifts, sync_tasks, sync_maintenance, sync_tickets_faults, import_busy_events, updated_at)
		VALUES ($1::uuid, $7, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (user_id, account_kind) DO UPDATE SET
			sync_shifts=EXCLUDED.sync_shifts,
			sync_tasks=EXCLUDED.sync_tasks,
			sync_maintenance=EXCLUDED.sync_maintenance,
			sync_tickets_faults=EXCLUDED.sync_tickets_faults,
			import_busy_events=EXCLUDED.import_busy_events,
			updated_at=NOW()`, getUser(r).ID,
		r.FormValue("sync_shifts") == "on",
		r.FormValue("sync_tasks") == "on",
		r.FormValue("sync_maintenance") == "on",
		r.FormValue("sync_tickets_faults") == "on",
		r.FormValue("import_busy_events") == "on", kind)
	if err != nil {
		http.Error(w, "Kalendereinstellungen konnten nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account?notice=Kalendereinstellungen+gespeichert", http.StatusSeeOther)
}

func (h *Handler) SyncMicrosoftCalendarWeb(w http.ResponseWriter, r *http.Request) {
	userID, kind := getUser(r).ID, microsoftAccountKind(r)
	if !h.microsoftSyncMu.TryLock() {
		http.Redirect(w, r, "/account?notice=Microsoft-Synchronisierung+läuft+bereits", http.StatusSeeOther)
		return
	}
	defer h.microsoftSyncMu.Unlock()
	preferences, err := h.loadMicrosoftCalendarPreferences(r.Context(), userID, kind)
	if err != nil {
		http.Redirect(w, r, "/account?notice=Microsoft-Kalendereinstellungen+konnten+nicht+geladen+werden", http.StatusSeeOther)
		return
	}
	if !preferences.Shifts && !preferences.Tasks && !preferences.Maintenance && !preferences.TicketsFaults && !preferences.ImportBusy {
		http.Redirect(w, r, "/account?notice=Zuerst+mindestens+einen+Kalenderbereich+auswählen", http.StatusSeeOther)
		return
	}
	accessToken, err := h.microsoftAccessToken(r.Context(), userID, kind)
	if err != nil {
		http.Redirect(w, r, "/account?notice=Microsoft-Anmeldung+erneuern", http.StatusSeeOther)
		return
	}
	created, imported, err := h.syncMicrosoftCalendar(r.Context(), userID, kind, accessToken, preferences)
	if err != nil {
		http.Redirect(w, r, "/account?notice="+url.QueryEscape("Kalendersync fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	_, _ = h.db.Exec(r.Context(), `UPDATE microsoft_calendar_preferences SET last_sync_at=NOW(), updated_at=NOW() WHERE user_id=$1::uuid AND account_kind=$2`, userID, kind)
	notice := fmt.Sprintf("Outlook synchronisiert: %d PDH-Termine aktualisiert, %d Busy-Blocker importiert", created, imported)
	http.Redirect(w, r, "/account?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func (h *Handler) loadMicrosoftCalendarPreferences(ctx context.Context, userID, kind string) (microsoftCalendarPreferences, error) {
	var preferences microsoftCalendarPreferences
	err := h.db.QueryRow(ctx, `
		SELECT sync_shifts, sync_tasks, sync_maintenance, sync_tickets_faults, import_busy_events
		FROM microsoft_calendar_preferences WHERE user_id=$1::uuid AND account_kind=$2`, userID, kind).Scan(
		&preferences.Shifts, &preferences.Tasks, &preferences.Maintenance, &preferences.TicketsFaults, &preferences.ImportBusy)
	if errors.Is(err, pgx.ErrNoRows) {
		return preferences, nil
	}
	return preferences, err
}

func (h *Handler) microsoftAccessToken(ctx context.Context, userID, kind string) (string, error) {
	var encryptedAccess, encryptedRefresh string
	var grantedScopes string
	var expiresAt time.Time
	if err := h.db.QueryRow(ctx, `
		SELECT access_token, refresh_token, granted_scopes, expires_at FROM microsoft_user_connections
		WHERE user_id=$1::uuid AND account_kind=$2`, userID, kind).Scan(&encryptedAccess, &encryptedRefresh, &grantedScopes, &expiresAt); err != nil {
		return "", err
	}
	if expiresAt.After(time.Now().Add(2 * time.Minute)) {
		return h.decryptMicrosoftToken(encryptedAccess)
	}
	refreshToken, err := h.decryptMicrosoftToken(encryptedRefresh)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(grantedScopes) == "" {
		grantedScopes = microsoftScopes("calendar")
	}
	form := url.Values{
		"client_id":     {h.microsoft.ClientID},
		"client_secret": {h.microsoft.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {grantedScopes},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, microsoftAuthority(kind)+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", microsoftGraphStatusError{StatusCode: response.StatusCode}
	}
	var tokens microsoftTokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return "", err
	}
	if tokens.AccessToken == "" {
		return "", fmt.Errorf("Microsoft returned no access token")
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	if tokens.Scope != "" {
		grantedScopes = tokens.Scope
	}
	encryptedAccess, err = h.encryptMicrosoftToken(tokens.AccessToken)
	if err != nil {
		return "", err
	}
	encryptedRefresh, err = h.encryptMicrosoftToken(tokens.RefreshToken)
	if err != nil {
		return "", err
	}
	expiresIn := tokens.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	_, err = h.db.Exec(ctx, `
		UPDATE microsoft_user_connections
		SET access_token=$1, refresh_token=$2, granted_scopes=$3, expires_at=$4, updated_at=NOW()
		WHERE user_id=$5::uuid AND account_kind=$6`, encryptedAccess, encryptedRefresh, grantedScopes, time.Now().Add(time.Duration(expiresIn)*time.Second), userID, kind)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func (h *Handler) syncMicrosoftCalendar(ctx context.Context, userID, kind, accessToken string, preferences microsoftCalendarPreferences) (int, int, error) {
	created, err := h.syncPDHEventsToMicrosoft(ctx, userID, kind, accessToken, preferences)
	if err != nil {
		return created, 0, err
	}
	imported := 0
	if preferences.ImportBusy {
		imported, err = h.importMicrosoftBusyEvents(ctx, userID, kind, accessToken)
	} else {
		_, err = h.db.Exec(ctx, `DELETE FROM microsoft_calendar_blocks WHERE user_id=$1::uuid AND account_kind=$2`, userID, kind)
	}
	return created, imported, err
}

func (h *Handler) syncPDHEventsToMicrosoft(ctx context.Context, userID, kind, accessToken string, preferences microsoftCalendarPreferences) (int, error) {
	sources, err := h.microsoftCalendarSources(ctx, userID, preferences)
	if err != nil {
		return 0, err
	}
	active := make(map[string]bool, len(sources))
	for _, source := range sources {
		key := source.Type + ":" + source.ID
		active[key] = true
		var eventID string
		err := h.db.QueryRow(ctx, `SELECT microsoft_event_id FROM microsoft_calendar_links WHERE user_id=$1::uuid AND account_kind=$4 AND source_type=$2 AND source_id=$3::uuid`, userID, source.Type, source.ID, kind).Scan(&eventID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		payload := map[string]interface{}{
			"subject":  source.Title,
			"body":     map[string]string{"contentType": "text", "content": "PDH synchronisierter Termin"},
			"start":    map[string]string{"dateTime": source.Start.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
			"end":      map[string]string{"dateTime": source.End.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
			"isAllDay": source.AllDay,
		}
		method := http.MethodPost
		endpoint := "https://graph.microsoft.com/v1.0/me/events"
		if eventID != "" {
			method = http.MethodPatch
			endpoint += "/" + url.PathEscape(eventID)
		}
		var event microsoftGraphEvent
		err = h.microsoftGraphRequest(ctx, accessToken, method, endpoint, payload, &event)
		if statusErr, ok := err.(microsoftGraphStatusError); ok && statusErr.StatusCode == http.StatusNotFound && eventID != "" {
			eventID = ""
			method = http.MethodPost
			endpoint = "https://graph.microsoft.com/v1.0/me/events"
			err = h.microsoftGraphRequest(ctx, accessToken, method, endpoint, payload, &event)
		}
		if err != nil {
			return 0, err
		}
		if event.ID == "" {
			event.ID = eventID
		}
		if event.ID == "" {
			return 0, fmt.Errorf("Microsoft Graph returned no event id")
		}
		_, err = h.db.Exec(ctx, `
			INSERT INTO microsoft_calendar_links (user_id, account_kind, source_type, source_id, microsoft_event_id, updated_at)
			VALUES ($1::uuid, $5, $2, $3::uuid, $4, NOW())
			ON CONFLICT (user_id, account_kind, source_type, source_id) DO UPDATE SET microsoft_event_id=EXCLUDED.microsoft_event_id, updated_at=NOW()`, userID, source.Type, source.ID, event.ID, kind)
		if err != nil {
			return 0, err
		}
	}
	deleted, err := h.removeStaleMicrosoftEvents(ctx, userID, kind, accessToken, preferences, active)
	if err != nil {
		return len(sources), err
	}
	return len(sources) - deleted, nil
}

func (h *Handler) microsoftCalendarSources(ctx context.Context, userID string, preferences microsoftCalendarPreferences) ([]microsoftCalendarSource, error) {
	sources := make([]microsoftCalendarSource, 0)
	if preferences.Shifts {
		rows, err := h.db.Query(ctx, `
			SELECT sa.id::text, sd.name, sa.date::text, sd.start_time::text, sd.end_time::text
			FROM shift_assignments sa JOIN shift_definitions sd ON sd.id=sa.shift_id
			WHERE sa.user_id=$1::uuid AND sa.date BETWEEN CURRENT_DATE-7 AND CURRENT_DATE+90`, userID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name, date, start, end string
			if err := rows.Scan(&id, &name, &date, &start, &end); err != nil {
				rows.Close()
				return nil, err
			}
			startAt, err := time.Parse("2006-01-02 15:04:05", date+" "+start)
			if err != nil {
				continue
			}
			endAt, err := time.Parse("2006-01-02 15:04:05", date+" "+end)
			if err != nil {
				continue
			}
			if !endAt.After(startAt) {
				endAt = endAt.Add(24 * time.Hour)
			}
			sources = append(sources, microsoftCalendarSource{Type: "shift", ID: id, Title: "PDH · Schicht · " + name, Start: startAt.UTC(), End: endAt.UTC()})
		}
		rows.Close()
	}
	if preferences.Tasks {
		rows, err := h.db.Query(ctx, `
			SELECT t.id::text, t.title, t.due_date::text FROM tasks t
			WHERE EXISTS(SELECT 1 FROM task_assignees ta WHERE ta.task_id = t.id AND ta.user_id=$1::uuid)
			  AND t.status IN ('open','in_progress','pending') AND t.due_date BETWEEN CURRENT_DATE-30 AND CURRENT_DATE+365`, userID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, title, due string
			if err := rows.Scan(&id, &title, &due); err != nil {
				rows.Close()
				return nil, err
			}
			if source, ok := allDayCalendarSource("task", id, title, due); ok {
				sources = append(sources, source)
			}
		}
		rows.Close()
	}
	if preferences.Maintenance {
		rows, err := h.db.Query(ctx, `
			SELECT id::text, title, due_date FROM maintenance_tasks
			WHERE assigned_to=$1::uuid AND status IN ('open','in_progress','pending') AND due_date BETWEEN NOW()-INTERVAL '30 days' AND NOW()+INTERVAL '365 days'`, userID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, title string
			var due time.Time
			if err := rows.Scan(&id, &title, &due); err != nil {
				rows.Close()
				return nil, err
			}
			sources = append(sources, timedCalendarSource("maintenance", id, title, due))
		}
		rows.Close()
	}
	if preferences.TicketsFaults {
		rows, err := h.db.Query(ctx, `
			SELECT id::text, title, due_date FROM tickets
			WHERE assigned_to=$1::uuid AND status IN ('open','in_progress','pending') AND due_date BETWEEN NOW()-INTERVAL '30 days' AND NOW()+INTERVAL '365 days'`, userID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, title string
			var due time.Time
			if err := rows.Scan(&id, &title, &due); err != nil {
				rows.Close()
				return nil, err
			}
			sources = append(sources, timedCalendarSource("ticket", id, title, due))
		}
		rows.Close()
		rows, err = h.db.Query(ctx, `
			SELECT id::text, title, due_date FROM faults
			WHERE assigned_to=$1::uuid AND status IN ('detected','analyzing','in_progress','pending') AND due_date BETWEEN NOW()-INTERVAL '30 days' AND NOW()+INTERVAL '365 days'`, userID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, title string
			var due time.Time
			if err := rows.Scan(&id, &title, &due); err != nil {
				rows.Close()
				return nil, err
			}
			sources = append(sources, timedCalendarSource("fault", id, title, due))
		}
		rows.Close()
	}
	return sources, nil
}

func allDayCalendarSource(sourceType, id, title, date string) (microsoftCalendarSource, bool) {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return microsoftCalendarSource{}, false
	}
	start := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC)
	return microsoftCalendarSource{Type: sourceType, ID: id, Title: "PDH · " + sourceType + " · " + title, Start: start, End: start.Add(24 * time.Hour), AllDay: true}, true
}

func timedCalendarSource(sourceType, id, title string, due time.Time) microsoftCalendarSource {
	return microsoftCalendarSource{Type: sourceType, ID: id, Title: "PDH · " + sourceType + " · " + title, Start: due.UTC(), End: due.UTC().Add(time.Hour)}
}

func (h *Handler) removeStaleMicrosoftEvents(ctx context.Context, userID, kind, accessToken string, preferences microsoftCalendarPreferences, active map[string]bool) (int, error) {
	rows, err := h.db.Query(ctx, `SELECT source_type, source_id::text, microsoft_event_id FROM microsoft_calendar_links WHERE user_id=$1::uuid AND account_kind=$2`, userID, kind)
	if err != nil {
		return 0, err
	}
	type staleEvent struct{ sourceType, sourceID, eventID string }
	stale := make([]staleEvent, 0)
	for rows.Next() {
		var item staleEvent
		if err := rows.Scan(&item.sourceType, &item.sourceID, &item.eventID); err != nil {
			rows.Close()
			return 0, err
		}
		selected := (item.sourceType == "shift" && preferences.Shifts) ||
			(item.sourceType == "task" && preferences.Tasks) ||
			(item.sourceType == "maintenance" && preferences.Maintenance) ||
			((item.sourceType == "ticket" || item.sourceType == "fault") && preferences.TicketsFaults)
		if !selected || !active[item.sourceType+":"+item.sourceID] {
			stale = append(stale, item)
		}
	}
	rows.Close()
	for _, item := range stale {
		endpoint := "https://graph.microsoft.com/v1.0/me/events/" + url.PathEscape(item.eventID)
		err := h.microsoftGraphRequest(ctx, accessToken, http.MethodDelete, endpoint, nil, nil)
		var statusErr microsoftGraphStatusError
		if err != nil && !errors.As(err, &statusErr) {
			return 0, err
		}
		if err != nil && statusErr.StatusCode != http.StatusNotFound {
			return 0, err
		}
		if _, err := h.db.Exec(ctx, `DELETE FROM microsoft_calendar_links WHERE user_id=$1::uuid AND account_kind=$4 AND source_type=$2 AND source_id=$3::uuid`, userID, item.sourceType, item.sourceID, kind); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}

func (h *Handler) importMicrosoftBusyEvents(ctx context.Context, userID, kind, accessToken string) (int, error) {
	started := time.Now().UTC()
	windowStart := started.AddDate(0, 0, -30)
	windowEnd := started.AddDate(0, 0, 180)
	query := url.Values{
		"startDateTime": {windowStart.Format(time.RFC3339)},
		"endDateTime":   {windowEnd.Format(time.RFC3339)},
		"$select":       {"id,start,end,showAs,isAllDay,isCancelled"},
		"$top":          {"500"},
	}
	nextURL := "https://graph.microsoft.com/v1.0/me/calendarView?" + query.Encode()
	count := 0
	for nextURL != "" {
		var page microsoftCalendarPage
		if err := h.getMicrosoftGraphJSON(ctx, accessToken, nextURL, &page); err != nil {
			return count, err
		}
		for _, event := range page.Events {
			if event.ID == "" || event.IsCancelled || !microsoftBusyStatus(event.ShowAs) {
				continue
			}
			start, err := parseMicrosoftDateTime(event.Start.DateTime)
			if err != nil {
				return count, err
			}
			end, err := parseMicrosoftDateTime(event.End.DateTime)
			if err != nil || !end.After(start) {
				continue
			}
			_, err = h.db.Exec(ctx, `
				INSERT INTO microsoft_calendar_blocks (user_id, account_kind, microsoft_event_id, starts_at, ends_at, show_as, is_all_day, last_seen_at)
				VALUES ($1::uuid, $8, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (user_id, account_kind, microsoft_event_id) DO UPDATE SET
					starts_at=EXCLUDED.starts_at, ends_at=EXCLUDED.ends_at, show_as=EXCLUDED.show_as,
					is_all_day=EXCLUDED.is_all_day, last_seen_at=EXCLUDED.last_seen_at`,
				userID, event.ID, start, end, event.ShowAs, event.IsAllDay, started, kind)
			if err != nil {
				return count, err
			}
			count++
		}
		nextURL = page.NextLink
	}
	_, err := h.db.Exec(ctx, `
		DELETE FROM microsoft_calendar_blocks
		WHERE user_id=$1::uuid AND account_kind=$5 AND starts_at >= $2 AND starts_at < $3 AND last_seen_at < $4`, userID, windowStart, windowEnd, started, kind)
	return count, err
}

func microsoftBusyStatus(status string) bool {
	switch strings.ToLower(status) {
	case "busy", "tentative", "oof", "workingelsewhere":
		return true
	default:
		return false
	}
}

func parseMicrosoftDateTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid Microsoft calendar datetime")
}

func (h *Handler) microsoftGraphRequest(ctx context.Context, accessToken, method, endpoint string, payload interface{}, target interface{}) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "graph.microsoft.com" {
		return fmt.Errorf("invalid Microsoft Graph URL")
	}
	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Prefer", `outlook.timezone="UTC"`)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return microsoftGraphStatusError{StatusCode: response.StatusCode}
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target)
}
