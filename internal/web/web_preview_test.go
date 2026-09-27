package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFlattenJSON(t *testing.T) {
	raw := []byte(`{"data":{"sensors":[{"name":"Halle 1","wert":21.4},{"name":"Halle 2","wert":19}]},"ok":true,"note":null}`)
	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	values := flattenJSONMap(parsed)

	cases := map[string]string{
		"data.sensors[0].name": "Halle 1",
		"data.sensors[0].wert": "21.4",
		"data.sensors[1].name": "Halle 2",
		"data.sensors[1].wert": "19",
		"ok":                   "true",
		"note":                 "null",
	}
	for path, want := range cases {
		if got := values[path]; got != want {
			t.Errorf("values[%q] = %q, want %q", path, got, want)
		}
	}
	if len(values) != len(cases) {
		t.Errorf("got %d flattened fields, want %d: %+v", len(values), len(cases), values)
	}
}

func TestBuildImportRequestWeb(t *testing.T) {
	req, err := buildImportRequest("web", map[string]string{"url": "http://example.com/data", "method": "post"})
	if err != nil {
		t.Fatalf("buildImportRequest: %v", err)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.String() != "http://example.com/data" {
		t.Errorf("url = %q", req.URL.String())
	}
}

func TestBuildImportRequestWebDefaultMethod(t *testing.T) {
	req, err := buildImportRequest("web", map[string]string{"url": "http://example.com/data"})
	if err != nil {
		t.Fatalf("buildImportRequest: %v", err)
	}
	if req.Method != "GET" {
		t.Errorf("method = %q, want GET", req.Method)
	}
}

func TestBuildImportRequestNoURL(t *testing.T) {
	if _, err := buildImportRequest("web", map[string]string{}); err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestBuildImportRequestRestAPIAuth(t *testing.T) {
	req, err := buildImportRequest("rest_api", map[string]string{
		"base_url": "http://example.com/api", "auth_method": "bearer", "bearer_token": "secret123",
	})
	if err != nil {
		t.Fatalf("buildImportRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret123" {
		t.Errorf("Authorization header = %q", got)
	}
}

func TestBuildImportRequestRestAPIBasicAuth(t *testing.T) {
	req, err := buildImportRequest("rest_api", map[string]string{
		"base_url": "http://example.com/api", "auth_method": "basic", "username": "u", "password": "p",
	})
	if err != nil {
		t.Fatalf("buildImportRequest: %v", err)
	}
	user, pass, ok := req.BasicAuth()
	if !ok || user != "u" || pass != "p" {
		t.Errorf("BasicAuth = %q %q %v", user, pass, ok)
	}
}

func TestBuildImportRequestRestAPIApiKey(t *testing.T) {
	req, err := buildImportRequest("rest_api", map[string]string{
		"base_url": "http://example.com/api", "auth_method": "api_key", "api_key_header": "X-Api-Key", "api_key_value": "abc",
	})
	if err != nil {
		t.Fatalf("buildImportRequest: %v", err)
	}
	if got := req.Header.Get("X-Api-Key"); got != "abc" {
		t.Errorf("X-Api-Key header = %q", got)
	}
}

func TestFetchImportResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer xyz" {
			t.Errorf("server saw Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"temp": 21.4}`))
	}))
	defer srv.Close()

	body, status, err := fetchImportResponse("rest_api", map[string]string{
		"base_url": srv.URL, "auth_method": "bearer", "bearer_token": "xyz",
	})
	if err != nil {
		t.Fatalf("fetchImportResponse: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	var parsed map[string]float64
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if parsed["temp"] != 21.4 {
		t.Errorf("temp = %v, want 21.4", parsed["temp"])
	}
}
