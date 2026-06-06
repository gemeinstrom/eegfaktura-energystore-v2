package graphqlapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"

	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/auth"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/calc"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/counterpoint"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/queryengine"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/store"
	"github.com/gemeinstrom/eegfaktura-energystore-v2/internal/xlsximport"
)

// Engine wires the schema + dependencies for the GraphQL endpoint.
type Engine struct {
	schema   graphql.Schema
	logger   *slog.Logger
	store    *store.Store
	calc     *calc.Engine
	qe       *queryengine.Engine
	repo     *counterpoint.Repository
}

// Options groups dependencies; nil-safe defaults apply.
type Options struct {
	Logger *slog.Logger
}

// New builds the GraphQL engine. The schema closes over the
// store/calc/queryengine/repo so the resolvers don't need to walk
// context.
func New(st *store.Store, calcEng *calc.Engine, qe *queryengine.Engine,
	repo *counterpoint.Repository, opts Options) (*Engine, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	e := &Engine{
		logger: logger.With("component", "graphql"),
		store:  st,
		calc:   calcEng,
		qe:     qe,
		repo:   repo,
	}
	schema, err := graphql.NewSchema(graphql.SchemaConfig{
		Query: graphql.NewObject(graphql.ObjectConfig{
			Name: "Query",
			Fields: graphql.Fields{
				"lastEnergyDate": {
					Type: graphql.NewNonNull(graphql.String),
					Args: graphql.FieldConfigArgument{
						"tenant": {Type: graphql.NewNonNull(graphql.String)},
						"ecId":   {Type: graphql.NewNonNull(graphql.String)},
					},
					Resolve: e.resolveLastEnergyDate,
				},
				"report": {
					Type: graphql.NewNonNull(eegEnergyType),
					Args: graphql.FieldConfigArgument{
						"tenant":  {Type: graphql.NewNonNull(graphql.String)},
						"ecId":    {Type: graphql.NewNonNull(graphql.String)},
						"year":    {Type: graphql.NewNonNull(graphql.Int)},
						"segment": {Type: graphql.NewNonNull(graphql.Int)},
						"period":  {Type: graphql.NewNonNull(graphql.String)},
					},
					Resolve: e.resolveReport,
				},
			},
		}),
		Mutation: graphql.NewObject(graphql.ObjectConfig{
			Name: "Mutation",
			Fields: graphql.Fields{
				"singleUpload": {
					Type: graphql.NewNonNull(graphql.Boolean),
					Args: graphql.FieldConfigArgument{
						"tenant": {Type: graphql.NewNonNull(graphql.String)},
						"ecId":   {Type: graphql.NewNonNull(graphql.String)},
						"sheet":  {Type: graphql.NewNonNull(graphql.String)},
						"file":   {Type: graphql.NewNonNull(uploadScalar)},
					},
					Resolve: e.resolveSingleUpload,
				},
			},
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("graphqlapi: build schema: %w", err)
	}
	e.schema = schema
	return e, nil
}

// uploadScalar is a no-op scalar — file payloads enter via the
// multipart-spec parsing in ServeHTTP rather than the regular variable
// channel. Defining it as a JSON-passthrough scalar means GraphQL's
// type checker accepts the placeholder value put on the operation.
var uploadScalar = graphql.NewScalar(graphql.ScalarConfig{
	Name:         "Upload",
	Serialize:    func(v any) any { return v },
	ParseValue:   func(v any) any { return v },
	ParseLiteral: func(_ ast.Value) any { return nil },
})

// uploadedFile is what resolveSingleUpload looks for in the context.
type uploadKey struct{}

type uploadedFile struct {
	Filename string
	Body     io.Reader
}

// ServeHTTP handles POST /query. Supports both regular application/json
// bodies and multipart/form-data bodies that follow the
// graphql-multipart-request-spec for file uploads.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		e.serveMultipart(w, r)
		return
	}
	e.serveJSON(w, r)
}

func (e *Engine) serveJSON(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := graphql.Do(graphql.Params{
		Schema:         e.schema,
		RequestString:  body.Query,
		OperationName:  body.OperationName,
		VariableValues: body.Variables,
		Context:        r.Context(),
	})
	respondGraphQL(w, res)
}

// serveMultipart parses graphql-multipart-request-spec uploads.
// Expected fields:
//
//	operations: JSON {query, operationName, variables}
//	map: JSON {"0": ["variables.file"]}
//	0: <file bytes>
//
// We extract the file, stash it on the request context, and run the
// query with a placeholder for the variables.file slot.
func (e *Engine) serveMultipart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	opsRaw := r.FormValue("operations")
	mapRaw := r.FormValue("map")
	if opsRaw == "" {
		http.Error(w, "missing operations part", http.StatusBadRequest)
		return
	}
	var ops struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(opsRaw), &ops); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var fileMap map[string][]string
	if mapRaw != "" {
		_ = json.Unmarshal([]byte(mapRaw), &fileMap)
	}

	// Attach the first uploaded file to the context — the
	// singleUpload resolver expects exactly one.
	var first *uploadedFile
	for key := range fileMap {
		fh, header, err := r.FormFile(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		first = &uploadedFile{Filename: header.Filename, Body: fh}
		defer func(f multipart.File) { _ = f.Close() }(fh)
		break
	}
	if ops.Variables == nil {
		ops.Variables = map[string]any{}
	}
	ops.Variables["file"] = "__upload__"

	ctx := r.Context()
	if first != nil {
		ctx = context.WithValue(ctx, uploadKey{}, first)
	}
	res := graphql.Do(graphql.Params{
		Schema:         e.schema,
		RequestString:  ops.Query,
		OperationName:  ops.OperationName,
		VariableValues: ops.Variables,
		Context:        ctx,
	})
	respondGraphQL(w, res)
}

