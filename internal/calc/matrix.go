// Package calc ports v1's calculation/ (~1500 LoC of algorithms + matrix
// math + report model) to v2. The maths is storage-agnostic — only the
// data-loader changed: instead of v1's BadgerDB iterator we drive a
// queryengine.QueryFunction so RawSourceLines stream out of the
// TimescaleDB hot-path.
//
// matrix.go: Matrix type + ops, ported verbatim from model/QuotaMatrix.go.
package calc

import (
	"errors"
	"math"
)

// Matrix is a flat row-major numeric matrix. Operations preserve v1
// numerics (in particular Multiply uses the same triple-loop and Add's
// auto-grow behaviour mirrors v1).
type Matrix struct {
	Rows     int       `json:"rows"`
	Cols     int       `json:"cols"`
	Elements []float64 `json:"elements"`
	step     int
}

const maxOneArray = 100

var oneArray = func() []float64 {
	a := make([]float64, maxOneArray)
	for i := range a {
		a[i] = 1
	}
	return a
}()

// NewMatrix returns a zero-filled matrix.
func NewMatrix(rows, cols int) *Matrix {
	return MakeMatrix(make([]float64, rows*cols), rows, cols)
}

// MakeMatrix wraps an existing slice (no copy).
func MakeMatrix(els []float64, rows, cols int) *Matrix {
	m := &Matrix{Rows: rows, Cols: cols, step: cols, Elements: els}
	m.ensureSize(rows*cols - 1)
	return m
}

// NewUniformMatrix shares the package-level oneArray for fast 1-fills.
func NewUniformMatrix(rows, cols int) *Matrix {
	return MakeMatrix(oneArray[:rows*cols], rows, cols)
}

// NewCopiedMatrixFromElements deep-copies into a new buffer.
func NewCopiedMatrixFromElements(els []float64, rows, cols int) *Matrix {
	m := NewMatrix(rows, cols)
	copy(m.Elements, els)
	return m
}

func (A *Matrix) CountRows() int { return A.Rows }
func (A *Matrix) CountCols() int { return A.Cols }

func (A *Matrix) GetElm(row, col int) float64 {
	return A.Elements[row*A.step+col]
}

func (A *Matrix) SetElm(row, col int, v float64) {
	idx := row*A.step + col
	A.ensureSize(idx)
	A.Elements[idx] = v
}

func (A *Matrix) SumElm(row, col int, v float64) {
	idx := row*A.step + col
	A.ensureSize(idx)
	A.Elements[idx] = A.Elements[idx] + v
}

func (A *Matrix) ensureSize(idx int) {
	if idx >= len(A.Elements) {
		newSize := idx - (idx % A.step) + A.step
		t := make([]float64, newSize)
		copy(t, A.Elements)
		A.Elements = t
		A.Rows = newSize / A.step
	}
}

// Add accumulates B into A. Mirrors v1's pad-to-max behaviour when
// dimensions differ (rare; primarily for ragged-EEG edge cases).
func (A *Matrix) Add(B *Matrix) error {
	if A.Elements == nil && A.Rows == 0 && A.Cols == 0 {
		A.Elements = make([]float64, B.Cols*B.Rows)
		A.Rows = B.Rows
		A.Cols = B.Cols
		A.step = B.step
	}
	if A.Cols != B.Cols || A.Rows != B.Rows {
		if A.step != B.step {
			return errors.New("calc: matrix add: stride mismatch")
		}
		maxRow := int(math.Max(float64(A.Rows), float64(B.Rows)))
		maxCol := int(math.Max(float64(A.Cols), float64(B.Cols)))
		tB := make([]float64, maxRow*maxCol)
		copy(tB, B.Elements)
		B.Elements = tB
		tA := make([]float64, maxRow*maxCol)
		copy(tA, A.Elements)
		A.Elements = tA
		B.Cols, B.Rows = maxCol, maxRow
		A.Cols, A.Rows = maxCol, maxRow
	}
	for i := 0; i < A.Rows; i++ {
		for j := 0; j < A.Cols; j++ {
			A.SetElm(i, j, A.GetElm(i, j)+B.GetElm(i, j))
		}
	}
	return nil
}

// RoundToFixed rounds every cell to `precision` decimals.
func (A *Matrix) RoundToFixed(precision uint) *Matrix {
	ratio := math.Pow(10, float64(precision))
	for i := 0; i < A.Rows; i++ {
		for j := 0; j < A.Cols; j++ {
			A.SetElm(i, j, math.Round(A.GetElm(i, j)*ratio)/ratio)
		}
	}
	return A
}

// Multiply does A·B with the v1 triple-loop ordering. Result dims are
// (A.Rows × B.Cols).
func Multiply(A, B *Matrix) *Matrix {
	rRows := A.Rows
	rCols := B.Cols
	r := MakeMatrix(make([]float64, rCols*rRows), rRows, rCols)
	for i := 0; i < A.Rows; i++ {
		for j := 0; j < B.Cols; j++ {
			var s float64
			for k := 0; k < A.Cols; k++ {
				s += A.GetElm(i, k) * B.GetElm(k, j)
			}
			r.SetElm(i, j, s)
		}
	}
	return r
}
