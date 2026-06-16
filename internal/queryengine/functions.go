package queryengine

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

// ParentFunction provides the per-CP RawData result map that Default,
// Aggregate and Raw share. It implements addToResult/GetResult — the
// HandleStart/Line/End methods live on the concrete types.
type ParentFunction struct {
	cps    []TargetMP
	Result map[string]*RawDataResult
}

// addToResult mirrors v1 default_function.go addToResult.
func (p *ParentFunction) addToResult(ctx *EngineContext, ts time.Time, line *RawSourceLine) error {
	for _, cp := range p.cps {
		m, ok := ctx.MetaMap[cp.MeteringPoint]
		if !ok {
			// v1 panics on missing meta; we silently drop instead.
			continue
		}
		if _, exists := p.Result[cp.MeteringPoint]; !exists {
			p.Result[cp.MeteringPoint] = &RawDataResult{Direction: m.Direction}
		}
		if m.Direction == counterpoint.DirectionConsumer {
			rd := RawData{Ts: ts.UnixMilli(), Value: make([]float64, 3), Qov: make([]int, 3)}
			start := m.SourceIdx * 3
			if start+3 <= len(line.Consumers) {
				copy(rd.Value, line.Consumers[start:start+3])
				copy(rd.Qov, line.QoVConsumers[start:start+3])
			}
			p.Result[cp.MeteringPoint].Data = append(p.Result[cp.MeteringPoint].Data, rd)
		} else {
			rd := RawData{Ts: ts.UnixMilli(), Value: make([]float64, 2), Qov: make([]int, 2)}
			start := m.SourceIdx * 2
			if start+2 <= len(line.Producers) {
				copy(rd.Value, line.Producers[start:start+2])
				copy(rd.Qov, line.QoVProducers[start:start+2])
			}
			p.Result[cp.MeteringPoint].Data = append(p.Result[cp.MeteringPoint].Data, rd)
		}
	}
	return nil
}

// GetResult exposes the per-MP result map.
func (p *ParentFunction) GetResult() map[string]*RawDataResult { return p.Result }

// DefaultFunction emits raw 15-min slots untouched (no bucketing).
type DefaultFunction struct {
	ParentFunction
}

// NewDefaultFunction is the v1 "no function specified" path.
func NewDefaultFunction(cps []TargetMP) *DefaultFunction {
	return &DefaultFunction{ParentFunction{cps: cps}}
}

func (d *DefaultFunction) HandleStart(_ *EngineContext) error {
	d.Result = make(map[string]*RawDataResult)
	return nil
}

func (d *DefaultFunction) HandleLine(ctx *EngineContext, line *RawSourceLine) error {
	ts, err := rowIDToTime(line.ID)
	if err != nil {
		return err
	}
	return d.addToResult(ctx, ts, line)
}

func (d *DefaultFunction) HandleEnd(_ *EngineContext) error { return nil }

// AggregateFunction time-buckets per cacheTs. v1's signature
// `aggregate(<duration>)` is preserved (e.g. `aggregate(1h)`, `aggregate(7d)`).
type AggregateFunction struct {
	ParentFunction
	cache
}

func NewAggregateFunction(args []string, cps []TargetMP) (*AggregateFunction, error) {
	if len(args) != 1 {
		return nil, errors.New("queryengine: aggregate takes exactly one argument")
	}
	d, err := parseDurationArg(args[0])
	if err != nil {
		return nil, err
	}
	return &AggregateFunction{
		ParentFunction: ParentFunction{cps: cps},
		cache:          cache{cacheTs: d},
	}, nil
}

func parseDurationArg(arg string) (time.Duration, error) {
	if arg == "" {
		return 0, errors.New("queryengine: empty duration")
	}
	last := arg[len(arg)-1]
	switch last {
	case 'h':
		return time.ParseDuration(arg)
	case 'd':
		v, err := strconv.ParseInt(arg[:len(arg)-1], 10, 16)
		if err != nil {
			return 0, fmt.Errorf("queryengine: bad days: %w", err)
		}
		return time.ParseDuration(fmt.Sprintf("%dh", v*24))
	}
	return 0, fmt.Errorf("queryengine: unsupported duration %q (use Nh or Nd)", arg)
}

func (a *AggregateFunction) HandleStart(ctx *EngineContext) error {
	a.Result = make(map[string]*RawDataResult)
	return a.cache.init(ctx)
}

func (a *AggregateFunction) HandleLine(ctx *EngineContext, line *RawSourceLine) error {
	ts, err := rowIDToTime(line.ID)
	if err != nil {
		return err
	}
	return a.cache.cacheLine(ctx, ts, line, a.addToResult)
}

func (a *AggregateFunction) HandleEnd(ctx *EngineContext) error {
	return a.addToResult(ctx, a.cacheTime, &a.cache.cache)
}

// IntraDay groups by hour-of-day (0..23) into a 24-slot ReportData array.
type IntraDay struct {
	cache
	result map[int]*ReportData
}

