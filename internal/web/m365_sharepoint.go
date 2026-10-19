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
)

// SharePoint-Bibliothek für Office-Vorlagen über Microsoft Graph (App-Token,
// Anwendungsberechtigung Sites.ReadWrite.All).

type sharePointLibrary struct {
	token, driveID string
	client         *http.Client
}

// sharePointFolder: Kategorie als Ordnername (ohne in SharePoint verbotene Zeichen).
func sharePointFolder(category string) string {
	s := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`"*:<>?/\|#%~&{}`, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(category))
	return strings.Trim(s, ". ")
}

// sharePointSitePath: „firma.sharepoint.com/sites/Vorlagen“ → Host und Pfad.
func sharePointSitePath(site string) (host, path string, err error) {
	site = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(site), "https://"), "/")
	host, path, _ = strings.Cut(site, "/")
	if !strings.HasSuffix(strings.ToLower(host), ".sharepoint.com") {
		return "", "", errors.New("SharePoint-Site muss auf .sharepoint.com liegen, z. B. firma.sharepoint.com/sites/Vorlagen")
	}
	return host, path, nil
}

func (h *Handler) openSharePoint(ctx context.Context) (*sharePointLibrary, error) {
	site := h.getUpdateSetting(ctx, m365SharePointSite, "")
	library := strings.TrimSpace(h.getUpdateSetting(ctx, m365SharePointLibrary, ""))
	if site == "" || library == "" {
		return nil, errors.New("SharePoint ist nicht eingerichtet (Reiter Einstellungen: Site und Bibliothek)")
	}
	if !h.microsoftAppConfigured() {
		return nil, errors.New("Microsoft 365 ist nicht eingerichtet (Server-Einstellungen → Microsoft 365)")
	}
	host, path, err := sharePointSitePath(site)
	if err != nil {
		return nil, err
	}
	token, err := h.microsoftDirectoryToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := microsoftGraphBase + "/v1.0/sites/" + host
	if path != "" {
		endpoint += ":/" + path
	}
	var s struct {
		ID string `json:"id"`
	}
	if err := h.microsoftGraphRequest(ctx, token, http.MethodGet, endpoint, nil, &s); err != nil {
		return nil, sharePointError("Site "+site, err)
	}
	var drives struct {
		Value []struct{ ID, Name string } `json:"value"`
	}
	if err := h.microsoftGraphRequest(ctx, token, http.MethodGet, microsoftGraphBase+"/v1.0/sites/"+s.ID+"/drives", nil, &drives); err != nil {
		return nil, sharePointError("Bibliotheken", err)
	}
	for _, d := range drives.Value {
		if strings.EqualFold(d.Name, library) {
			return &sharePointLibrary{token: token, driveID: d.ID, client: &http.Client{Timeout: 2 * time.Minute}}, nil
		}
	}
	return nil, fmt.Errorf("Bibliothek „%s“ gibt es auf der Site nicht", library)
}

func sharePointError(what string, err error) error {
	var se microsoftGraphStatusError
	if errors.As(err, &se) {
		switch se.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			return fmt.Errorf("%s: kein Zugriff – der App fehlt die Anwendungsberechtigung Sites.ReadWrite.All", what)
		case http.StatusNotFound:
			return fmt.Errorf("%s nicht gefunden", what)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// upload legt die Datei in folder ab (überschreibt gleichnamige) und gibt die Element-ID zurück.
func (s *sharePointLibrary) upload(ctx context.Context, folder, name string, data []byte) (string, error) {
	p := url.PathEscape(name)
	if folder != "" {
		p = url.PathEscape(folder) + "/" + p
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		microsoftGraphBase+"/v1.0/drives/"+s.driveID+"/root:/"+p+":/content", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", sharePointError(name, microsoftGraphStatusError{StatusCode: resp.StatusCode})
	}
	var item struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&item); err != nil {
		return "", err
	}
	return item.ID, nil
}

// delete entfernt ein Element; schon gelöschte zählen als erledigt.
func (s *sharePointLibrary) delete(ctx context.Context, itemID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, microsoftGraphBase+"/v1.0/drives/"+s.driveID+"/items/"+url.PathEscape(itemID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return sharePointError("Löschen", microsoftGraphStatusError{StatusCode: resp.StatusCode})
}
