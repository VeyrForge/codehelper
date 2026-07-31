package controllers

import "example.com/beegobed/services"

// Controller is a minimal Beego-like controller base.
type Controller struct{}

// UserController handles /users (probe: Router → UserController → UserService).
type UserController struct {
	Controller
	Svc *services.UserService
}

// Prepare runs before actions (Beego Prepare hook → service leaf).
func (c *UserController) Prepare() {
	if c.Svc == nil {
		c.Svc = &services.UserService{}
	}
}

// Get is the conventional Beego GET action → ListUsers.
func (c *UserController) Get() {
	c.Prepare()
	_ = c.Svc.ListUsers()
}

// Post is the conventional Beego POST action → CreateUser.
func (c *UserController) Post() {
	c.Prepare()
	_ = c.Svc.CreateUser("new@example.com")
}
