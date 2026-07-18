package buildinfo

import "testing"

func TestReportedUsesLinkedVersion(t *testing.T) {
	original := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = original })

	if got := Reported(); got != "v1.2.3" {
		t.Fatalf("Reported() = %q, want v1.2.3", got)
	}
}
