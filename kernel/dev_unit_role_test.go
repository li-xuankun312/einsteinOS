package kernel

import (
	"strings"
	"testing"
)

// Two units on their first fix attempt at once have roles of their own; both are still fix
// attempts to everything that reads the role by its prefix.
func TestTwoUnitsFixedAtOnceHaveRolesOfTheirOwn(t *testing.T) {
	a := devUnitRole("u316bd6b9ec", 15, 2)
	b := devUnitRole("u52d156705c", 16, 2)
	if a == b {
		t.Fatalf("both are %q", a)
	}
	for _, r := range []string{a, b} {
		if !strings.HasPrefix(r, "dev_fix") || !RoleWritesCode(r) || !RoleSeesMasked(r) {
			t.Fatalf("%q is not read as a fix attempt", r)
		}
	}
	if got := devUnitRole("u89be81de0b", 0, 1); got != "dev_unit_1" {
		t.Fatalf("a first attempt: %q", got)
	}
}
