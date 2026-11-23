package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTerminalAdminURL(t *testing.T) {
	long := "https://" + strings.Repeat("a", 250) + ":8443/admin/"
	for _, c := range []struct{ v, want string }{
		{"https://10.1.2.3:8443/admin/", "https://10.1.2.3:8443/admin/"},
		{"https://[fd00::1]:8443/admin/", "https://[fd00::1]:8443/admin/"},
		{"https://pi-halle1.lan.example:443/admin/", "https://pi-halle1.lan.example:443/admin/"},
		{"https://pi:1/admin/", "https://pi:1/admin/"},
		{"https://pi:65535/admin/", "https://pi:65535/admin/"},
		{"", ""},
		{"http://10.1.2.3:8443/admin/", ""},
		{"javascript:alert(1)", ""},
		{"https://user:pw@10.1.2.3:8443/admin/", ""},
		{"https://10.1.2.3:8443/other/", ""},
		{"https://10.1.2.3:8443/admin", ""},
		{"https://10.1.2.3:8443/admin/x", ""},
		{"https://10.1.2.3:8443/admin/?a=1", ""},
		{"https://10.1.2.3:8443/admin/#x", ""},
		{"https://10.1.2.3/admin/", ""},
		{"https://10.1.2.3:0/admin/", ""},
		{"https://10.1.2.3:65536/admin/", ""},
		{"https://10.1.2.3:80a/admin/", ""},
		{"https://piä.lan:8443/admin/", ""},
		{"https://pi lan:8443/admin/", ""},
		{"https://\"x:8443/admin/", ""},
		{"https://fd00::1:8443/admin/", ""},
		{"https://[fe80::1%25eth0]:8443/admin/", ""},
		{"https://-a.lan:8443/admin/", ""},
		{"https://a..lan:8443/admin/", ""},
		{long, ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Terminal-Admin-URL", c.v)
		if got := terminalAdminURL(r); got != c.want {
			t.Errorf("%q: %q, erwartet %q", c.v, got, c.want)
		}
	}
}

func TestTerminalGearRendered(t *testing.T) {
	tmpl := loadTestTemplates(t)
	// Angriffsversuch im Wert: html/template maskiert; target und rel stehen immer.
	evil := `https://10.1.2.3:8443/admin/"><script>x</script>`
	out := renderPage(t, tmpl, "kiosk_terminals", KioskTerminalsPageData{PDHTerminals: []pdhTerminalView{
		{DeviceID: "d1", Name: "Mit", AdminURL: "https://10.1.2.3:8443/admin/", Link: "/users/u1?tab=master"},
		{DeviceID: "d2", Name: "Ohne", Link: "/users/u1?tab=master"},
		{DeviceID: "d3", Name: "Boese", AdminURL: evil, Link: "/users/u1?tab=master"},
	}})
	if !strings.Contains(out, `href="https://10.1.2.3:8443/admin/" target="_blank" rel="noopener noreferrer"`) {
		t.Error("Zahnrad mit Adresse fehlt")
	}
	if !strings.Contains(out, `title="Terminal meldet seine Adresse ab 0.4.5"`) || !strings.Contains(out, `aria-disabled="true"`) {
		t.Error("graues Zahnrad fehlt")
	}
	if strings.Contains(out, "<script>x</script>") {
		t.Error("Adresse nicht maskiert")
	}
	ud := UserDetailData{CanDevices: true, User: UserView{ID: "u1", IsSystemUser: true}, Devices: []terminalDeviceRow{
		{ID: "d1", Name: "Mit", AdminURL: "https://10.1.2.3:8443/admin/"}, {ID: "d2", Name: "Ohne"},
	}}
	out = renderPage(t, tmpl, "user_detail", ud)
	if !strings.Contains(out, `href="https://10.1.2.3:8443/admin/" target="_blank" rel="noopener noreferrer"`) || !strings.Contains(out, "Terminal meldet seine Adresse ab 0.4.5") {
		t.Error("Zahnrad in Gekoppelte Geräte fehlt")
	}
	ud.CanDevices = false
	if out = renderPage(t, tmpl, "user_detail", ud); strings.Contains(out, "10.1.2.3:8443/admin/") {
		t.Error("Zahnrad ohne Recht sichtbar")
	}
}
