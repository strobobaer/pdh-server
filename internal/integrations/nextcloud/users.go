package nextcloud

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Nextcloud-Konten über die Provisioning-API (OCS) anlegen. Der Zugang aus den
// Server-Einstellungen (PDH_NEXTCLOUD_USERNAME) braucht dafür Admin- oder
// Gruppen-Admin-Rechte in Nextcloud.

type UserClient struct {
	baseURL, username, password string
	httpClient                  *http.Client
}

// UserClientFromEnv: gleiche Nextcloud-Adresse und Zugangsdaten wie die Ablage.
func UserClientFromEnv() *UserClient {
	return &UserClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_BASEURL")), "/"),
		username:   strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_USERNAME")),
		password:   os.Getenv("PDH_NEXTCLOUD_PASSWORD"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *UserClient) Enabled() bool {
	return c != nil && c.baseURL != "" && c.username != "" && c.password != ""
}

type NewUser struct {
	UserID, DisplayName, Email, Language string
}

// ErrUserExists: in Nextcloud gibt es die Benutzer-ID schon.
var ErrUserExists = errors.New("nextcloud-benutzer existiert bereits")

// CreateUser legt das Konto mit einem zufälligen Startpasswort an und gibt es zurück.
func (c *UserClient) CreateUser(ctx context.Context, u NewUser) (string, error) {
	if !c.Enabled() {
		return "", errors.New("Nextcloud ist nicht eingerichtet (Server-Einstellungen → Nextcloud)")
	}
	pw := RandomPassword(20)
	form := url.Values{"userid": {u.UserID}, "password": {pw}}
	if u.DisplayName != "" {
		form.Set("displayName", u.DisplayName)
	}
	if u.Email != "" {
		form.Set("email", u.Email)
	}
	if u.Language != "" {
		form.Set("language", u.Language)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/ocs/v1.php/cloud/users?format=json", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", errors.New("Nextcloud lehnt den Zugang ab – der Nextcloud-Benutzer in den Server-Einstellungen braucht Admin-Rechte")
	}
	var r struct {
		OCS struct {
			Meta struct {
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("nextcloud: %s", resp.Status)
	}
	switch r.OCS.Meta.StatusCode {
	case 100, 200:
		return pw, nil
	case 102:
		return "", ErrUserExists
	default:
		msg := strings.TrimSpace(r.OCS.Meta.Message)
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("nextcloud (%d): %s", r.OCS.Meta.StatusCode, msg)
	}
}

var invalidUserIDChars = regexp.MustCompile(`[^a-zA-Z0-9_.@-]+`)

// SanitizeUserID: Nextcloud erlaubt nur a-z, A-Z, 0-9, _ . @ - in Benutzer-IDs.
func SanitizeUserID(s string) string {
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss", " ", ".").Replace(strings.TrimSpace(s))
	return strings.Trim(invalidUserIDChars.ReplaceAllString(s, ""), ".-")
}

// RandomPassword enthält Groß-/Kleinbuchstaben, Ziffern und Sonderzeichen
// (übliche Nextcloud-Passwortrichtlinie), ohne leicht verwechselbare Zeichen.
func RandomPassword(n int) string {
	sets := []string{"ABCDEFGHJKLMNPQRSTUVWXYZ", "abcdefghijkmnpqrstuvwxyz", "23456789", "!#%+-=?@"}
	all := strings.Join(sets, "")
	pick := func(s string) byte {
		i, err := rand.Int(rand.Reader, big.NewInt(int64(len(s))))
		if err != nil {
			panic(err)
		}
		return s[i.Int64()]
	}
	b := make([]byte, n)
	for i := range b {
		if i < len(sets) {
			b[i] = pick(sets[i])
		} else {
			b[i] = pick(all)
		}
	}
	for i := len(b) - 1; i > 0; i-- { // mischen
		j, _ := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		b[i], b[j.Int64()] = b[j.Int64()], b[i]
	}
	return string(b)
}
