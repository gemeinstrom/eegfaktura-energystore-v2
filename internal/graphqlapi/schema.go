// Package graphqlapi ports v1's GraphQL surface to v2. The schema is
// tiny (2 queries + 1 mutation) and is reachable under POST /query.
//
// v1 used 99designs/gqlgen which generates ~3.5k LoC of boilerplate.
// v2 uses the runtime schema library graphql-go/graphql — fewer
// generated files, easier to reason about, and a closer fit for the
// hand-written resolver style that the v1 codebase already used.
//
// The schema mirrors v1 exactly so existing callers (eegfaktura-web,
// eegfaktura-admin) don't have to change their GraphQL queries.
package graphqlapi

import (
	"github.com/graphql-go/graphql"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
)

// counterPointMeta type — wire fields exactly match v1 CounterPointMeta.
var counterPointMetaType = graphql.NewObject(graphql.ObjectConfig{
	Name: "CounterPointMeta",
	Fields: graphql.Fields{
		"id":          {Type: graphql.String, Resolve: cpField(func(c *counterpoint.CounterPoint) any { return c.MeteringPoint })},
		"name":        {Type: graphql.String, Resolve: cpField(func(c *counterpoint.CounterPoint) any { return c.Name })},
		"sourceIdx":   {Type: graphql.Int, Resolve: cpField(func(c *counterpoint.CounterPoint) any { return c.SourceIdx })},
		"dir":         {Type: graphql.String, Resolve: cpField(func(c *counterpoint.CounterPoint) any { return c.Direction.String() })},
		"count":       {Type: graphql.Int, Resolve: cpField(func(c *counterpoint.CounterPoint) any { return 0 })},
		"period_start": {Type: graphql.String, Resolve: cpField(func(c *counterpoint.CounterPoint) any {
			if c.PeriodStart == nil {
				return ""
			}
			return c.PeriodStart.Format("02.01.2006 15:04:05")
		})},
		"period_end": {Type: graphql.String, Resolve: cpField(func(c *counterpoint.CounterPoint) any {
			if c.PeriodEnd == nil {
				return ""
			}
			return c.PeriodEnd.Format("02.01.2006 15:04:05")
		})},
	},
})

// energyReportType mirrors v1 model.EnergyReport.
var energyReportType = graphql.NewObject(graphql.ObjectConfig{
	Name: "EnergyReport",
	Fields: graphql.Fields{
		"id":             {Type: graphql.String, Resolve: erField(func(r *calc.EnergyReport) any { return r.ID })},
		"allocated":      {Type: graphql.NewList(graphql.Float), Resolve: erField(func(r *calc.EnergyReport) any { return r.Allocated })},
		"consumed":       {Type: graphql.NewList(graphql.Float), Resolve: erField(func(r *calc.EnergyReport) any { return r.Consumed })},
		"produced":       {Type: graphql.NewList(graphql.Float), Resolve: erField(func(r *calc.EnergyReport) any { return r.Produced })},
		"distributed":    {Type: graphql.NewList(graphql.Float), Resolve: erField(func(r *calc.EnergyReport) any { return r.Distributed })},
		"shared":         {Type: graphql.NewList(graphql.Float), Resolve: erField(func(r *calc.EnergyReport) any { return r.Shared })},
		"total_produced": {Type: graphql.Float, Resolve: erField(func(r *calc.EnergyReport) any { return r.TotalProduced })},
	},
})

// eegEnergyType — top-level result of report query.
var eegEnergyType = graphql.NewObject(graphql.ObjectConfig{
	Name: "EegEnergy",
	Fields: graphql.Fields{
		"report": {Type: energyReportType, Resolve: func(p graphql.ResolveParams) (any, error) {
			e, _ := p.Source.(*calc.EegEnergy)
			if e == nil {
				return nil, nil
			}
			return e.Report, nil
		}},
		"intermediateReportResults": {Type: graphql.NewList(energyReportType), Resolve: func(p graphql.ResolveParams) (any, error) {
			e, _ := p.Source.(*calc.EegEnergy)
			if e == nil {
				return nil, nil
			}
			return e.Results, nil
		}},
		"meta": {Type: graphql.NewList(counterPointMetaType), Resolve: func(p graphql.ResolveParams) (any, error) {
			e, _ := p.Source.(*calc.EegEnergy)
			if e == nil {
				return nil, nil
			}
			return e.Meta, nil
		}},
	},
})

func cpField(get func(*counterpoint.CounterPoint) any) func(graphql.ResolveParams) (any, error) {
	return func(p graphql.ResolveParams) (any, error) {
		cp, _ := p.Source.(*counterpoint.CounterPoint)
		if cp == nil {
			return nil, nil
		}
		return get(cp), nil
	}
}

func erField(get func(*calc.EnergyReport) any) func(graphql.ResolveParams) (any, error) {
	return func(p graphql.ResolveParams) (any, error) {
		r, _ := p.Source.(*calc.EnergyReport)
		if r == nil {
			return nil, nil
		}
		return get(r), nil
	}
}
