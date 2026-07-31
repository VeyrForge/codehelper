package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseSQLLite_TablesProcsAndRefs(t *testing.T) {
	src := []byte(`
CREATE TABLE users (
  id INT PRIMARY KEY
);
CREATE TABLE orders (
  id INT PRIMARY KEY,
  user_id INT REFERENCES users(id)
);
CREATE INDEX idx_orders_user ON orders(user_id);
CREATE PROCEDURE refresh_stats() BEGIN SELECT 1; END;
CREATE TRIGGER orders_audit AFTER INSERT ON orders BEGIN END;
CREATE VIEW order_summary AS SELECT * FROM orders;
`)
	res, err := parseSQLLite(context.Background(), "repo", "schema.sql", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	var ordersID string
	for _, s := range res.Symbols {
		found[s.Name] = true
		if s.Name == "orders" {
			ordersID = s.ID
		}
	}
	for _, want := range []string{"users", "orders", "idx_orders_user", "refresh_stats", "orders_audit", "order_summary"} {
		if !found[want] {
			t.Errorf("missing SQL symbol %q; got %v", want, found)
		}
	}
	if ordersID == "" {
		t.Fatal("missing orders table")
	}
	sawRefs := false
	for _, e := range res.Edges {
		if e.SourceID == ordersID && e.Kind == types.RefKindReads && strings.HasSuffix(e.TargetID, ":users") {
			sawRefs = true
		}
	}
	if !sawRefs {
		t.Fatal("expected orders READS users via REFERENCES")
	}
}
