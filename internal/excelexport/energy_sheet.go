package excelexport

import (
	"fmt"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// EnergySheet renders the "Energiedaten" sheet: 7 header rows + one
// row per 15-min slot. Mirrors v1 excel/EnergySheet.go.
type EnergySheet struct {
	name      string
	excel     *excelize.File
	stylesQoV []int
	writer    *excelize.StreamWriter
	lineNum   int
}

func (es *EnergySheet) initSheet(ctx *runnerContext) error {
	f := es.excel
	if _, err := f.NewSheet(es.name); err != nil {
		return err
	}

	participantMeterMap := map[string]string{}
	for _, m := range ctx.cps.Cps {
		participantMeterMap[m.MeteringPoint] = m.Name
	}

	styleL3, _ := f.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Color: []string{"ff5429"}, Pattern: 1}})
	styleL2, _ := f.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Color: []string{"FFFF00"}, Pattern: 1}})
	numFmt := "#,##0.000000"
	styleNum, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &numFmt})
	es.stylesQoV = []int{styleNum, styleL2, styleL3}

	sw, err := f.NewStreamWriter(es.name)
	if err != nil {
		return err
	}
	es.writer = sw

	_ = sw.SetColWidth(1, 1, 30)
	_ = sw.SetColWidth(2, 1000, 25)

	_ = sw.SetRow("A2", append([]any{excelize.Cell{Value: "MeteringpointID"}},
		addHeaderV2(ctx, 3, 2,
			func(m *counterpoint.CounterPoint, _ int) any { return m.MeteringPoint },
			func(_ *counterpoint.CounterPoint, _ int) int { return 0 })...))

	_ = sw.SetRow("A3", append([]any{excelize.Cell{Value: "Name"}},
		addHeaderV2(ctx, 3, 2,
			func(m *counterpoint.CounterPoint, _ int) any {
				if p, ok := participantMeterMap[m.MeteringPoint]; ok {
					return p
				}
				return m.Name
			},
			func(_ *counterpoint.CounterPoint, _ int) int { return 0 })...))

	_ = sw.SetRow("A4", append([]any{excelize.Cell{Value: "Energy direction"}},
		addHeaderV2(ctx, 3, 2,
			func(m *counterpoint.CounterPoint, _ int) any { return m.Direction.String() },
			func(_ *counterpoint.CounterPoint, _ int) int { return 0 })...))

	sYr, sMo, sDy := ctx.start.Year(), int(ctx.start.Month()), ctx.start.Day()
	eYr, eMo, eDy := ctx.end.Year(), int(ctx.end.Month()), ctx.end.Day()

	_ = sw.SetRow("A5", append([]any{excelize.Cell{Value: "Period start"}},
		addHeaderV2(ctx, 3, 2,
			func(m *counterpoint.CounterPoint, _ int) any {
				d := ctx.getPeriodRange(m).start
				if d.IsZero() || !d.After(ctx.start) {
					return fmt.Sprintf("%.2d.%.2d.%.4d 00:00:00", sDy, sMo, sYr)
				}
				return fmt.Sprintf("%.2d.%.2d.%.4d 00:00:00", d.Day(), int(d.Month()), d.Year())
			},
			func(_ *counterpoint.CounterPoint, _ int) int { return 0 })...))

	_ = sw.SetRow("A6", append([]any{excelize.Cell{Value: "Period end"}},
		addHeaderV2(ctx, 3, 2,
			func(m *counterpoint.CounterPoint, _ int) any {
				d := ctx.getPeriodRange(m).end
				if d.IsZero() || !d.Before(ctx.end) {
					return fmt.Sprintf("%.2d.%.2d.%.4d 23:45:00", eDy, eMo, eYr)
				}
				return fmt.Sprintf("%.2d.%.2d.%.4d 23:45:00", d.Day(), int(d.Month()), d.Year())
			},
			func(_ *counterpoint.CounterPoint, _ int) int { return 0 })...))

	_ = sw.SetRow("A7", append([]any{excelize.Cell{Value: "Metercode"}},
		addHeaderV2(ctx, 3, 2,
			func(m *counterpoint.CounterPoint, i int) any {
				if m.Direction == counterpoint.DirectionConsumer {
					switch i {
					case 0:
						return "Gesamtverbrauch lt. Messung (bei Teilnahme gem. Erzeugung) [KWH]"
					case 1:
						return "Anteil gemeinschaftliche Erzeugung [KWH]"
					case 2:
						return "Eigendeckung gemeinschaftliche Erzeugung [KWH]"
					}
				} else {
					switch i {
					case 0:
						return "Gesamte gemeinschaftliche Erzeugung [KWH]"
					case 1:
						return "Gesamt/Überschusserzeugung, Gemeinschaftsüberschuss [KWH]"
					}
				}
				return ""
			},
			func(_ *counterpoint.CounterPoint, _ int) int { return 0 })...))

	return nil
}

func (es *EnergySheet) handleLine(ctx *runnerContext, line *queryengine.RawSourceLine) error {
	es.lineNum++
	ts, err := parseRowTime(line.ID)
	if err != nil {
		return err
	}
	lineDate := fmt.Sprintf("%.2d.%.2d.%.4d %.2d:%.2d:00",
		ts.Day(), int(ts.Month()), ts.Year(), ts.Hour(), ts.Minute())
	_ = es.writer.SetRow(fmt.Sprintf("A%d", es.lineNum+10),
		append([]any{excelize.Cell{Value: lineDate}}, addLine(line, ctx.countCons, ctx.meta, es.stylesQoV)...))

	if !checkQoV(line, ctx.meta) {
		ctx.qovLogArray = append(ctx.qovLogArray, line.Copy())
	}
	return nil
}

func (es *EnergySheet) closeSheet(_ *runnerContext) error {
	return es.writer.Flush()
}

// checkQoV mirrors v1 logic: per CP, if the line is before the CP's
// period start, skip; otherwise all 3 (consumer) or 2 (producer) slots
// must be QoV=1. Any mismatch returns false → caller appends to
// qovLogArray.
func checkQoV(line *queryengine.RawSourceLine, meta []counterpoint.CounterPoint) bool {
	lineDate, err := parseRowTime(line.ID)
	if err != nil {
		return true
	}
	checkDate := func(periodStart *time.Time) bool {
		if periodStart == nil {
			return false
		}
		return lineDate.Before(*periodStart)
	}
	for i := range meta {
		m := &meta[i]
		if m.Direction == counterpoint.DirectionConsumer {
			if checkDate(m.PeriodStart) {
				continue
			}
			base := m.SourceIdx
			if base+2 >= len(line.QoVConsumers) {
				continue
			}
			if line.QoVConsumers[base] != 1 || line.QoVConsumers[base+1] != 1 || line.QoVConsumers[base+2] != 1 {
				return false
			}
		} else {
			if checkDate(m.PeriodStart) {
				continue
			}
			base := m.SourceIdx
			if base+1 >= len(line.QoVProducers) {
				continue
			}
			if line.QoVProducers[base] != 1 || line.QoVProducers[base+1] != 1 {
				return false
			}
		}
	}
	return true
}
