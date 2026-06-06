package excelexport

import (
	"fmt"

	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

// generateQoVLogSheet writes the optional "QoV Log" sheet listing all
// lines that failed the QoV check, with one (value, qov) pair per CP
// slot. Mirrors v1 excel/QoVSheet.go.
func generateQoVLogSheet(ctx *runnerContext, f *excelize.File) error {
	const name = "QoV Log"
	if _, err := f.NewSheet(name); err != nil {
		return err
	}

	styleL3, _ := f.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Color: []string{"ff5429"}, Pattern: 1}})
	styleL2, _ := f.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Color: []string{"FFFF00"}, Pattern: 1}})
	numFmt := "#,##0.000000"
	styleNum, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &numFmt})
	qovStyle := []int{styleNum, styleL2, styleL3}

	sw, err := f.NewStreamWriter(name)
	if err != nil {
		return err
	}

	_ = sw.SetColWidth(1, 1, 30)
	totalCols := (ctx.countCons * 6) + (ctx.countProd * 4)
	for i := 0; i < totalCols; i++ {
		if i%2 == 0 {
			_ = sw.SetColWidth(i+2, i+2, 25)
		} else {
			_ = sw.SetColWidth(i+2, i+2, 5)
		}
	}

	header := []any{excelize.Cell{Value: "Timestamp"}}
	for i := range ctx.meta {
		m := &ctx.meta[i]
		if m.Direction == counterpoint.DirectionConsumer {
			for j := 0; j < 3; j++ {
				header = append(header,
					excelize.Cell{Value: fmt.Sprintf("%s slot %d", m.MeteringPoint, j)},
					excelize.Cell{Value: "qov"})
			}
		} else {
			for j := 0; j < 2; j++ {
				header = append(header,
					excelize.Cell{Value: fmt.Sprintf("%s slot %d", m.MeteringPoint, j)},
					excelize.Cell{Value: "qov"})
			}
		}
	}
	_ = sw.SetRow("A2", header)

	for li, line := range ctx.qovLogArray {
		ts, _ := parseRowTime(line.ID)
		row := []any{excelize.Cell{Value: fmt.Sprintf("%.2d.%.2d.%.4d %.2d:%.2d:00",
			ts.Day(), int(ts.Month()), ts.Year(), ts.Hour(), ts.Minute())}}
		for i := range ctx.meta {
			m := &ctx.meta[i]
			if m.Direction == counterpoint.DirectionConsumer {
				for j := 0; j < 3; j++ {
					idx := m.SourceIdx*3 + j
					val, qov := safeFloat(line.Consumers, idx), safeInt(line.QoVConsumers, idx, 1)
					row = append(row,
						excelize.Cell{Value: roundTo6(val), StyleID: qovStyle[qovBucket(qov)]},
						excelize.Cell{Value: qov})
				}
			} else {
				for j := 0; j < 2; j++ {
					idx := m.SourceIdx*2 + j
					val, qov := safeFloat(line.Producers, idx), safeInt(line.QoVProducers, idx, 1)
					row = append(row,
						excelize.Cell{Value: roundTo6(val), StyleID: qovStyle[qovBucket(qov)]},
						excelize.Cell{Value: qov})
				}
			}
		}
		_ = sw.SetRow(fmt.Sprintf("A%d", li+3), row)
	}
	return sw.Flush()
}

func safeFloat(s []float64, idx int) float64 {
	if idx < 0 || idx >= len(s) {
		return 0
	}
	return s[idx]
}

func safeInt(s []int, idx int, fallback int) int {
	if idx < 0 || idx >= len(s) {
		return fallback
	}
	return s[idx]
}

func qovBucket(q int) int {
	switch q {
	case 1:
		return 0
	case 2:
		return 1
	default:
		return 2
	}
}
