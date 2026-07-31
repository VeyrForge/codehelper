package inventory

// InventoryClient looks up stock for SKUs (multi-repo-a side of the pair).
type InventoryClient struct{}

// GetStock returns available units for a SKU.
func (c *InventoryClient) GetStock(sku string) int {
	if sku == "" {
		return 0
	}
	return 3
}
