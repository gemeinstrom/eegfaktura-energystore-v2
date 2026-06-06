// Package excelexport ports v1 excel/{EnergyExport,EnergySheet,SummarySheet,
// QoVSheet}.go (~1700 LoC) to the v2 long-schema. The xlsx file layout is
// byte-compatible with v1 — column widths, sheet names, header rows,
// number formats, QoV coloring all preserved — because operators may
// store / archive these reports verbatim.
//
// Architecture mirrors v1:
//   - ExportCPs is the per-request input: time range + community ID + CPs
//   - RunnerContext is the per-export state shared across sheets
//   - Sheet interface: initSheet + handleLine + closeSheet
//   - EnergyRunner walks RawSourceLines via queryengine.Engine.Query and
//     dispatches each line to every sheet
package excelexport

import (
	"math"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// ExportCPs is the v1-shape request body for /eeg/{ecid}/excel/report/download.
type ExportCPs struct {
	Start       int64            `json:"start"`
	End         int64            `json:"end"`
	CommunityID string           `json:"communityId"`
	Cps         []InvestigatorCP `json:"cps"`
}

// InvestigatorCP names one metering point + display name + direction.
type InvestigatorCP struct {
	MeteringPoint string `json:"meteringPoint"`
	Direction     string `json:"direction"`
	Name          string `json:"name"`
}

// SummaryMeterResult holds the aggregated numbers per CP used by the
// Summary sheet. Wire-identical to v1.
type SummaryMeterResult struct {
	MeteringPoint string
	Name          string
	BeginDate     string
	EndDate       string
	DataOK        bool
	Total         float64
	Coverage      float64
	Share         float64
}

// SummaryResult bundles consumer + producer summary rows.
type SummaryResult struct {
	Consumer []SummaryMeterResult
	Producer []SummaryMeterResult
}

// runnerContext is the per-export state. Fields mirror v1 RunnerContext.
type runnerContext struct {
	start, end time.Time
	cps        *ExportCPs

	metaMap         map[string]*counterpoint.CounterPoint
	meta            []counterpoint.CounterPoint // consumers then producers, sorted
	info            *queryengine.CounterPointMetaInfo
	countCons       int
	countProd       int
	periodsConsumer map[int]periodRange
	periodsProducer map[int]periodRange
	qovLogArray     []queryengine.RawSourceLine
	checkBegin      func(lineDate, mDate time.Time) bool
}

type periodRange struct {
	start time.Time
	end   time.Time
}

func (c *runnerContext) getPeriodRange(m *counterpoint.CounterPoint) periodRange {
	if m.Direction == counterpoint.DirectionConsumer {
		return c.periodsConsumer[m.SourceIdx]
	}
	return c.periodsProducer[m.SourceIdx]
}

// returnFloat returns array[idx] or 0 if out of bounds. Mirrors v1
// returnFloatValue. Shared by the sheets.
func returnFloat(a []float64, idx int) float64 {
	if idx < 0 || idx >= len(a) {
		return 0
	}
	return a[idx]
}

// roundTo6 quantises to 6 decimals. v1 utils.RoundToFixed equivalent.
func roundTo6(v float64) float64 {
	return roundTo(v, 6)
}

func roundTo(v float64, precision uint) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(v*ratio) / ratio
}
