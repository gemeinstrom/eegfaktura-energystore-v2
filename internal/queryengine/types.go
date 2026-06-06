// Package queryengine ports v1's store/{engine,query_engine,*_function}.go
// to the v2 long-schema. Wire-identical RawSourceLine / RawData / ReportData
// so the REST layer (workstream G) and the calculation layer (workstream E)
// can stay byte-compatible with v1.
package queryengine

import "github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"

// RawSourceLine is the per-timestamp wide-array view that v1 emits to
// every report function. Each consumer occupies 3 slots in Consumers
// (Consumed, Allocated/Share, Distributed/Cover). Each producer occupies
// 2 slots in Producers (Produced/Generated, Distributed/Overage). Order
// within each slice is source_idx — counterpoint.ListByEC returns
// consumers + producers already sorted.
type RawSourceLine struct {
	ID           string    `json:"id"`
	Consumers    []float64 `json:"consumers"`
	Producers    []float64 `json:"producers"`
	QoVConsumers []int     `json:"qovconsumers"`
	QoVProducers []int     `json:"qovproducers"`
}

// Copy produces a same-length deep copy.
func (l RawSourceLine) Copy() RawSourceLine {
	r := RawSourceLine{
		ID:           l.ID,
		Consumers:    make([]float64, len(l.Consumers)),
		Producers:    make([]float64, len(l.Producers)),
		QoVConsumers: make([]int, len(l.QoVConsumers)),
		QoVProducers: make([]int, len(l.QoVProducers)),
	}
	copy(r.Consumers, l.Consumers)
	copy(r.Producers, l.Producers)
	copy(r.QoVConsumers, l.QoVConsumers)
	copy(r.QoVProducers, l.QoVProducers)
	return r
}

// DeepCopy reshapes to (nConsumer*3, nProducer*2) — matches the v1 signature
// used by the Cache helper.
func (l RawSourceLine) DeepCopy(nConsumer, nProducer int) RawSourceLine {
	r := RawSourceLine{
		ID:           l.ID,
		Consumers:    make([]float64, nConsumer*3),
		Producers:    make([]float64, nProducer*2),
		QoVConsumers: make([]int, nConsumer*3),
		QoVProducers: make([]int, nProducer*2),
	}
	copy(r.Consumers, l.Consumers)
	copy(r.Producers, l.Producers)
	copy(r.QoVConsumers, l.QoVConsumers)
	copy(r.QoVProducers, l.QoVProducers)
	return r
}

// makeRawSourceLine allocates a zero-filled line of the right width.
func makeRawSourceLine(id string, consumerSize, producerSize int) *RawSourceLine {
	return &RawSourceLine{
		ID:           id,
		Consumers:    make([]float64, consumerSize),
		Producers:    make([]float64, producerSize),
		QoVConsumers: make([]int, consumerSize),
		QoVProducers: make([]int, producerSize),
	}
}

// ReportData is the v1 wire shape returned by report endpoints. Field
// order + `omitempty` decisions are load-bearing — see v1 query_engine.go
// for the parity-gap commentary.
type ReportData struct {
	Consumed    float64 `json:"consumed"`
	Allocated   float64 `json:"allocated"`
	Distributed float64 `json:"distributed"`
	Produced    float64 `json:"produced"`
	Unused      float64 `json:"unused"`
	QoVConsumer int     `json:"qoVConsumer"`
	QoVProducer int     `json:"qoVProducer"`
	CntProducer int     `json:"cntProducer"`
	CntConsumer int     `json:"cntConsumer"`
	Name        string  `json:"name,omitempty"`
}

// RawData is one slot for /raw, /aggregate, /default. Value/Qov widths
// are 3 for consumers, 2 for producers — matches v1.
type RawData struct {
	Ts    int64     `json:"ts"`
	Value []float64 `json:"value"`
	Qov   []int     `json:"qov"`
}

// RawDataResult bundles a metering point's RawData stream + its direction.
type RawDataResult struct {
	Data      []RawData                `json:"data"`
	Direction counterpoint.Direction   `json:"direction"`
}

// TargetMP selects which metering points the request asks about.
// JSON-wire-compatible with v1.
type TargetMP struct {
	MeteringPoint string `json:"meteringPoint"`
}

// MetaData is the per-MP period summary returned by /meta. Same shape as v1.
type MetaData struct {
	PeriodBegin int64 `json:"periodBegin"`
	PeriodEnd   int64 `json:"periodEnd"`
}

// CounterPointMetaInfo summarises a tenant+ec's CP topology. v1 uses this
// in calculation; v2 keeps the same shape so the calc port lands cleanly.
type CounterPointMetaInfo struct {
	ConsumerCount  int
	ProducerCount  int
	MaxConsumerIdx int
	MaxProducerIdx int
}

// calcQoV mirrors v1: prefer higher-than-1 numbers; once you hit 1 stay
// at 1 unless the new value is also != 1. This codifies the "raw beats
// substitute beats interpolated" preference.
func calcQoV(current, target int) int {
	if current != 1 {
		if target > current && target != 1 {
			return target
		}
		return current
	}
	return target
}

// initIntSlice fills a slice with the same value.
func initIntSlice(v int, in []int) []int {
	for i := range in {
		in[i] = v
	}
	return in
}
