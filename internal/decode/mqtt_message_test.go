package decode

import (
	"testing"
	"time"
)

func TestDecodeSlots_v1FixtureRoundTrip(t *testing.T) {
	// Fixture from v1 model/mqtt_test.go (one slot, EcId added because v1's
	// fixture omits it; v2 requires it).
	payload := []byte(`{
        "message": {
            "messageId": "AT003000202211111446152980115933630",
            "meter": {"meteringPoint": "AT003000000000000000000000012345"},
            "ecId": "TE100200",
            "energy": {
                "data": [{
                    "start": 1667948400000,
                    "end":   1668034800000,
                    "interval": "QH",
                    "nInterval": 288,
                    "meterCode": "1-1:1.9.0 G.01",
                    "value": [
                        {"from": 1667948400000, "to": 1667949300000, "method": "L1", "value": 0.118},
                        {"from": 1667949300000, "to": 1667950200000, "method": "L2", "value": 0.130}
                    ]
                }]
            }
        }
    }`)

	slots, err := DecodeSlots("vfeeg", payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	want := time.UnixMilli(1667948400000).UTC()
	if !slots[0].Timestamp.Equal(want) {
		t.Fatalf("slot[0].Timestamp = %v, want %v", slots[0].Timestamp, want)
	}
	if slots[0].TenantID != "vfeeg" || slots[0].ECID != "TE100200" {
		t.Fatalf("tenant/ec mismatch: %+v", slots[0])
	}
	if slots[0].MeteringPoint != "AT003000000000000000000000012345" {
		t.Fatalf("metering point mismatch: %q", slots[0].MeteringPoint)
	}
	if slots[0].MeterCode != "1-1:1.9.0 G.01" {
		t.Fatalf("expected raw OBIS pass-through, got %q", slots[0].MeterCode)
	}
	if slots[0].QoV != 1 {
		t.Fatalf("L1 → qov 1 (measured), got %d", slots[0].QoV)
	}
	if slots[1].QoV != 2 {
		t.Fatalf("L2 → qov 2 (replaced), got %d", slots[1].QoV)
	}
	if slots[1].Value != 0.130 {
		t.Fatalf("value: %v", slots[1].Value)
	}
}

func TestDecodeSlots_MissingTenant(t *testing.T) {
	payload := []byte(`{"message":{"meter":{"meteringPoint":"AT001"},"ecId":"E1","energy":{"data":[]}}}`)
	// Tenant comes from the caller, so empty is allowed at this layer.
	if _, err := DecodeSlots("", payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeSlots_MissingMeteringPoint(t *testing.T) {
	payload := []byte(`{"message":{"meter":{"meteringPoint":""},"ecId":"E1","energy":{"data":[]}}}`)
	if _, err := DecodeSlots("vfeeg", payload); err == nil {
		t.Fatal("expected error for missing meteringPoint")
	}
}

func TestDecodeSlots_MissingMeterCode(t *testing.T) {
	payload := []byte(`{"message":{"meter":{"meteringPoint":"AT001"},"ecId":"E1","energy":{"data":[{"meterCode":"","value":[]}]}}}`)
	if _, err := DecodeSlots("vfeeg", payload); err == nil {
		t.Fatal("expected error for missing meterCode")
	}
}

// TestQoVForMethod pins the v1-parity mapping (L1=1 measured, L2=2
// replaced, L3=3 estimated, unknown=0). Earlier v2 versions had the
// mapping shifted by one (L1=0) which made every measured MQTT slot
// land in the DB with qov=0 → empty Excel cells downstream.
func TestQoVForMethod(t *testing.T) {
	cases := map[string]int16{"L1": 1, "L2": 2, "L3": 3, "": 0, "weird": 0}
	for in, want := range cases {
		if got := QoVForMethod(in); got != want {
			t.Errorf("QoVForMethod(%q) = %d, want %d", in, got, want)
		}
	}
}
