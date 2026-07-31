package services

// UserService is the Beego controller dependency leaf (probe: Get → ListUsers).
type UserService struct{}

// ListUsers returns users for UserController.Get.
func (s *UserService) ListUsers() []string {
	return []string{"alice", "bob"}
}

// CreateUser creates a user for UserController.Post.
func (s *UserService) CreateUser(email string) string {
	return email
}
