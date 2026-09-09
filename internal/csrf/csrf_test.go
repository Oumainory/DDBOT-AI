package csrf

import "testing"

func TestCSRFTokenIsDistinctAndConstantTimeValidated(t *testing.T) {
	first, err := GenerateToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 43 {
		t.Fatalf("tokens = %q, %q", first, second)
	}
	if !Validate(first, first) || Validate(first, second) || Validate(first, "") {
		t.Fatal("csrf validation accepted an invalid token")
	}
}
