package ops

import "testing"

func TestValidateReadOnlySQL_AllowsSelect(t *testing.T) {
	if err := ValidateReadOnlySQL("SELECT id, name FROM users WHERE id = 1"); err != nil {
		t.Fatalf("expected select allowed: %v", err)
	}
}

func TestValidateReadOnlySQL_BlocksInsert(t *testing.T) {
	if err := ValidateReadOnlySQL("INSERT INTO users VALUES (1)"); err == nil {
		t.Fatal("expected insert blocked")
	}
}

func TestValidateReadOnlySQL_BlocksMultiStatement(t *testing.T) {
	if err := ValidateReadOnlySQL("SELECT 1; SELECT 2;"); err == nil {
		t.Fatal("expected multi-statement blocked")
	}
}

func TestValidateReadOnlySQL_Empty(t *testing.T) {
	if err := ValidateReadOnlySQL("  "); err == nil {
		t.Fatal("expected empty blocked")
	}
}
