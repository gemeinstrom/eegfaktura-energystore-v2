package excelexport

import (
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// SummarySheet builds the "Summary" sheet — overall consumption /
// generation totals + per-CP rows. Mirrors v1 excel/SummarySheet.go.
type SummarySheet struct {
	name             string
	excel            *excelize.File
	consumed         []float64
	allocated        []float64
	shared           []float64
	produced         []float64
	distributed      []float64
	qovConsumerSlice []bool
	qovProducerSlice []bool
}

func (ss *SummarySheet) initSheet(ctx *runnerContext) error {
	ss.consumed = make([]float64, ctx.info.ConsumerCount)
	ss.allocated = make([]float64, ctx.info.ConsumerCount)
	ss.shared = make([]float64, ctx.info.ConsumerCount)
	ss.produced = make([]float64, ctx.info.ProducerCount)
	ss.distributed = make([]float64, ctx.info.ProducerCount)
	ss.qovConsumerSlice = boolSlice(ctx.info.ConsumerCount, true)
	ss.qovProducerSlice = boolSlice(ctx.info.ProducerCount, true)

	_, err := ss.excel.NewSheet(ss.name)
	return err
}

func (ss *SummarySheet) handleLine(ctx *runnerContext, line *queryengine.RawSourceLine) error {
	lineDate, _ := parseRowTime(line.ID)

	// v1's ConvertLineToMatrix turns the flat slices into (n×3) and
	// (m×2) matrices. We work with the flat indices directly — the
	// math is identical.
	for i := 0; i < ctx.info.ConsumerCount; i++ {
		base := i * 3
		if base+2 < len(line.Consumers) {
			ss.consumed[i] += line.Consumers[base]
			ss.shared[i] += line.Consumers[base+1]
			ss.allocated[i] += line.Consumers[base+2]
		}
		if base+2 < len(line.QoVConsumers) {
			startTS := ctx.periodsConsumer[i].start
			before := ctx.checkBegin(lineDate, startTS)
			allOK := line.QoVConsumers[base] == 1 && line.QoVConsumers[base+1] == 1 && line.QoVConsumers[base+2] == 1
			ss.qovConsumerSlice[i] = ss.qovConsumerSlice[i] && (before || allOK)
		}
	}
	for i := 0; i < ctx.info.ProducerCount; i++ {
		base := i * 2
		if base+1 < len(line.Producers) {
			ss.produced[i] += line.Producers[base]
			ss.distributed[i] += line.Producers[base+1]
		}
		if base+1 < len(line.QoVProducers) {
			startTS := ctx.periodsProducer[i].start
			before := ctx.checkBegin(lineDate, startTS)
			allOK := line.QoVProducers[base] == 1 && line.QoVProducers[base+1] == 1
			ss.qovProducerSlice[i] = ss.qovProducerSlice[i] && (before || allOK)
		}
	}
	return nil
}

func (ss *SummarySheet) closeSheet(ctx *runnerContext) error {
	f := ss.excel
	styleID, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Size: 10.0}})
	styleBold, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Size: 10.0, Bold: true},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	styleSummary, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Size: 10.0},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	styleHeader, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	styleGood, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Size: 10.0},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"00a933"}, Pattern: 1},
	})
	styleBad, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Size: 10.0},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"ff4000"}, Pattern: 1},
	})
	qovStyle := map[bool]int{true: styleGood, false: styleBad}

	sw, err := f.NewStreamWriter(ss.name)
	if err != nil {
		return err
	}

	begin := time.Date(ctx.start.Year(), ctx.start.Month(), ctx.start.Day(), 0, 0, 0, 0, time.Local)
	end := time.Date(ctx.end.Year(), ctx.end.Month(), ctx.end.Day(), 23, 45, 0, 0, time.Local)

	_ = sw.SetColWidth(1, 1, 40)
	_ = sw.SetColWidth(2, 2, 35)
	_ = sw.SetColWidth(3, 4, 22)
	_ = sw.SetColWidth(5, 5, 15)
	_ = sw.SetColWidth(6, 10, 22)

	rowOpts := excelize.RowOpts{StyleID: styleSummary}
	cps := ss.summarize(ctx)

	_ = sw.SetRow("A2", []any{
		excelize.Cell{Value: "Gemeinschafts-ID", StyleID: styleBold},
		excelize.Cell{Value: ctx.cps.CommunityID}}, rowOpts)
	_ = sw.SetRow("A3", []any{
		excelize.Cell{Value: "Zeitraum von", StyleID: styleBold},
		excelize.Cell{Value: dateToString(begin)}}, rowOpts)
	_ = sw.SetRow("A4", []any{
		excelize.Cell{Value: "Zeitraum bis", StyleID: styleBold},
		excelize.Cell{Value: dateToString(end)}}, rowOpts)
	_ = sw.SetRow("A5", []any{
		excelize.Cell{Value: "Gesamtverbrauch lt. Messung (bei Teilnahme gem. Erzeugung) [KWH]", StyleID: styleBold},
		excelize.Cell{Value: sumOver(cps.Consumer, func(e *SummaryMeterResult) float64 { return e.Total })},
	}, excelize.RowOpts{StyleID: styleSummary, Height: 0.34 * 72})
	_ = sw.SetRow("A6", []any{
		excelize.Cell{Value: "Anteil gemeinschaftliche Erzeugung [KWH]", StyleID: styleBold},
		excelize.Cell{Value: sumOver(cps.Consumer, func(e *SummaryMeterResult) float64 { return e.Coverage })},
	}, rowOpts)
	_ = sw.SetRow("A7", []any{
		excelize.Cell{Value: "Eigendeckung gemeinschaftliche Erzeugung [KWH]", StyleID: styleBold},
		excelize.Cell{Value: sumOver(cps.Consumer, func(e *SummaryMeterResult) float64 { return e.Share })},
	}, excelize.RowOpts{StyleID: styleSummary, Height: 0.34 * 72})
	_ = sw.SetRow("A8", []any{
		excelize.Cell{Value: "Gesamt/Überschusserzeugung, Gemeinschaftsüberschuss [KWH]", StyleID: styleBold},
		excelize.Cell{Value: sumOver(cps.Producer, func(e *SummaryMeterResult) float64 { return e.Share })},
	}, excelize.RowOpts{StyleID: styleSummary, Height: 0.34 * 72})
	_ = sw.SetRow("A9", []any{
		excelize.Cell{Value: "Gesamte gemeinschaftliche Erzeugung [KWH]", StyleID: styleBold},
		excelize.Cell{Value: sumOver(cps.Producer, func(e *SummaryMeterResult) float64 { return e.Total })},
	}, rowOpts)

	line := 12
	_ = sw.SetRow(fmt.Sprintf("A%d", line), []any{
		excelize.Cell{Value: "Verbrauchszählpunkt"},
		excelize.Cell{Value: "Name"},
		excelize.Cell{Value: "Beginn der Daten"},
		excelize.Cell{Value: "Ende der Daten"},
		excelize.Cell{Value: "Daten vollständig? Ja/Nein"},
		excelize.Cell{Value: "Gesamtverbrauch lt. Messung (bei Teilnahme gem. Erzeugung) [KWH]"},
		excelize.Cell{Value: "Anteil gemeinschaftliche Erzeugung [KWH]"},
		excelize.Cell{Value: "Eigendeckung gemeinschaftliche Erzeugung [KWH]"},
	}, excelize.RowOpts{StyleID: styleHeader, Height: 1.15 * 72})

	for _, c := range cps.Consumer {
		line++
		_ = sw.SetRow(fmt.Sprintf("A%d", line), []any{
			excelize.Cell{Value: c.MeteringPoint},
			excelize.Cell{Value: c.Name},
			excelize.Cell{Value: c.BeginDate},
			excelize.Cell{Value: c.EndDate},
			excelize.Cell{Value: c.DataOK, StyleID: qovStyle[c.DataOK]},
			excelize.Cell{Value: roundTo6(c.Total)},
			excelize.Cell{Value: roundTo6(c.Coverage)},
			excelize.Cell{Value: roundTo6(c.Share)},
		}, excelize.RowOpts{StyleID: styleID})
	}

	line += 3
	_ = sw.SetRow(fmt.Sprintf("A%d", line), []any{
		excelize.Cell{Value: "Einspeisezählpunkt"},
		excelize.Cell{Value: "Name"},
		excelize.Cell{Value: "Beginn der Daten"},
		excelize.Cell{Value: "Ende der Daten"},
		excelize.Cell{Value: "Daten vollständig? Ja/Nein"},
		excelize.Cell{Value: "Gesamt/Überschusserzeugung, Gemeinschaftsüberschuss [KWH]"},
		excelize.Cell{Value: "Gesamte gemeinschaftliche Erzeugung [KWH]"},
		excelize.Cell{Value: "Eigendeckung gemeinschaftliche Erzeugung [KWH]"},
	}, excelize.RowOpts{StyleID: styleHeader, Height: 1.15 * 72})

	for _, c := range cps.Producer {
		line++
		_ = sw.SetRow(fmt.Sprintf("A%d", line), []any{
			excelize.Cell{Value: c.MeteringPoint},
			excelize.Cell{Value: c.Name},
			excelize.Cell{Value: c.BeginDate},
			excelize.Cell{Value: c.EndDate},
			excelize.Cell{Value: c.DataOK, StyleID: qovStyle[c.DataOK]},
			excelize.Cell{Value: roundTo6(c.Share)},
			excelize.Cell{Value: roundTo6(c.Total)},
			excelize.Cell{Value: roundTo6(c.Coverage)},
		}, excelize.RowOpts{StyleID: styleID})
	}
	return sw.Flush()
}

