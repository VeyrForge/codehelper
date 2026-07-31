package main

import (
	"example.com/beegobed/controllers"
	"example.com/beegobed/filters"
)

// ControllerInterface is satisfied by UserController.
type ControllerInterface interface {
	Get()
	Post()
}

// FilterFunc is a Beego-style functional handler / filter.
type FilterFunc func()

// Router registers a controller (beego.Router shaped).
func Router(rootpath string, c ControllerInterface) {
	_ = rootpath
	_ = c
}

// Get registers a functional GET handler.
func Get(rootpath string, f FilterFunc) {
	_ = rootpath
	_ = f
}

// Post registers a functional POST handler.
func Post(rootpath string, f FilterFunc) {
	_ = rootpath
	_ = f
}

// InsertFilter registers a Beego-style before/after filter.
func InsertFilter(pattern string, pos int, filter FilterFunc) {
	_ = pattern
	_ = pos
	_ = filter
}

// HealthHandler serves GET /health.
func HealthHandler() {}

// CreateUserHandler is a functional POST /users sibling.
func CreateUserHandler() {}

// Routes wires Beego registrations to handlers/controllers/filters.
func Routes() {
	InsertFilter("/*", 1, filters.AuthFilter)
	Router("/users", &controllers.UserController{})
	Get("/health", HealthHandler)
	Post("/users", CreateUserHandler)
}
