package queryengine

import "strings"

// OBIS-code → wide-array slot offset within a (consumer or producer)
// metering point's source_idx block. Mirrors the v1 const set in
// model/counterpoint.go.
//
// Consumer block (size 3 per CP):
//
//	slot 0: Consumed   — "1-1:1.9.0 G.01"
//	slot 1: Allocated  — "1-1:2.9.0 G.02"
//	slot 2: Distributed (Cover) — "1-1:2.9.0 G.03"
//
// Producer block (size 2 per CP):
//
//	slot 0: Produced (Generated)  — "1-1:2.9.0 G.01"
//	slot 1: Distributed (Overage) — "1-1:2.9.0 P.01"
//
// Returns ok=false for unmapped codes; the loader silently drops them
// (T-variants like "G.01T" / "G.03R" are not part of the standard
// netzbetreiber payload — same v1 behaviour).
func consumerSlotForCode(code string) (int, bool) {
	c := normalizeOBIS(code)
	switch c {
	case "1-1:1.9.0 G.01":
		return 0, true
	case "1-1:2.9.0 G.02":
		return 1, true
	case "1-1:2.9.0 G.03":
		return 2, true
	}
	return 0, false
}

func producerSlotForCode(code string) (int, bool) {
	c := normalizeOBIS(code)
	switch c {
	case "1-1:2.9.0 G.01":
		return 0, true
	case "1-1:2.9.0 P.01":
		return 1, true
	}
	return 0, false
}

func normalizeOBIS(s string) string {
	return strings.TrimSpace(s)
}
