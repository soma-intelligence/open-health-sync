// SPDX-License-Identifier: AGPL-3.0-only

package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

func init() {
	// Device uploads are gzip bytes; validate them as an opaque file.
	openapi3filter.RegisterBodyDecoder("application/gzip", openapi3filter.FileBodyDecoder)
	// Parquet exports are binary files.
	openapi3filter.RegisterBodyDecoder("application/vnd.apache.parquet", openapi3filter.FileBodyDecoder)
	// The user archive is a zip file.
	openapi3filter.RegisterBodyDecoder("application/zip", openapi3filter.FileBodyDecoder)
	// FHIR responses are JSON under their own media type.
	openapi3filter.RegisterBodyDecoder("application/fhir+json", openapi3filter.JSONBodyDecoder)
}

// Call is one recorded exchange.
type Call struct {
	Method, Path string
	Status       int
	Operation    string
	Problems     []string
}

// Recorder is an http.RoundTripper that sends each request, then validates the
// request and its response against the OpenAPI document. It never fails a
// call: problems are recorded for the test to report.
type Recorder struct {
	Next   http.RoundTripper
	doc    *openapi3.T
	router routers.Router

	mu    sync.Mutex
	calls []Call
}

// NewRecorder loads the document and routes requests to it for serverURL.
func NewRecorder(ctx context.Context, specPath, serverURL string) (*Recorder, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", specPath, err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("%s is not a valid document: %w", specPath, err)
	}
	// Route against the test server instead of the documented one.
	doc.Servers = openapi3.Servers{{URL: serverURL}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	return &Recorder{Next: http.DefaultTransport, doc: doc, router: router}, nil
}

