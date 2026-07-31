package checkout

// CheckoutService places an order after checking stock (multi-repo-b).
// Paired locate: agents should find CheckoutService here and InventoryClient
// in the sibling bed multi-repo-a under the same CODEHELPER_TESTBEDS root.
type CheckoutService struct{}

// PlaceOrder reserves stock for a SKU (stub; real multi-root wiring is optional).
func (s *CheckoutService) PlaceOrder(sku string, qty int) bool {
	return sku != "" && qty > 0
}