func (ss *SummarySheet) summarize(ctx *runnerContext) *SummaryResult {
	out := &SummaryResult{}
	for _, cp := range ctx.cps.Cps {
		m, ok := ctx.metaMap[cp.MeteringPoint]
		if !ok {
			continue
		}
		begin, end := "", ""
		if m.PeriodStart != nil {
			begin = dateToString(*m.PeriodStart)
		}
		if m.PeriodEnd != nil {
			end = dateToString(*m.PeriodEnd)
		}
		if cp.Direction == "CONSUMPTION" {
			out.Consumer = append(out.Consumer, SummaryMeterResult{
				MeteringPoint: cp.MeteringPoint,
				Name:          cp.Name,
				BeginDate:     begin,
				EndDate:       end,
				DataOK:        safeBool(ss.qovConsumerSlice, m.SourceIdx, true),
				Total:         returnFloat(ss.consumed, m.SourceIdx),
				Coverage:      returnFloat(ss.shared, m.SourceIdx),
				Share:         returnFloat(ss.allocated, m.SourceIdx),
			})
		} else {
			producedV := returnFloat(ss.produced, m.SourceIdx)
			distributedV := returnFloat(ss.distributed, m.SourceIdx)
			out.Producer = append(out.Producer, SummaryMeterResult{
				MeteringPoint: cp.MeteringPoint,
				Name:          cp.Name,
				BeginDate:     begin,
				EndDate:       end,
				DataOK:        safeBool(ss.qovProducerSlice, m.SourceIdx, true),
				Total:         producedV,
				Coverage:      producedV - distributedV,
				Share:         distributedV,
			})
		}
	}
	return out
}

// helpers

func sumOver(s []SummaryMeterResult, get func(*SummaryMeterResult) float64) float64 {
	var total float64
	for i := range s {
		total += get(&s[i])
	}
	return roundTo(total, 6)
}

func boolSlice(n int, v bool) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func safeBool(s []bool, idx int, fallback bool) bool {
	if idx < 0 || idx >= len(s) {
		return fallback
	}
	return s[idx]
}

// parseRowTime parses queryengine row IDs ("CP/Y/M/D/h/m") in local TZ.
func parseRowTime(id string) (time.Time, error) {
	var prefix string
	var yr, mo, dy, hr, mn int
	n, err := fmt.Sscanf(id, "%2s/%04d/%02d/%02d/%02d/%02d",
		&prefix, &yr, &mo, &dy, &hr, &mn)
	if err != nil || n != 6 || prefix != "CP" {
		return time.Time{}, fmt.Errorf("excelexport: bad row id %q", id)
	}
	return time.Date(yr, time.Month(mo), dy, hr, mn, 0, 0, time.Local), nil
}
