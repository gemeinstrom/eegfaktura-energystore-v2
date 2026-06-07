package queryengine

import "strings"

// OBIS-code → wide-array slot offset within a (consumer or producer)
// metering point's source_idx block.
//
// Consumer block (size 3 per CP):
//
//	slot 0: Consumed           — "1-1:1.9.0 G.01"
//	                           — "1-1:1.9.0 G.01T" (Teilnahmefaktor-bereinigt; v1-Verhalten: gleicher Slot, last-write-wins)
//	slot 1: Allocated          — "1-1:2.9.0 G.02"
//	slot 2: Distributed (Cover)— "1-1:2.9.0 G.03"
//	                           — "1-1:2.9.0 G.03R" (Renewable-Anteil; v1-Verhalten: gleicher Slot, last-write-wins)
//
// Producer block (size 2 per CP):
//
//	slot 0: Produced (Generated)  — "1-1:2.9.0 G.01"
//	                              — "1-1:2.9.0 G.01T" (Teilnahmefaktor-bereinigt; v1-Verhalten: gleicher Slot)
//	slot 1: Distributed (Overage) — "1-1:2.9.0 P.01"
//	                              — "1-1:2.9.0 P.01T" (Restnetz-Überschuss; v1-Verhalten: gleicher Slot)
//
// Returns ok=false für nicht erkannte Codes; der Loader silentdroppt sie.
//
// OBIS-EXTENSION-POINT (ELWG Q4 2026):
// Bei Bedarf für ELWG-Erweiterungen (Mehrfachteilnahme, Peer-to-Peer,
// Eigenversorgung über mehrere ZP) müssen die T-/R-Varianten ggf. in
// eigene Slots (Wide-Schema-Expansion 3→5 / 2→4) statt last-write-wins.
// Ebenso wenn der Netzbetreiber Speicher-Codes oder weitere ELWG-
// Varianten einführt. Siehe ADR-0010 + Issue #XX (Phase-2-Refactor).
// Die Stellen die dann zusammen geändert werden müssen:
//   - queryengine/codes.go     (this file — slot mapping)
//   - queryengine/loader.go    (consumerCount*3 / producerCount*2)
//   - queryengine/engine.go    (cache init)
//   - excelexport/sheet_helpers.go (addHeaderV2 cellCon/cellProd)
//   - excelexport/energy_sheet.go  (Metercode-Header-Spalten)
//   - calc/participant.go      (IntermediateRecord-Felder + Allokation)
//   - eegfaktura-web models/energy.model.ts (Frontend-Mirror-Struct)
func consumerSlotForCode(code string) (int, bool) {
	c := normalizeOBIS(code)
	switch c {
	case "1-1:1.9.0 G.01", "1-1:1.9.0 G.01T":
		return 0, true
	case "1-1:2.9.0 G.02":
		return 1, true
	case "1-1:2.9.0 G.03", "1-1:2.9.0 G.03R":
		return 2, true
	}
	return 0, false
}

func producerSlotForCode(code string) (int, bool) {
	c := normalizeOBIS(code)
	switch c {
	case "1-1:2.9.0 G.01", "1-1:2.9.0 G.01T":
		return 0, true
	case "1-1:2.9.0 P.01", "1-1:2.9.0 P.01T":
		return 1, true
	}
	return 0, false
}

func normalizeOBIS(s string) string {
	return strings.TrimSpace(s)
}
