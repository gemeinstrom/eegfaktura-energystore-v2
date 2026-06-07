package queryengine

import "testing"

// TestConsumerSlotForCode pinnt die OBIS-→-Slot-Mapping inkl. der
// T-/R-Varianten, die seit 8.4.2024 EDA-Standard sind. v2-pre dieser
// Patch droppte sie silent; ab jetzt landen sie wie in v1 im selben
// Slot wie die jeweilige non-T-Variante (last-write-wins).
func TestConsumerSlotForCode(t *testing.T) {
	cases := []struct {
		code string
		slot int
		ok   bool
	}{
		// Standard-Codes (existieren seit Tag 1).
		{"1-1:1.9.0 G.01", 0, true},
		{"1-1:2.9.0 G.02", 1, true},
		{"1-1:2.9.0 G.03", 2, true},
		// Mehrfachteilnahme-Codes (seit 8.4.2024 EDA-Standard) —
		// v1-Verhalten: gleicher Slot wie non-T, last-write-wins im
		// Loader (siehe queryengine/loader.go:placeCell). Damit ist
		// v2 wieder parity zu v1.
		{"1-1:1.9.0 G.01T", 0, true},
		{"1-1:2.9.0 G.03R", 2, true},
		// Unbekanntes: keine Spalte, ok=false → Loader silent-droppt.
		{"1-1:1.9.0 X.99", 0, false},
		{"", 0, false},
		{"junk", 0, false},
		// Producer-Code für Consumer-Lookup → false.
		{"1-1:2.9.0 P.01", 0, false},
		// Whitespace-Toleranz.
		{"  1-1:1.9.0 G.01  ", 0, true},
	}
	for _, c := range cases {
		slot, ok := consumerSlotForCode(c.code)
		if ok != c.ok || (ok && slot != c.slot) {
			t.Errorf("consumerSlotForCode(%q) = (%d,%v), want (%d,%v)",
				c.code, slot, ok, c.slot, c.ok)
		}
	}
}

func TestProducerSlotForCode(t *testing.T) {
	cases := []struct {
		code string
		slot int
		ok   bool
	}{
		// Standard.
		{"1-1:2.9.0 G.01", 0, true},
		{"1-1:2.9.0 P.01", 1, true},
		// Mehrfachteilnahme-Varianten.
		{"1-1:2.9.0 G.01T", 0, true},
		{"1-1:2.9.0 P.01T", 1, true},
		// Unbekannt + Consumer-Codes → false.
		{"1-1:2.9.0 X.99", 0, false},
		{"1-1:1.9.0 G.01", 0, false}, // Consumer-Verbrauch ist hier ungültig
		{"", 0, false},
		// Whitespace-Toleranz.
		{"\t1-1:2.9.0 P.01\n", 1, true},
	}
	for _, c := range cases {
		slot, ok := producerSlotForCode(c.code)
		if ok != c.ok || (ok && slot != c.slot) {
			t.Errorf("producerSlotForCode(%q) = (%d,%v), want (%d,%v)",
				c.code, slot, ok, c.slot, c.ok)
		}
	}
}