// Operations lists every operationId of the document.
func (r *Recorder) Operations() []string {
	var ids []string
	for _, item := range r.doc.Paths.Map() {
		for _, op := range item.Operations() {
			ids = append(ids, op.OperationID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Calls returns what was recorded so far.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	res, err := r.Next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resBody, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(resBody))

	call := Call{Method: req.Method, Path: req.URL.Path, Status: res.StatusCode}
	call.Problems, call.Operation = r.validate(req, reqBody, res, resBody)
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	return res, nil
}

func (r *Recorder) validate(req *http.Request, reqBody []byte, res *http.Response, resBody []byte) ([]string, string) {
	ctx := req.Context()
	vreq := req.Clone(ctx)
	vreq.Body = io.NopCloser(bytes.NewReader(reqBody))
	route, params, err := r.router.FindRoute(vreq)
	if err != nil {
		return []string{"undocumented route: " + err.Error()}, ""
	}
	var problems []string
	opts := &openapi3filter.Options{
		MultiError:            true,
		IncludeResponseStatus: true,
		AuthenticationFunc:    bearerPresent,
	}
	in := &openapi3filter.RequestValidationInput{Request: vreq, PathParams: params, Route: route, Options: opts}
	// A request the server refused with a 4xx may be invalid on purpose (the
	// suite sends a few); its response is still checked below.
	if err := openapi3filter.ValidateRequest(ctx, in); err != nil && (res.StatusCode < 400 || res.StatusCode >= 500) {
		problems = append(problems, "request: "+flatten(err))
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in,
		Status:                 res.StatusCode,
		Header:                 res.Header,
		Options:                opts,
	}
	out.SetBodyBytes(resBody)
	if err := openapi3filter.ValidateResponse(ctx, out); err != nil {
		problems = append(problems, "response: "+flatten(err))
	}
	problems = append(problems, undocumentedFields(route.Operation, res, resBody)...)
	return problems, route.Operation.OperationID
}

// bearerPresent accepts any request that carries a bearer credential: the
// server decides whether it is valid, the contract only checks the shape.
func bearerPresent(_ context.Context, in *openapi3filter.AuthenticationInput) error {
	if strings.HasPrefix(in.RequestValidationInput.Request.Header.Get("Authorization"), "Bearer ") {
		return nil
	}
	return fmt.Errorf("no bearer credential for %s", in.SecuritySchemeName)
}

func flatten(err error) string {
	if me, ok := err.(openapi3.MultiError); ok {
		parts := make([]string, 0, len(me))
		for _, e := range me {
			parts = append(parts, flatten(e))
		}
		return strings.Join(parts, "; ")
	}
	return strings.ReplaceAll(err.Error(), "\n", " ")
}

// undocumentedFields reports, once per path ($.data[].x), every object member of a JSON response that the
// response schema does not name, unless the schema allows extra members
// (additionalProperties) or describes a free-form object (no properties).
// kin-openapi accepts unknown members by default, as JSON Schema does.
func undocumentedFields(op *openapi3.Operation, res *http.Response, body []byte) []string {
	ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if ct != "application/json" && ct != "application/problem+json" && ct != "application/fhir+json" {
		return nil
	}
	ref := op.Responses.Status(res.StatusCode)
	if ref == nil || ref.Value == nil {
		return nil // reported as an undocumented status already
	}
	mt := ref.Value.Content.Get(ct)
	if mt == nil || mt.Schema == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return []string{"response: not JSON: " + err.Error()}
	}
	var found []string
	walk(v, []*openapi3.Schema{mt.Schema.Value}, "$", &found)
	// One line per field path, not per array element.
	sort.Strings(found)
	return slices.Compact(found)
}

// expand flattens allOf so the members of every branch count.
func expand(ss []*openapi3.Schema) []*openapi3.Schema {
	var out []*openapi3.Schema
	for _, s := range ss {
		if s == nil {
			continue
		}
		out = append(out, s)
		for _, a := range s.AllOf {
			out = append(out, expand([]*openapi3.Schema{a.Value})...)
		}
	}
	return out
}

// branchOf picks, among the oneOf or anyOf branches of s, the ones whose
// resourceType constant is the object's (a FHIR resource). It returns nil
// when the branches are not told apart that way.
func branchOf(s *openapi3.Schema, val map[string]any) []*openapi3.Schema {
	rt, ok := val["resourceType"].(string)
	if !ok {
		return nil
	}
	var out []*openapi3.Schema
	for _, b := range append(append(openapi3.SchemaRefs{}, s.OneOf...), s.AnyOf...) {
		if b.Value == nil {
			return nil
		}
		p := b.Value.Properties["resourceType"]
		if p == nil || p.Value == nil || p.Value.Const == nil {
			return nil
		}
		if p.Value.Const == rt {
			out = append(out, b.Value)
		}
	}
	return out
}

func walk(v any, schemas []*openapi3.Schema, path string, out *[]string) {
	ss := expand(schemas)
	if val, ok := v.(map[string]any); ok {
		for _, s := range ss {
			ss = append(ss, expand(branchOf(s, val))...)
		}
	}
	switch val := v.(type) {
	case map[string]any:
		props := map[string][]*openapi3.Schema{}
		named, open := false, false
		var extra []*openapi3.Schema
		for _, s := range ss {
			if (len(s.OneOf) > 0 || len(s.AnyOf) > 0) && branchOf(s, val) == nil {
				open = true // not resolved here; kin-openapi validates the branch
			}
			for name, p := range s.Properties {
				named = true
				props[name] = append(props[name], p.Value)
			}
			ap := s.AdditionalProperties
			if ap.Has != nil && *ap.Has {
				open = true
			}
			if ap.Schema != nil {
				extra = append(extra, ap.Schema.Value)
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := path + "." + k
			switch {
			case len(props[k]) > 0:
				walk(val[k], props[k], child, out)
			case len(extra) > 0:
				walk(val[k], extra, child, out)
			case open || !named:
			default:
				*out = append(*out, "response: undocumented field "+child)
			}
		}
	case []any:
		var items []*openapi3.Schema
		for _, s := range ss {
			if s.Items != nil {
				items = append(items, s.Items.Value)
			}
		}
		if len(items) == 0 {
			return
		}
		for _, e := range val {
			walk(e, items, path+"[]", out)
		}
	}
}