func NewIntraDayFunction() *IntraDay {
	return &IntraDay{
		cache:  cache{cacheTs: time.Hour},
		result: make(map[int]*ReportData),
	}
}

func (id *IntraDay) HandleStart(ctx *EngineContext) error { return id.cache.init(ctx) }

func (id *IntraDay) HandleLine(ctx *EngineContext, line *RawSourceLine) error {
	ts, err := rowIDToTime(line.ID)
	if err != nil {
		return err
	}
	return id.cache.cacheLine(ctx, ts, line, id.addToResult)
}

func (id *IntraDay) HandleEnd(ctx *EngineContext) error {
	return id.addToResult(ctx, id.cacheTime, &id.cache.cache)
}

// GetResult returns the 24-bucket array, zero-filled where empty.
func (id *IntraDay) GetResult() []*ReportData {
	out := make([]*ReportData, 24)
	for i := range out {
		if r, ok := id.result[i]; ok {
			out[i] = r
		} else {
			out[i] = &ReportData{}
		}
	}
	return out
}

func (id *IntraDay) addToResult(_ *EngineContext, t time.Time, line *RawSourceLine) error {
	hour := t.Add(-1 * id.cacheTs).UTC().Hour()
	rd, ok := id.result[hour]
	if !ok {
		rd = &ReportData{}
		id.result[hour] = rd
	}
	accumulateReportData(rd, line)
	return nil
}

// LoadCurve aggregates per calendar day (Berlin local) into ordered
// ReportData entries with v1-shape Name "D:MM:DD:DOW".
type LoadCurve struct {
	cache
	bucket map[string]*ReportData
	order  []string
}

func NewLoadCurveFunction() *LoadCurve {
	return &LoadCurve{
		cache:  cache{cacheTs: 24 * time.Hour},
		bucket: make(map[string]*ReportData),
	}
}

func (lc *LoadCurve) HandleStart(ctx *EngineContext) error { return lc.cache.init(ctx) }

func (lc *LoadCurve) HandleLine(ctx *EngineContext, line *RawSourceLine) error {
	ts, err := rowIDToTime(line.ID)
	if err != nil {
		return err
	}
	return lc.cache.cacheLine(ctx, ts, line, lc.addToResult)
}

func (lc *LoadCurve) HandleEnd(ctx *EngineContext) error {
	return lc.addToResult(ctx, lc.cacheTime, &lc.cache.cache)
}

// GetResult returns ReportData entries in chronological order.
func (lc *LoadCurve) GetResult() []*ReportData {
	out := make([]*ReportData, 0, len(lc.order))
	for _, k := range lc.order {
		out = append(out, lc.bucket[k])
	}
	return out
}

func (lc *LoadCurve) addToResult(_ *EngineContext, t time.Time, line *RawSourceLine) error {
	day := t.Add(-1 * lc.cacheTs)
	key := fmt.Sprintf("%04d-%02d-%02d", day.Year(), int(day.Month()), day.Day())
	dow := (int(day.Weekday()) + 6) % 7
	name := fmt.Sprintf("D:%02d:%02d:%02d", int(day.Month()), day.Day(), dow)

	rd, ok := lc.bucket[key]
	if !ok {
		rd = &ReportData{Name: name}
		lc.bucket[key] = rd
		lc.order = append(lc.order, key)
	} else if rd.Name == "" {
		rd.Name = name
	}
	accumulateReportData(rd, line)
	return nil
}

// MonthlyCurve groups slots by calendar month (Berlin local) and emits
// `M:YYYY:MM:00`-shape Name entries — the prod-extension counterpart to
// LoadCurve. The customer-web `calcXAxisNameV2` already understands
// `M:`-codes (LoadCurveReport.functions.ts), but public-v1's only
// aggregation function is `aggregate(Nh|Nd)` which cannot express
// "calendar month" (variable length). Used by the combined-report
// dispatcher when the requested range exceeds ~45 days so the
// Lastverlauf-Jahres-Ansicht shows month bars instead of 365 daily bars.
type MonthlyCurve struct {
	bucket map[string]*ReportData
	order  []string
}

func NewMonthlyCurveFunction() *MonthlyCurve {
	return &MonthlyCurve{bucket: make(map[string]*ReportData)}
}

func (mc *MonthlyCurve) HandleStart(_ *EngineContext) error { return nil }

func (mc *MonthlyCurve) HandleLine(_ *EngineContext, line *RawSourceLine) error {
	ts, err := rowIDToTime(line.ID)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%04d-%02d", ts.Year(), int(ts.Month()))
	rd, ok := mc.bucket[key]
	if !ok {
		rd = &ReportData{Name: fmt.Sprintf("M:%04d:%02d:00", ts.Year(), int(ts.Month()))}
		mc.bucket[key] = rd
		mc.order = append(mc.order, key)
	}
	accumulateReportData(rd, line)
	return nil
}

