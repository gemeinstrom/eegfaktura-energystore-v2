// Package decode parses MQTT payloads delivered by the EDA-bridge (Ponton
// / eda-comm) into a flat []store.Slot ready for UpsertSlots.
//
// Payload shape is taken from v1 (model/mqtt.go in eegfaktura-energystore).
// Kept binary-compatible so v2 can subscribe to the same broker topics as v1.
package decode

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

// MqttEnergyValue is one quarter-hour slot.
type MqttEnergyValue struct {
	From   int64   `json:"from"`   // epoch-ms, inclusive
	To     int64   `json:"to"`     // epoch-ms, exclusive
	Method string  `json:"method"` // L1 / L2 / ... — quality marker, ignored here
	Value  float64 `json:"value"`
}

// MqttEnergyData carries one OBIS code's series for a metering point.
type MqttEnergyData struct {
	MeterCode string            `json:"meterCode"`
	Value     []MqttEnergyValue `json:"value"`
}

// MqttEnergy is the per-message payload section.
type MqttEnergy struct {
	Start int64            `json:"start"`
	End   int64            `json:"end"`
	Data  []MqttEnergyData `json:"data"`
}

// EnergyMeter identifies the metering point.
type EnergyMeter struct {
	MeteringPoint string `json:"meteringPoint"`
	Direction     string `json:"direction,omitempty"`
}

// MqttEnergyMessage is the inner message; matches v1 schema exactly.
type MqttEnergyMessage struct {
	Meter  EnergyMeter `json:"meter"`
	Energy MqttEnergy  `json:"energy"`
	EcID   string      `json:"ecId"`
}

// MqttEnergyResponse is the outer envelope sent by the broker.
type MqttEnergyResponse struct {
	Message MqttEnergyMessage `json:"message"`
}

// QoVForMethod maps the v1 quality marker to v2's qov SMALLINT column.
//
// Mirrors v1 utils.CastQoVStringToInt (calculation/utils/counterpoint.go:70):
//
//	"L1" → 1  measured  (Netzbetreiber-Echtwert; the dominant case in prod)
//	"L2" → 2  replaced  (rendered yellow in Excel)
//	"L3" → 3  estimated/interpolated (rendered red in Excel)
//	other → 0 unknown   (treated as "no value" by the Excel renderer —
//	                    sheet_helpers.go:setCell falls through to an
//	                    empty cell, which is the correct semantic for
//	                    a slot whose quality marker we can't trust.)
//
// Earlier v2 versions emitted 0/1/2/9 here, which made every measured
// slot land in the DB with qov=0 → every downstream Excel-Export came
// out empty (see pilot 2026-06-06 incident).
func QoVForMethod(method string) int16 {
	switch method {
	case "L1":
		return 1
	case "L2":
		return 2
	case "L3":
		return 3
	default:
		return 0
	}
}

// MessageHeader carries the meter-identifying fields out of the MQTT
// payload. We need it alongside the Slots so the MQTT-Ingest path
// can auto-upsert `counterpoint_meta` on first sight of an
// (tenant, ec, meteringPoint) triple — the bare Slot loses the
// Direction. See gemeinstrom/eegfaktura-energystore-v2#feedback_counterpoint_meta_only_xlsximport
// for the background.
type MessageHeader struct {
	TenantID      string
	ECID          string
	MeteringPoint string
	Direction     string // CONSUMPTION / GENERATION (or empty if absent)
}

// DecodeMessage decodes the MQTT payload into a MessageHeader plus
// the flat slice of Slots. Use this when the caller also needs the
// meta-info (e.g. counterpoint-auto-upsert path); use DecodeSlots
// when only the time-series rows are needed.
func DecodeMessage(tenant string, payload []byte) (MessageHeader, []store.Slot, error) {
	var resp MqttEnergyResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return MessageHeader{}, nil, fmt.Errorf("decode: unmarshal: %w", err)
	}
	msg := resp.Message
	if msg.Meter.MeteringPoint == "" {
		return MessageHeader{}, nil, fmt.Errorf("decode: missing meteringPoint")
	}
	if msg.EcID == "" {
		return MessageHeader{}, nil, fmt.Errorf("decode: missing ecId")
	}

	hdr := MessageHeader{
		TenantID:      tenant,
		ECID:          msg.EcID,
		MeteringPoint: msg.Meter.MeteringPoint,
		Direction:     msg.Meter.Direction,
	}
	out := make([]store.Slot, 0, sliceLenHint(msg.Energy.Data))
	for _, series := range msg.Energy.Data {
		if series.MeterCode == "" {
			return MessageHeader{}, nil, fmt.Errorf("decode: missing meterCode in data entry")
		}
		for _, v := range series.Value {
			out = append(out, store.Slot{
				TenantID:      tenant,
				ECID:          msg.EcID,
				MeteringPoint: msg.Meter.MeteringPoint,
				MeterCode:     series.MeterCode,
				Timestamp:     time.UnixMilli(v.From).UTC(),
				Value:         v.Value,
				QoV:           QoVForMethod(v.Method),
			})
		}
	}
	return hdr, out, nil
}

// DecodeSlots turns an MQTT payload into a flat slice of store.Slot.
// Backward-compat wrapper over DecodeMessage — keeps the existing
// callers (mainly tests) working without change.
//
// `tenant` is taken from the broker context (typically the topic structure
// `eegfaktura/<tenant>/energy/<...>`) because the v1 message envelope does
// not carry it. The caller (subscriber callback) is responsible for
// extracting it from the topic before calling Decode.
func DecodeSlots(tenant string, payload []byte) ([]store.Slot, error) {
	_, slots, err := DecodeMessage(tenant, payload)
	return slots, err
}

func sliceLenHint(data []MqttEnergyData) int {
	n := 0
	for _, d := range data {
		n += len(d.Value)
	}
	return n
}
