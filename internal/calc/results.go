package calc

import (
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// calcResults is the running accumulator across one period. Mirrors v1
// calculation/EEGCalculation.go.
type calcResults struct {
	rAlloc *Matrix
	rCons  *Matrix
	rProd  *Matrix
	rDist  *Matrix
	rShar  *Matrix
	pSum   float64
}

func newCalcResult(info *queryengine.CounterPointMetaInfo) *calcResults {
	return &calcResults{
		rCons:  NewMatrix(info.ConsumerCount, 1),
		rAlloc: NewMatrix(info.ConsumerCount, 1),
		rProd:  NewMatrix(info.ProducerCount, 1),
		rDist:  NewMatrix(info.ProducerCount, 1),
		rShar:  NewMatrix(info.ConsumerCount, 1),
	}
}

// appendResults mirrors v1 appendResults: turn one RawSourceLine into
// matrix contributions via the alloc function and add into the
// accumulator.
func appendResults(line *queryengine.RawSourceLine, alloc AllocationHandlerV2, r *calcResults) error {
	cm, pm := ConvertLineToMatrix(line)
	m, s, p := alloc(cm, pm)

	consumerPick := MakeMatrix(make([]float64, 3), 3, 1)
	consumerPick.SetElm(0, 0, 1)
	producerPick := MakeMatrix(make([]float64, 2), 2, 1)
	producerPick.SetElm(0, 0, 1)

	consumed := Multiply(cm, consumerPick)
	produced := Multiply(pm, producerPick)

	if r.rCons == nil {
		r.rCons = NewCopiedMatrixFromElements(line.Consumers, len(line.Consumers), 1)
	} else {
		_ = r.rCons.Add(consumed)
	}
	if r.rProd == nil {
		r.rProd = NewCopiedMatrixFromElements(line.Producers, len(line.Producers), 1)
	} else {
		_ = r.rProd.Add(produced)
	}
	if r.rAlloc == nil {
		r.rAlloc = NewCopiedMatrixFromElements(m.Elements, m.CountRows(), m.CountCols())
	} else {
		_ = r.rAlloc.Add(m)
	}
	if r.rDist == nil {
		r.rDist = NewCopiedMatrixFromElements(p.Elements, p.CountRows(), p.CountCols())
	} else {
		_ = r.rDist.Add(p)
	}
	if r.rShar == nil {
		r.rShar = NewCopiedMatrixFromElements(s.Elements, s.CountRows(), s.CountCols())
	} else {
		_ = r.rShar.Add(s)
	}
	r.pSum += sum(produced.Elements)
	return nil
}

func sumIntermediate(intermediate calcResults, r *calcResults) error {
	if r.rCons == nil {
		r.rCons = NewCopiedMatrixFromElements(intermediate.rCons.Elements, len(intermediate.rCons.Elements), 1)
	} else {
		_ = r.rCons.Add(intermediate.rCons)
	}
	if r.rProd == nil {
		r.rProd = NewCopiedMatrixFromElements(intermediate.rProd.Elements, intermediate.rProd.Rows, 1)
	} else {
		_ = r.rProd.Add(intermediate.rProd)
	}
	if r.rAlloc == nil {
		r.rAlloc = NewCopiedMatrixFromElements(intermediate.rAlloc.Elements, intermediate.rAlloc.CountRows(), intermediate.rAlloc.CountCols())
	} else {
		_ = r.rAlloc.Add(intermediate.rAlloc)
	}
	if r.rDist == nil {
		r.rDist = NewCopiedMatrixFromElements(intermediate.rDist.Elements, intermediate.rDist.CountRows(), intermediate.rDist.CountCols())
	} else {
		_ = r.rDist.Add(intermediate.rDist)
	}
	if r.rShar == nil {
		r.rShar = NewCopiedMatrixFromElements(intermediate.rShar.Elements, intermediate.rShar.CountRows(), intermediate.rShar.CountCols())
	} else {
		_ = r.rShar.Add(intermediate.rShar)
	}
	r.pSum += intermediate.pSum
	return nil
}

func ensureMatrix(m *Matrix, defaultLen int) *Matrix {
	if m == nil {
		return NewMatrix(defaultLen, 1)
	}
	return m
}
