package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Kennungen in anderer Schreibweise (32 Hexziffern, Klammern, urn:uuid:) werden früh abgewiesen,
// bevor Abteilungs- und Rechteprüfung den Pfad ansehen (uuid_path.go).
func TestNonCanonicalUUIDPathRejected(t *testing.T) {
	const id = "01a18200-0000-7000-8000-000000000001"
	const hex = "01a18200000070008000000000000001"
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := NonCanonicalUUIDPathMiddleware(ok)
	code := func(p string) int {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.Path = p
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, p := range []string{
		"/projects/" + hex, "/api/v1/projects/" + hex, "/api/v1/projects/{" + id + "}", "/api/v1/projects/{" + hex + "}",
		"/api/v1/projects/urn:uuid:" + id, "/api/v1/projects/URN:UUID:" + id, "/tickets/" + hex + "/edit",
		"/api/v1/attachments/kvp/" + hex, "/records/fault/" + hex, "/projects/01a18200-0000-7000-80000000-00000001",
		"/projects/01A18200000070008000000000000001", "/api/v1/projects/01A18200-0000-7000-8000-000000000001",
	} {
		if c := code(p); c != http.StatusNotFound {
			t.Errorf("%s: %d, erwartet 404", p, c)
		}
	}
	for _, p := range []string{
		"/projects/" + id, "/api/v1/projects/" + id, "/projects", "/api/v1/projects/",
		"/tickets/" + id + "/edit",
		// Bereiche mit Dateinamen/Prüfsummen bleiben unberührt
		"/uploads/" + hex + ".png", "/static/" + hex + ".js", "/certificates/" + id + "/" + hex, "/global/b/" + hex, "/pki/ca.crt",
		"/import/esppdh/" + id + "/firmware/" + id + "/factory.bin",
	} {
		if c := code(p); c != http.StatusOK {
			t.Errorf("%s: %d, erwartet 200", p, c)
		}
	}
}