func (mc *MonthlyCurve) HandleEnd(_ *EngineContext) error { return nil }

func (mc *MonthlyCurve) GetResult() []*ReportData {
	out := make([]*ReportData, 0, len(mc.order))
	for _, k := range mc.order {
		out = append(out, mc.bucket[k])
	}
	return out
}

// Summary returns a single ReportData covering the whole queried range.
type Summary struct {
	result *ReportData
}

func NewSummary() *Summary {
	return &Summary{result: &ReportData{}}
}

func (s *Summary) HandleStart(_ *EngineContext) error { return nil }

func (s *Summary) HandleLine(_ *EngineContext, line *RawSourceLine) error {
	cLen := len(line.Consumers) - (len(line.Consumers) % 3)
	for i := 0; i < cLen; i += 3 {
		s.result.Consumed += line.Consumers[i]
		s.result.Allocated += line.Consumers[i+1]
		s.result.Distributed += line.Consumers[i+2]
		qov := 1
		if i < len(line.QoVConsumers) {
			qov = line.QoVConsumers[i]
		}
		s.result.QoVConsumer = calcQoV(s.result.QoVConsumer, qov)
	}
	pLen := len(line.Producers) - (len(line.Producers) % 2)
	for i := 0; i < pLen; i += 2 {
		s.result.Produced += line.Producers[i]
		qov := 1
		if i < len(line.QoVProducers) {
			qov = line.QoVProducers[i]
		}
		s.result.QoVProducer = calcQoV(s.result.QoVProducer, qov)
	}
	return nil
}

func (s *Summary) HandleEnd(_ *EngineContext) error { return nil }

// GetResult exposes the aggregated ReportData. Caller usually marshals
// this to JSON for /summary.
func (s *Summary) GetResult() *ReportData { return s.result }

// accumulateReportData is the shared accumulate logic for IntraDay and
// LoadCurve — both walk consumer/producer wide arrays and update a
// ReportData with cnt/qov tracking. Producer Distributed lives in slot 1
// of each producer block (mirror of v1's semantics: producer.distributed
// is what the network took off the producer; we already added it via
// the consumer cover side, so producer Distributed isn't included in
// rd.Distributed). Unused = Produced - Distributed-from-consumers.
func accumulateReportData(rd *ReportData, line *RawSourceLine) {
	activeCons := 0
	cLen := len(line.Consumers) - (len(line.Consumers) % 3)
	for i := 0; i < cLen; i += 3 {
		if line.Consumers[i] != 0 || line.Consumers[i+1] != 0 || line.Consumers[i+2] != 0 {
			activeCons++
		}
		rd.Consumed += line.Consumers[i]
		rd.Allocated += line.Consumers[i+1]
		rd.Distributed += line.Consumers[i+2]
		if i < len(line.QoVConsumers) {
			rd.QoVConsumer = calcQoV(rd.QoVConsumer, line.QoVConsumers[i])
		}
	}
	if activeCons > rd.CntConsumer {
		rd.CntConsumer = activeCons
	}

	activeProd := 0
	pLen := len(line.Producers) - (len(line.Producers) % 2)
	for i := 0; i < pLen; i += 2 {
		if line.Producers[i] != 0 || line.Producers[i+1] != 0 {
			activeProd++
		}
		rd.Produced += line.Producers[i]
		if i < len(line.QoVProducers) {
			rd.QoVProducer = calcQoV(rd.QoVProducer, line.QoVProducers[i])
		}
	}
	if activeProd > rd.CntProducer {
		rd.CntProducer = activeProd
	}

	rd.Unused = rd.Produced - rd.Distributed
	if rd.Unused < 0 {
		rd.Unused = 0
	}
}

// QueryFunctionRegistry mirrors v1 `Functions` map used by the /raw
// endpoint to pick a function from a `f=name(args)` query param.
var fnRE = regexp.MustCompile(`^(\w*)[^(]*\(([^)]*)\)$`)

type RawFunctionFactory func(args []string, cps []TargetMP) (QueryFunction, error)

// RawFunctions is the v1 dispatch table. Callers add custom factories at
// init() time if they extend it; the built-ins match v1 names.
var RawFunctions = map[string]RawFunctionFactory{
	"AGGREGATE": func(args []string, cps []TargetMP) (QueryFunction, error) {
		return NewAggregateFunction(args, cps)
	},
}

// ParseRawFunction splits "name(arg,arg)" into name + args. Empty arg
// list returns empty slice (not nil) so callers can pass to factories.
func ParseRawFunction(f string) (string, []string, error) {
	m := fnRE.FindStringSubmatch(f)
	if len(m) < 3 {
		return "", nil, errors.New("queryengine: cannot parse function expression")
	}
	args := []string{}
	if m[2] != "" {
		for _, p := range strings.Split(m[2], ",") {
			args = append(args, strings.TrimSpace(p))
		}
	}
	return strings.ToUpper(m[1]), args, nil
}
