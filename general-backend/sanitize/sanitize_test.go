package sanitize

import "testing"

func TestUUID(t *testing.T) {
	valid := "550e8400-e29b-41d4-a716-446655440000"
	if _, err := UUID(valid); err != nil {
		t.Fatalf("expected valid uuid to pass, got %v", err)
	}
	if _, err := UUID("  " + valid + "  "); err != nil {
		t.Fatalf("expected surrounding whitespace to be trimmed, got %v", err)
	}
	if _, err := UUID("not-a-uuid"); err == nil {
		t.Fatal("expected malformed uuid to be rejected")
	}
	if _, err := UUID(""); err == nil {
		t.Fatal("expected empty string to be rejected")
	}
}

func TestNonEmptyString(t *testing.T) {
	if _, err := NonEmptyString("  hello  ", 10); err != nil {
		t.Fatalf("expected trimmed string to pass, got %v", err)
	}
	if _, err := NonEmptyString("   ", 10); err == nil {
		t.Fatal("expected whitespace-only string to be rejected")
	}
	if _, err := NonEmptyString("", 10); err == nil {
		t.Fatal("expected empty string to be rejected")
	}
	if _, err := NonEmptyString("this is too long", 5); err == nil {
		t.Fatal("expected over-length string to be rejected")
	}
}

func TestOutcomes(t *testing.T) {
	cleaned, err := Outcomes([]string{" A ", "B"}, 2, 10, 100)
	if err != nil {
		t.Fatalf("expected valid outcomes to pass, got %v", err)
	}
	if cleaned[0] != "A" || cleaned[1] != "B" {
		t.Fatalf("expected trimmed outcomes, got %v", cleaned)
	}

	if _, err := Outcomes([]string{"A"}, 2, 10, 100); err == nil {
		t.Fatal("expected too-few outcomes to be rejected")
	}
	if _, err := Outcomes([]string{"A", "B", "C"}, 2, 2, 100); err == nil {
		t.Fatal("expected too-many outcomes to be rejected")
	}
	if _, err := Outcomes([]string{"A", "a"}, 2, 10, 100); err == nil {
		t.Fatal("expected case-insensitive duplicate outcomes to be rejected")
	}
	if _, err := Outcomes([]string{"A", ""}, 2, 10, 100); err == nil {
		t.Fatal("expected empty outcome to be rejected")
	}
	if _, err := Outcomes([]string{"A", "toolong"}, 2, 10, 3); err == nil {
		t.Fatal("expected over-length outcome to be rejected")
	}
}

func TestPositiveInt64(t *testing.T) {
	if _, err := PositiveInt64(100, 1000); err != nil {
		t.Fatalf("expected valid value to pass, got %v", err)
	}
	if _, err := PositiveInt64(0, 1000); err == nil {
		t.Fatal("expected zero to be rejected")
	}
	if _, err := PositiveInt64(-1, 1000); err == nil {
		t.Fatal("expected negative value to be rejected")
	}
	if _, err := PositiveInt64(2000, 1000); err == nil {
		t.Fatal("expected over-cap value to be rejected")
	}
}

func TestNonNegativeInt64(t *testing.T) {
	if _, err := NonNegativeInt64(0, 1000); err != nil {
		t.Fatalf("expected zero to pass, got %v", err)
	}
	if _, err := NonNegativeInt64(-1, 1000); err == nil {
		t.Fatal("expected negative value to be rejected")
	}
	if _, err := NonNegativeInt64(2000, 1000); err == nil {
		t.Fatal("expected over-cap value to be rejected")
	}
}