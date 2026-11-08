package api

import "net/http"

// defaultCORSAllowedOrigin is the Next.js dev server's origin.
const defaultCORSAllowedOrigin = "http://localhost:3000"

// CORSAllowedOrigin resolves the single browser origin allowed to call the
// API from the CORS_ALLOWED_ORIGIN environment variable, falling back to the
// local frontend when it is unset or empty. The getenv function is injected so
// the fallback can be tested without mutating process-wide environment state.
func CORSAllowedOrigin(getenv func(string) string) string {
	if origin := getenv("CORS_ALLOWED_ORIGIN"); origin != "" {
		return origin
	}
	return defaultCORSAllowedOrigin
}

// CORS wraps next so browsers served from allowedOrigin may call it with
// Connect requests.
//
// Only an Origin that exactly matches allowedOrigin is echoed back; the
// wildcard is never sent. A preflight from any other origin is rejected with
// 403. Any other request is passed through untouched, without CORS headers:
// CORS only controls what browsers expose to a page. It is not authentication,
// and non-browser clients ignore it entirely.
func CORS(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Add("Vary", "Origin")

		origin := r.Header.Get("Origin")
		allowed := origin != "" && origin == allowedOrigin

		if isPreflight(r) {
			h.Add("Vary", "Access-Control-Request-Headers")
			if !allowed {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms")
			h.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if allowed {
			h.Set("Access-Control-Allow-Origin", origin)
		}
		next.ServeHTTP(w, r)
	})
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}
