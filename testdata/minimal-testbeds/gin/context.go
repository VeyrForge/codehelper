package gin

// Context carries request-scoped values for a Gin-like handler.
type Context struct {
	Status int
}

// JSON writes a JSON response (probe: Context.JSON).
func (c *Context) JSON(code int, obj any) {
	c.Status = code
	_ = obj
}

// AbortWithStatus aborts the chain with a status code.
func (c *Context) AbortWithStatus(code int) {
	c.Status = code
}
