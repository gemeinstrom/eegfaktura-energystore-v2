package calc

import (
	"math"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// AllocationHandlerV2 is the signature used by the primary calc path.
// allocResult / shareResult / prodResult mirror v1.
type AllocationHandlerV2 func(consumerMatrix, producerMatrix *Matrix) (alloc, share, prod *Matrix)

// AllocDynamicV2 is the production allocation function. v1 builds three
// (3x1)/(2x1) identity-style picker matrices and multiplies them against
// the (n×3) consumer / (m×2) producer wide-array matrices to select the
// pre-allocated columns. v2 keeps the algorithm verbatim — the input
// matrices are the same shape.
func AllocDynamicV2(consumerMatrix, producerMatrix *Matrix) (*Matrix, *Matrix, *Matrix) {
	consPick := MakeMatrix(make([]float64, 3), 3, 1)
	consPick.SetElm(2, 0, 1)
	alloc := Multiply(consumerMatrix, consPick)

	consPick.SetElm(2, 0, 0)
	consPick.SetElm(1, 0, 1)
	share := Multiply(consumerMatrix, consPick)

	prodPick := MakeMatrix(make([]float64, 2), 2, 1)
	prodPick.SetElm(1, 0, 1)
	prod := Multiply(producerMatrix, prodPick)

	return alloc, share, prod
}

// AllocDynamic2 mirrors v1 calculation/AllocateLine.go for completeness.
// Used by the legacy /eeg/report path (statistik aggregations).
func AllocDynamic2(line *queryengine.RawSourceLine) (*Matrix, *Matrix, *Matrix) {
	lenC := int(math.Max(float64(len(line.Consumers)), 1))
	lenP := int(math.Max(float64(len(line.Producers)), 1))

	allocResult := MakeMatrix(make([]float64, lenC), lenC, 1)
	shareResult := MakeMatrix(make([]float64, lenC), lenC, 1)
	prodResult := MakeMatrix(make([]float64, lenP), lenP, 1)

	consumerSum := sum(line.Consumers)
	producerSum := sum(line.Producers)

	var factor float64
	if producerSum > 0 && consumerSum > 0 {
		factor = producerSum / consumerSum
	}
	for i, l := range line.Consumers {
		var green float64
		if factor > 0 {
			green = l * factor
		}
		shareResult.SetElm(i, 0, green)
		allocResult.SetElm(i, 0, math.Min(green, l))
	}

	var prodFactor float64
	if producerSum > 0 && consumerSum > 0 {
		prodFactor = consumerSum / producerSum
	}
	for i, l := range line.Producers {
		green := l * prodFactor
		prodResult.SetElm(i, 0, math.Min(green, l))
	}

	return allocResult, shareResult, prodResult
}

func sum(s []float64) float64 {
	var t float64
	for _, v := range s {
		t += v
	}
	return t
}
