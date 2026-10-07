package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorizedRequiresMatchingBearerToken(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	agent := &updateAgent{token: token}
	handler := agent.authorized(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		authority  string
		wantStatus int
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong", authority: "Bearer 1123456789abcdef0123456789abcdef", wantStatus: http.StatusUnauthorized},
		{name: "valid", authority: "Bearer " + token, wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
			if test.authority != "" {
				request.Header.Set("Authorization", test.authority)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("got status %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestLimitedBufferCapsOutput(t *testing.T) {
	buffer := &limitedBuffer{}
	input := strings.Repeat("x", 70*1024)
	written, err := buffer.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if written != len(input) {
		t.Fatalf("Write returned %d, want %d", written, len(input))
	}
	if buffer.Len() != 64*1024 {
		t.Fatalf("buffer length is %d, want %d", buffer.Len(), 64*1024)
	}
}
func TestValidateMountRejectsInjection(t *testing.T) {
	ok := mountRequest{Key: "daten", Kind: "smb", Source: "//nas.local/daten", Username: "pdh", Password: "geheim", Options: "vers=3.0"}
	if err := validateMount(&ok); err != nil {
		t.Fatalf("gueltige Anfrage abgelehnt: %v", err)
	}
	bad := []mountRequest{
		{Key: "../etc", Kind: "smb", Source: "//nas/x"},
		{Key: "a", Kind: "smb", Source: "//nas/x y"},
		{Key: "a", Kind: "smb", Source: "//nas/x", Options: "uid=0"},
		{Key: "a", Kind: "smb", Source: "//nas/x", Options: "credentials=/etc/shadow"},
		{Key: "a", Kind: "smb", Source: "//nas/x", Options: "vers=3.0;rm"},
		{Key: "a", Kind: "smb", Source: "//nas/x", Password: "x\nusername=root"},
		{Key: "a", Kind: "nfs", Source: "nas:relative"},
		{Key: "a", Kind: "ext4", Source: "/dev/sda1"},
	}
	for _, b := range bad {
		b := b
		if validateMount(&b) == nil {
			t.Errorf("unsichere Anfrage akzeptiert: %+v", b)
		}
	}
}
