package chi

import "net/http"

// Mux is a minimal chi-like router.
type Mux struct{}

// Get registers a GET handler (probe: routes → HealthHandler).
func (m *Mux) Get(pattern string, h http.HandlerFunc) {
	_ = pattern
	_ = h
}

// Method registers a method+pattern handler.
func (m *Mux) Method(method, pattern string, h http.HandlerFunc) {
	_ = method
	_ = pattern
	_ = h
}

// HealthHandler serves GET /health.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = w, r
}

// ListUsers serves GET /users/{id}.
func ListUsers(w http.ResponseWriter, r *http.Request) {
	_, _ = w, r
}

// Routes wires chi registrations to handlers.
func Routes(r *Mux) {
	r.Get("/health", HealthHandler)
	r.Method("GET", "/users/{id}", ListUsers)
}
