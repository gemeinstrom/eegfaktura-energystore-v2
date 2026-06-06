package calc

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
)

// ConvertLineToMatrix mirrors v1 utils.ConvertLineToMatrix: the flat
// Consumers/Producers slices become (n×3)/(m×2) matrices ready for
// AllocDynamicV2.
func ConvertLineToMatrix(line *queryengine.RawSourceLine) (*Matrix, *Matrix) {
	lenC := int(math.Max(float64(len(line.Consumers)-1), 1))
	lenP := int(math.Max(float64(len(line.Producers)-1), 1))
	rowsC := (lenC + 3 - (lenC % 3)) / 3
	rowsP := (lenP + 2 - (lenP % 2)) / 2
	c := MakeMatrix(line.Consumers, rowsC, 3)
	p := MakeMatrix(line.Producers, rowsP, 2)
	return c, p
}

// RoundFixed quantises a scalar.
func RoundFixed(v float64, precision uint) float64 {
	r := math.Pow(10, float64(precision))
	return math.Round(v*r) / r
}

// TruncateToDay zeros the time-of-day in local TZ. Used for participant
// from/until period comparisons.
func TruncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// PeriodToStartEndTime mirrors v1 utils.PeriodToStartEndTime: maps a
// (year, segment, periodCode) tuple to UTC [start, end] bounds.
// periodCode is one of:
//
//	"YM" — segment is month 1..12
//	"YQ" — segment is quarter 1..4
//	"YH" — segment is half-year 1..2
//	"Y"  — segment must be 0
func PeriodToStartEndTime(year, segment int, periodCode string) (time.Time, time.Time, error) {
	switch periodCode {
	case "YM":
		if segment > 0 && segment < 13 {
			return time.Date(year, time.Month(segment), 1, 0, 0, 0, 0, time.UTC),
				time.Date(year, time.Month(segment+1), 0, 0, 0, 0, 0, time.UTC), nil
		}
	case "YQ":
		if segment > 0 && segment < 5 {
			return time.Date(year, time.Month((segment-1)*3+1), 1, 0, 0, 0, 0, time.UTC),
				time.Date(year, time.Month((segment)*3+1), 0, 0, 0, 0, 0, time.UTC), nil
		}
	case "YH":
		if segment > 0 && segment < 3 {
			return time.Date(year, time.Month((segment-1)*6+1), 1, 0, 0, 0, 0, time.UTC),
				time.Date(year, time.Month((segment)*6+1), 0, 0, 0, 0, 0, time.UTC), nil
		}
	case "Y":
		if segment == 0 {
			return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
				time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, time.Time{},
		errors.New(fmt.Sprintf("calc: invalid period (year=%d, segment=%d, code=%s)", year, segment, periodCode))
}

// ensureFloatSlice returns `s` grown to at least `n` elements (mirrors
// v1 EnsureIntermediatValueSlice).
func ensureFloatSlice(s []float64, n int) []float64 {
	if n <= len(s) {
		return s
	}
	t := make([]float64, n)
	copy(t, s)
	return t
}
