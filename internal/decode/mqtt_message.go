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
// 0 = raw (L1), 1 = replaced (L2), 2 = interpolated, 9 = unknown.
func QoVForMethod(method string) int16 {
	switch method {
	case "L1":
		return 0
	case "L2":
		return 1
	case "L3":
		return 2
	default:
		return 9
	}
}

// DecodeSlots turns an MQTT payload into a flat slice of store.Slot.
//
// `tenant` is taken from the broker context (typically the topic structure
// `eegfaktura/<tenant>/energy/<...>`) because the v1 message envelope does
// not carry it. The caller (subscriber callback) is responsible for
// extracting it from the topic before calling Decode.
func DecodeSlots(tenant string, payload []byte) ([]store.Slot, error) {
	var resp MqttEnergyResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("decode: unmarshal: %w", err)
	}
	msg := resp.Message
	if msg.Meter.MeteringPoint == "" {
		return nil, fmt.Errorf("decode: missing meteringPoint")
	}
	if msg.EcID == "" {
		return nil, fmt.Errorf("decode: missing ecId")
	}

	out := make([]store.Slot, 0, sliceLenHint(msg.Energy.Data))
	for _, series := range msg.Energy.Data {
		if series.MeterCode == "" {
			return nil, fmt.Errorf("decode: missing meterCode in data entry")
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
	return out, nil
}

func sliceLenHint(data []MqttEnergyData) int {
	n := 0
	for _, d := range data {
		n += len(d.Value)
	}
	return n
}
