// Package xlsximport ports v1's excel/{import.go,ExcelSourceNew.go} (~270 LoC)
// to the long-schema. The XLSX wire format is the netzbetreiber-Energiedaten
// export; v1 and v2 parse the same headers + the same date cells, only the
// destination differs (v1: BadgerDB wide-array, v2: counterpoint_meta rows +
// energy_data slots).
//
// Each row in [meta-block][data-block] structure becomes:
//   - one counterpoint.CounterPoint per metering point (header derived)
//   - up to 3 (consumer) or 2 (producer) store.Slot per data row, mapped via
//     the OBIS code table in queryengine/codes.go
package xlsximport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
)

var (
	reDateLine = regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}\.[0-9]{4}\s[0-9]{2}:[0-9]{2}:[0-9]{2}$`)
	reNumeric  = regexp.MustCompile(`^[0-9\.,]+$`)
)

// meterCodeKind identifies the column's role within a metering point.
type meterCodeKind int

const (
	codeTotal meterCodeKind = iota
	codeShare
	codeCoverage
	codeProfit
	codeTotalProd
	codeBad
)

type excelHeader struct {
	meteringPointID map[int]string
	energyDirection map[int]counterpoint.Direction
	periodStart     map[int]time.Time
	periodEnd       map[int]time.Time
	meterCode       map[int]meterCodeKind
}

type cpRecord struct {
	meteringPoint string
	direction     counterpoint.Direction
	sourceIdx     int
	periodStart   *time.Time
	periodEnd     *time.Time
	// idxTotal/idxShare/idxCoverage are column offsets (-1 if absent).
	idxTotal    int
	idxShare    int
	idxCoverage int
}

// Importer wires the dependencies an import needs. Repository writes the
// per-MP meta rows; Store writes the per-slot energy_data rows.
type Importer struct {
	Tenant     string
	ECID       string
	SheetName  string
	Repository *counterpoint.Repository
	Store      *store.Store
}

// ImportReader runs the parse + upsert pipeline against a reader holding
// an XLSX file. Returns the number of (counterpoints, slots) written.
func (im *Importer) ImportReader(ctx context.Context, r io.Reader) (cpCount, slotCount int, err error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return 0, 0, fmt.Errorf("xlsximport: open: %w", err)
	}
	defer f.Close()
	return im.importFile(ctx, f)
}

// for tests
func (im *Importer) importFile(ctx context.Context, f *excelize.File) (int, int, error) {
	rows, err := f.Rows(im.SheetName)
	if err != nil {
		return 0, 0, fmt.Errorf("xlsximport: rows %s: %w", im.SheetName, err)
	}
	defer rows.Close()

	var (
		hdr        excelHeader
		hdrReady   bool
		excelMeta  []*cpRecord
		slotsBatch []store.Slot
	)

	for rows.Next() {
		cols, err := rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil || len(cols) == 0 {
			continue
		}
		switch cols[0] {
		case "MeteringpointID":
			hdr.meteringPointID = mapFromCols(cols)
		case "Spaltensumme", "Metering Interval",
			"Name", "MeteringReason", "Number of Metering Intervals",
			"Spaltensumme / minimale Qualität", "Data Completeness",
			"Metering Point active end", "Metering Point active start",
			"Data Period end", "Data Period start":
			continue
		case "Energy direction":
			hdr.energyDirection = make(map[int]counterpoint.Direction)
			for i, c := range cols[1:] {
				d, err := counterpoint.ParseDirection(c)
				if err == nil {
					hdr.energyDirection[i] = d
				}
			}
		case "Period end", "Report Filter end":
			hdr.periodEnd = parsePeriodMap(cols)
		case "Period start", "Report Filter start":
			hdr.periodStart = parsePeriodMap(cols)
		case "Metercode":
			hdr.meterCode = make(map[int]meterCodeKind)
			for i, c := range cols[1:] {
				hdr.meterCode[i] = classifyMeterCode(strings.ToUpper(c))
			}
		default:
			if !isDate(cols[0]) {
				continue
			}
			if !hdrReady {
				excelMeta, err = buildCpRecords(hdr)
				if err != nil {
					return 0, 0, err
				}
				hdrReady = true
			}
			ts, err := parseDateCell(cols[0])
			if err != nil {
				continue
			}
			for _, cp := range excelMeta {
				slots := slotsForRow(cp, ts, cols, im.Tenant, im.ECID)
				slotsBatch = append(slotsBatch, slots...)
			}
		}
	}

	if !hdrReady {
		return 0, 0, errors.New("xlsximport: no header rows found (Metercode/MeteringpointID/Energy direction)")
	}

	for _, cp := range excelMeta {
		row := counterpoint.CounterPoint{
			TenantID:      im.Tenant,
			ECID:          im.ECID,
			MeteringPoint: cp.meteringPoint,
			Direction:     cp.direction,
			SourceIdx:     cp.sourceIdx,
			PeriodStart:   cp.periodStart,
			PeriodEnd:     cp.periodEnd,
		}
		if err := im.Repository.Upsert(ctx, row); err != nil {
			return 0, 0, fmt.Errorf("xlsximport: upsert cp %s: %w", cp.meteringPoint, err)
		}
	}

	if len(slotsBatch) > 0 {
		if err := im.Store.UpsertSlots(ctx, slotsBatch); err != nil {
			return 0, 0, fmt.Errorf("xlsximport: upsert slots: %w", err)
		}
	}
	return len(excelMeta), len(slotsBatch), nil
}

// slotsForRow translates one Excel data row + one CP entry into the matching
// long-schema rows. Returns at most 3 slots (consumer) or 2 (producer).
func slotsForRow(cp *cpRecord, ts time.Time, cols []string, tenant, ec string) []store.Slot {
	var out []store.Slot
	emit := func(idx int, code string) {
		if idx < 0 {
			return
		}
		v := returnFloat(safeCol(cols, idx+1))
		out = append(out, store.Slot{
			TenantID:      tenant,
			ECID:          ec,
			MeteringPoint: cp.meteringPoint,
			MeterCode:     code,
			Timestamp:     ts,
			Value:         v,
			QoV:           0,
		})
	}
	switch cp.direction {
	case counterpoint.DirectionConsumer:
		emit(cp.idxTotal, "1-1:1.9.0 G.01")
		emit(cp.idxShare, "1-1:2.9.0 G.02")
		emit(cp.idxCoverage, "1-1:2.9.0 G.03")
	case counterpoint.DirectionProducer:
		emit(cp.idxTotal, "1-1:2.9.0 G.01")
		emit(cp.idxShare, "1-1:2.9.0 P.01")
	}
	return out
}

// buildCpRecords scans the parsed header to produce one cpRecord per
// metering point. source_idx is assigned per direction in column order
// (matches v1 behaviour: consumers numbered 0..n, producers 0..m).
func buildCpRecords(h excelHeader) ([]*cpRecord, error) {
	if h.meteringPointID == nil || h.energyDirection == nil || h.meterCode == nil {
		return nil, errors.New("xlsximport: header incomplete")
	}
	type pair struct {
		idxTotal    int
		idxShare    int
		idxCoverage int
		direction   counterpoint.Direction
		periodStart *time.Time
		periodEnd   *time.Time
	}
	byMP := map[string]*pair{}

	insert := func(i int, kind meterCodeKind) {
		raw := h.meteringPointID[i]
		mp := strings.TrimSpace(raw)
		if len(mp) < 4 || strings.EqualFold(mp, "total") || strings.EqualFold(mp, "mm") {
			return
		}
		dir, ok := h.energyDirection[i]
		if !ok {
			return
		}
		p, ok := byMP[mp]
		if !ok {
			p = &pair{idxTotal: -1, idxShare: -1, idxCoverage: -1, direction: dir}
			if ps, ok := h.periodStart[i]; ok {
				ts := ps
				p.periodStart = &ts
			}
			if pe, ok := h.periodEnd[i]; ok {
				ts := pe
				p.periodEnd = &ts
			}
			byMP[mp] = p
		}
		switch kind {
		case codeTotal, codeTotalProd:
			// codeTotal is the consumer total ("GESAMTVERBRAUCH …"),
			// codeTotalProd is the producer total ("GESAMTE
			// GEMEINSCHAFTLICHE ERZEUGUNG …"). Both occupy slot 0 of
			// their respective wide-array block — see codes.go.
			p.idxTotal = i
		case codeShare, codeProfit:
			p.idxShare = i
		case codeCoverage:
			p.idxCoverage = i
		}
	}

	for i := 0; i < len(h.meteringPointID); i++ {
		kind, ok := h.meterCode[i]
		if !ok || kind == codeBad {
			continue
		}
		insert(i, kind)
	}

	// Filter: must have at least the Total column.
	mps := make([]string, 0, len(byMP))
	for mp, p := range byMP {
		if p.idxTotal < 0 {
			continue
		}
		mps = append(mps, mp)
	}
	sort.SliceStable(mps, func(i, j int) bool {
		return byMP[mps[i]].idxTotal < byMP[mps[j]].idxTotal
	})

	var consumerIdx, producerIdx int
	out := make([]*cpRecord, 0, len(mps))
	for _, mp := range mps {
		p := byMP[mp]
		rec := &cpRecord{
			meteringPoint: mp,
			direction:     p.direction,
			idxTotal:      p.idxTotal,
			idxShare:      p.idxShare,
			idxCoverage:   p.idxCoverage,
			periodStart:   p.periodStart,
			periodEnd:     p.periodEnd,
		}
		switch p.direction {
		case counterpoint.DirectionConsumer:
			rec.sourceIdx = consumerIdx
			consumerIdx++
		case counterpoint.DirectionProducer:
			rec.sourceIdx = producerIdx
			producerIdx++
		}
		out = append(out, rec)
	}
	return out, nil
}

func mapFromCols(cols []string) map[int]string {
	out := make(map[int]string, len(cols)-1)
	for i, c := range cols[1:] {
		out[i] = c
	}
	return out
}

func parsePeriodMap(cols []string) map[int]time.Time {
	out := make(map[int]time.Time, len(cols)-1)
	for i, c := range cols[1:] {
		t, err := parseDateCell(c)
		if err == nil {
			out[i] = t
		}
	}
	return out
}

func parseDateCell(cell string) (time.Time, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return time.Time{}, errors.New("empty cell")
	}
	if reDateLine.MatchString(cell) {
		// Format: DD.MM.YYYY HH:MM:SS
		return time.ParseInLocation("02.01.2006 15:04:05", cell, time.Local)
	}
	if reNumeric.MatchString(cell) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(cell, ",", "."), 64)
		if err != nil {
			return time.Time{}, err
		}
		epoch := time.Date(1899, time.December, 30, 0, 0, 0, 0, time.UTC)
		return epoch.Add(time.Duration(v * float64(24*time.Hour))).Round(15 * time.Minute), nil
	}
	return time.Time{}, fmt.Errorf("xlsximport: not a date: %q", cell)
}

func isDate(cell string) bool {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return false
	}
	return reDateLine.MatchString(cell) || reNumeric.MatchString(cell)
}

func classifyMeterCode(c string) meterCodeKind {
	switch {
	case strings.Contains(c, "GESAMTVERBRAUCH"):
		return codeTotal
	case strings.Contains(c, "GESAMTE GEMEINSCHAFTLICHE"):
		return codeTotalProd
	case strings.Contains(c, "ANTEIL"):
		return codeShare
	case strings.Contains(c, "EIGENDECKUNG"):
		return codeCoverage
	case strings.Contains(c, "ÜBERSCHUSSERZEUGUNG"), strings.Contains(c, "UEBERSCHUSSERZEUGUNG"):
		return codeProfit
	default:
		return codeBad
	}
}

func returnFloat(c string) float64 {
	c = strings.TrimSpace(c)
	if c == "" {
		return 0
	}
	c = strings.ReplaceAll(c, ",", ".")
	v, err := strconv.ParseFloat(c, 64)
	if err != nil {
		return 0
	}
	return v
}

func safeCol(cols []string, idx int) string {
	if idx < 0 || idx >= len(cols) {
		return ""
	}
	return cols[idx]
}

