package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const pingPath = "/lemmyscheduler.v1.PingService/Ping"

func TestCORSAllowedOriginDefaultsWhenUnset(t *testing.T) {
	got := CORSAllowedOrigin(func(string) string { return "" })
	if want := "http://localhost:3000"; got != want {
		t.Fatalf("CORSAllowedOrigin() = %q, want %q", got, want)
	}
}

func TestCORSAllowedOriginUsesEnvOverride(t *testing.T) {
	getenv := func(key string) string {
		if key == "CORS_ALLOWED_ORIGIN" {
			return "https://scheduler.example"
		}
		return ""
	}

	got := CORSAllowedOrigin(getenv)
	if want := "https://scheduler.example"; got != want {
		t.Fatalf("CORSAllowedOrigin() = %q, want %q", got, want)
	}
}

func newTestHandler() (http.Handler, *bool) {
	called := new(bool)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
	return CORS("http://localhost:3000", next), called
}

func preflight(origin string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, pingPath, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,connect-protocol-version")
	return req
}

func TestCORSPreflightFromAllowedOrigin(t *testing.T) {
	h, called := newTestHandler()
	res := httptest.NewRecorder()

	h.ServeHTTP(res, preflight("http://localhost:3000"))

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
	}
	if *called {
		t.Fatal("preflight must not reach the wrapped handler")
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":  "http://localhost:3000",
		"Access-Control-Allow-Methods": "POST, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms",
		"Access-Control-Max-Age":       "7200",
	}
	for name, value := range want {
		if got := res.Header().Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if got, want := strings.Join(res.Header().Values("Vary"), ", "), "Origin, Access-Control-Request-Headers"; got != want {
		t.Errorf("Vary = %q, want %q", got, want)
	}
}

func TestCORSPreflightFromDisallowedOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
	}{
		{"other origin", "http://evil.example"},
		{"same host different port", "http://localhost:3001"},
		{"wildcard-looking", "*"},
		{"missing origin", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, called := newTestHandler()
			res := httptest.NewRecorder()

			h.ServeHTTP(res, preflight(tt.origin))

			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
			}
			if *called {
				t.Fatal("preflight must not reach the wrapped handler")
			}
			for name := range res.Header() {
				if strings.HasPrefix(name, "Access-Control-") {
					t.Errorf("unexpected header %s = %q", name, res.Header().Get(name))
				}
			}
		})
	}
}

func TestCORSNonPreflightRequests(t *testing.T) {
	tests := []struct {
		name       string
		origin     string
		wantOrigin string
	}{
		{"allowed origin", "http://localhost:3000", "http://localhost:3000"},
		{"other origin", "http://evil.example", ""},
		{"no origin", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, called := newTestHandler()
			req := httptest.NewRequest(http.MethodPost, pingPath, nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			res := httptest.NewRecorder()

			h.ServeHTTP(res, req)

			if !*called {
				t.Fatal("request did not reach the wrapped handler")
			}
			if got := res.Header().Get("Access-Control-Allow-Origin"); got != tt.wantOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantOrigin)
			}
			if got, want := res.Header().Get("Vary"), "Origin"; got != want {
				t.Errorf("Vary = %q, want %q", got, want)
			}
		})
	}
}
