package bootstrap

import (
	"regexp"
	"testing"
)

// TestUUIDGeneratorProducesDistinctVersion4Identifiers valida formato, variante e unicidade básica.
func TestUUIDGeneratorProducesDistinctVersion4Identifiers(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	first, err := (UUIDGenerator{}).NewID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := (UUIDGenerator{}).NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !pattern.MatchString(first) || !pattern.MatchString(second) || first == second {
		t.Fatalf("generated UUIDs = %q and %q", first, second)
	}
}
