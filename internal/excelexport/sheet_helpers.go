package excelexport

import (
	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// addHeaderV2 builds one header row across all CPs. Each consumer
// occupies `cellCon` cells, each producer `cellProd` cells. Mirror v1.
func addHeaderV2(ctx *runnerContext, cellCon, cellProd int,
	value func(*counterpoint.CounterPoint, int) any,
	style func(*counterpoint.CounterPoint, int) int) []any {
	cCnt, pCnt := 0, 0
	out := make([]any, (ctx.countCons*cellCon)+(ctx.countProd*cellProd))
	for i := range ctx.meta {
		m := &ctx.meta[i]
		if m.Direction == counterpoint.DirectionConsumer {
			base := cCnt * cellCon
			cCnt++
			for j := 0; j < cellCon; j++ {
				out[base+j] = excelize.Cell{Value: value(m, j), StyleID: style(m, j)}
			}
		} else {
			base := (ctx.countCons * cellCon) + (pCnt * cellProd)
			pCnt++
			for j := 0; j < cellProd; j++ {
				out[base+j] = excelize.Cell{Value: value(m, j), StyleID: style(m, j)}
			}
		}
	}
	return out
}

// addLine builds one data row across all CPs. Mirror v1 addLine.
func addLine(g1 *queryengine.RawSourceLine, countCon int,
	meta []counterpoint.CounterPoint, stylesQoV []int) []any {

	lineData := make([]any, len(meta)*3)
	setCell := func(length, sourceIdx int, raw []float64, qov []int) excelize.Cell {
		if length <= sourceIdx {
			return excelize.Cell{Value: ""}
		}
		q := 1
		if sourceIdx < len(qov) {
			q = qov[sourceIdx]
		}
		switch q {
		case 1:
			return excelize.Cell{Value: roundTo6(raw[sourceIdx]), StyleID: stylesQoV[0]}
		case 2:
			return excelize.Cell{Value: roundTo6(raw[sourceIdx]), StyleID: stylesQoV[1]}
		case 3:
			return excelize.Cell{Value: roundTo6(raw[sourceIdx]), StyleID: stylesQoV[2]}
		default:
			return excelize.Cell{Value: ""}
		}
	}

	cCnt, pCnt := 0, 0
	for i := range meta {
		m := &meta[i]
		if m.Direction == counterpoint.DirectionConsumer {
			base := cCnt * 3
			cCnt++
			lineData[base] = setCell(len(g1.Consumers), m.SourceIdx*3, g1.Consumers, g1.QoVConsumers)
			lineData[base+1] = setCell(len(g1.Consumers), m.SourceIdx*3+1, g1.Consumers, g1.QoVConsumers)
			lineData[base+2] = setCell(len(g1.Consumers), m.SourceIdx*3+2, g1.Consumers, g1.QoVConsumers)
		} else {
			base := (countCon * 3) + (pCnt * 2)
			pCnt++
			lineData[base] = setCell(len(g1.Producers), m.SourceIdx*2, g1.Producers, g1.QoVProducers)
			lineData[base+1] = setCell(len(g1.Producers), m.SourceIdx*2+1, g1.Producers, g1.QoVProducers)
		}
	}
	return lineData
}