func respondGraphQL(w http.ResponseWriter, res *graphql.Result) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// resolveLastEnergyDate maps to v1 services.GetLastEnergyEntry: returns
// the most recent slot timestamp across the EC.
func (e *Engine) resolveLastEnergyDate(p graphql.ResolveParams) (any, error) {
	tenant, _ := p.Args["tenant"].(string)
	ecid, _ := p.Args["ecId"].(string)
	if e.qe == nil {
		return "", errors.New("query engine not configured")
	}
	meta, err := e.qe.QueryMetaData(p.Context, tenant, ecid)
	if err != nil {
		return "", err
	}
	var latest int64
	for _, m := range meta {
		if m.PeriodEnd > latest {
			latest = m.PeriodEnd
		}
	}
	if latest == 0 {
		return "", nil
	}
	return fmt.Sprintf("%d", latest), nil
}

// resolveReport maps to v1 calculation.EnergyReport.
func (e *Engine) resolveReport(p graphql.ResolveParams) (any, error) {
	tenant, _ := p.Args["tenant"].(string)
	year, _ := p.Args["year"].(int)
	segment, _ := p.Args["segment"].(int)
	period, _ := p.Args["period"].(string)
	if e.calc == nil {
		return nil, errors.New("calc engine not configured")
	}
	return e.calc.EnergyReport(p.Context, tenant, year, segment, period)
}

// resolveSingleUpload runs the XLSX importer on the uploaded file.
func (e *Engine) resolveSingleUpload(p graphql.ResolveParams) (any, error) {
	tenant, _ := p.Args["tenant"].(string)
	ecid, _ := p.Args["ecId"].(string)
	sheet, _ := p.Args["sheet"].(string)
	if e.calc == nil || e.repo == nil || e.store == nil {
		return false, errors.New("dependencies not configured")
	}
	f, ok := p.Context.Value(uploadKey{}).(*uploadedFile)
	if !ok || f == nil {
		return false, errors.New("no file in multipart payload")
	}
	im := &xlsximport.Importer{
		Tenant:     tenant,
		ECID:       ecid,
		SheetName:  sheet,
		Repository: e.repo,
		Store:      e.store,
	}
	_, _, err := im.ImportReader(p.Context, f.Body)
	if err != nil {
		return false, err
	}
	return true, nil
}

// Used to keep claims param parity with v1 wrapper for callers that
// thread auth.HandlerFunc into the GraphQL route.
type authClaims = *auth.PlatformClaims

var _ = (authClaims)(nil)
