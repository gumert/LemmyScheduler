package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const pingPath = "/lemmyscheduler.v1.PingService/Ping"

func TestMuxPingOverConnectJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, pingPath, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	newMux(noEnv).ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", res.Code, http.StatusOK, res.Body.String())
	}
	if got, want := res.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if got, want := res.Body.String(), `{"message":"pong"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestMuxPingPreflightHonorsCORSAllowedOrigin(t *testing.T) {
	override := func(key string) string {
		if key == "CORS_ALLOWED_ORIGIN" {
			return "https://scheduler.example"
		}
		return ""
	}
	tests := []struct {
		name   string
		getenv func(string) string
		origin string
		want   int
	}{
		{"default allows localhost:3000", noEnv, "http://localhost:3000", http.StatusNoContent},
		{"default rejects other origin", noEnv, "https://scheduler.example", http.StatusForbidden},
		{"override allows configured origin", override, "https://scheduler.example", http.StatusNoContent},
		{"override rejects default origin", override, "http://localhost:3000", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, pingPath, nil)
			req.Header.Set("Origin", tt.origin)
			req.Header.Set("Access-Control-Request-Method", "POST")
			res := httptest.NewRecorder()

			newMux(tt.getenv).ServeHTTP(res, req)

			if res.Code != tt.want {
				t.Fatalf("status = %d, want %d", res.Code, tt.want)
			}
		})
	}
}
