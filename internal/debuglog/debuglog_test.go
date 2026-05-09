package debuglog

import "testing"

func TestEnabled_DefaultFalse(t *testing.T) {
	if Enabled() {
		t.Fatalf("Enabled() should be false before Init")
	}
}
