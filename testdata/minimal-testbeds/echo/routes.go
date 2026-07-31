package echo

// Context is a minimal Echo-like request context (probe surface).
type Context struct{}

// Echo is a minimal router (labstack/echo shaped).
type Echo struct{}

// GET registers a route handler (probe: routes → HealthHandler).
func (e *Echo) GET(path string, h HandlerFunc) {
	_ = path
	_ = h
}

// POST registers a POST handler.
func (e *Echo) POST(path string, h HandlerFunc) {
	_ = path
	_ = h
}

// HandlerFunc is an Echo-style handler.
type HandlerFunc func(Context) error

// HealthHandler serves GET /health.
func HealthHandler(c Context) error {
	_ = c
	return nil
}

// ListUsers serves POST /users.
func ListUsers(c Context) error {
	_ = c
	return nil
}

// Routes wires Echo registrations to handlers.
func Routes(e *Echo) {
	e.GET("/health", HealthHandler)
	e.POST("/users", ListUsers)
}
