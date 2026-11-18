package nextcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Provisioning-API (OCS) für den Abgleich PDH → Nextcloud: Konten lesen und
// ändern, Gruppen anlegen, Mitglieder setzen. Alle Aufrufe laufen mit dem
// Dienstkonto aus den Server-Einstellungen.

// RemoteUser: ein Konto, wie Nextcloud es führt.
type RemoteUser struct {
	ID          string
	DisplayName string
	Email       string
	Language    string
	Enabled     bool
	Groups      []string
}

// RemoteGroup: eine Gruppe mit Anzeigenamen.
type RemoteGroup struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayname"`
}

// ErrNotFound: Nextcloud kennt das Konto bzw. die Gruppe nicht.
var ErrNotFound = errors.New("in nextcloud nicht gefunden")

// ocs ruft /ocs/v1.php/cloud/<path> auf und legt „data“ in out ab.
// Statuscode 100 ist Erfolg; ok nennt weitere Codes, die nicht als Fehler zählen.
func (c *UserClient) ocs(ctx context.Context, method, path string, form url.Values, out any, ok ...int) (int, error) {
	if !c.Enabled() {
		return 0, errors.New("Nextcloud ist nicht eingerichtet (Server-Einstellungen → Nextcloud)")
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/ocs/v1.php/cloud/"+path+sep+"format=json", body)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(c.username, c.password)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return 0, ErrLoginFailed
	}
	var r struct {
		OCS struct {
			Meta struct {
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, fmt.Errorf("nextcloud: %s", resp.Status)
	}
	code := r.OCS.Meta.StatusCode
	for _, k := range ok {
		if k == code {
			return code, nil
		}
	}
	switch {
	case code == 997:
		return code, ErrLoginFailed
	case code == 404 || code == 998 || resp.StatusCode == http.StatusNotFound:
		return code, ErrNotFound
	case code != 100 && code != 200:
		msg := strings.TrimSpace(r.OCS.Meta.Message)
		if msg == "" {
			msg = resp.Status
		}
		return code, fmt.Errorf("nextcloud (%d): %s", code, msg)
	}
	if out != nil && len(r.OCS.Data) > 0 {
		if err := json.Unmarshal(r.OCS.Data, out); err != nil {
			return code, fmt.Errorf("nextcloud: unerwartete Antwort (%v)", err)
		}
	}
	return code, nil
}

// Users: alle Benutzer-IDs.
func (c *UserClient) Users(ctx context.Context) ([]string, error) {
	var d struct {
		Users []string `json:"users"`
	}
	_, err := c.ocs(ctx, http.MethodGet, "users", nil, &d)
	return d.Users, err
}

// UserInfo: Stammdaten und Gruppen eines Kontos.
func (c *UserClient) UserInfo(ctx context.Context, id string) (*RemoteUser, error) {
	var d struct {
		ID          string   `json:"id"`
		Enabled     any      `json:"enabled"` // bool, bei alten Fassungen "true"
		DisplayName string   `json:"displayname"`
		Email       *string  `json:"email"`
		Language    string   `json:"language"`
		Groups      []string `json:"groups"`
	}
	if _, err := c.ocs(ctx, http.MethodGet, "users/"+url.PathEscape(id), nil, &d); err != nil {
		return nil, err
	}
	u := &RemoteUser{ID: d.ID, DisplayName: d.DisplayName, Language: d.Language, Groups: d.Groups}
	if d.Email != nil {
		u.Email = *d.Email
	}
	switch v := d.Enabled.(type) {
	case bool:
		u.Enabled = v
	case string:
		u.Enabled = v == "true" || v == "1"
	default:
		u.Enabled = true
	}
	if u.ID == "" {
		u.ID = id
	}
	return u, nil
}

// EditUser setzt ein Feld (displayname, email, language, password).
func (c *UserClient) EditUser(ctx context.Context, id, key, value string) error {
	_, err := c.ocs(ctx, http.MethodPut, "users/"+url.PathEscape(id), url.Values{"key": {key}, "value": {value}}, nil)
	return err
}

// SetEnabled sperrt ein Konto oder gibt es wieder frei.
func (c *UserClient) SetEnabled(ctx context.Context, id string, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	_, err := c.ocs(ctx, http.MethodPut, "users/"+url.PathEscape(id)+"/"+action, url.Values{}, nil)
	return err
}

// Groups: alle Gruppen mit Anzeigenamen.
func (c *UserClient) Groups(ctx context.Context) ([]RemoteGroup, error) {
	var d struct {
		Groups []RemoteGroup `json:"groups"`
	}
	_, err := c.ocs(ctx, http.MethodGet, "groups/details", nil, &d)
	return d.Groups, err
}

// CreateGroup legt eine Gruppe an; eine vorhandene ist kein Fehler.
func (c *UserClient) CreateGroup(ctx context.Context, id, displayName string) error {
	form := url.Values{"groupid": {id}}
	if displayName != "" {
		form.Set("displayname", displayName)
	}
	_, err := c.ocs(ctx, http.MethodPost, "groups", form, nil, 102)
	return err
}

// SetGroupDisplayName benennt eine Gruppe um (die Kennung bleibt).
func (c *UserClient) SetGroupDisplayName(ctx context.Context, id, displayName string) error {
	_, err := c.ocs(ctx, http.MethodPut, "groups/"+url.PathEscape(id), url.Values{"key": {"displayname"}, "value": {displayName}}, nil)
	return err
}

// DeleteGroup löscht eine Gruppe; eine fehlende ist kein Fehler.
func (c *UserClient) DeleteGroup(ctx context.Context, id string) error {
	_, err := c.ocs(ctx, http.MethodDelete, "groups/"+url.PathEscape(id), nil, nil, 101)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// GroupMembers: Benutzer-IDs der Mitglieder.
func (c *UserClient) GroupMembers(ctx context.Context, id string) ([]string, error) {
	var d struct {
		Users []string `json:"users"`
	}
	_, err := c.ocs(ctx, http.MethodGet, "groups/"+url.PathEscape(id)+"/users", nil, &d)
	return d.Users, err
}

// AddToGroup nimmt ein Konto in eine Gruppe auf.
func (c *UserClient) AddToGroup(ctx context.Context, userID, groupID string) error {
	_, err := c.ocs(ctx, http.MethodPost, "users/"+url.PathEscape(userID)+"/groups", url.Values{"groupid": {groupID}}, nil)
	return err
}

// RemoveFromGroup nimmt ein Konto aus einer Gruppe.
func (c *UserClient) RemoveFromGroup(ctx context.Context, userID, groupID string) error {
	_, err := c.ocs(ctx, http.MethodDelete, "users/"+url.PathEscape(userID)+"/groups", url.Values{"groupid": {groupID}}, nil)
	return err
}
