// SPDX-License-Identifier: AGPL-3.0-only

package sdkgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// skipped lists the operations the SDKs leave out: routes called by providers
// or by the operator's tooling, never by a tenant's backend.
var skipped = map[string]string{
	"oauthCallback": "the provider redirects the end user's browser here",
	"inboundProbe":  "providers probe their notification URL",
	"inbound":       "providers post their notifications here",
	"mcp":           "AI agents speak MCP to it, see docs/mcp.md",
	"metrics":       "Prometheus scrapes it",
	"openapi":       "the document the SDKs are generated from",

	"connectPage":         "the end user's browser opens the connect link",
	"connectProviderPage": "the end user's browser opens the connect link",
	"connectStart":        "the connect page's form posts here",
}

// API is the part of the OpenAPI document the emitters need, in document
// order.
type API struct {
	Title   string
	Version string
	Doc     string
	Tags    []string
	Types   []*Named
	Ops     []*Operation
}

// Kind is the shape of a Type.
type Kind int

// The kinds of Type.
const (
	KindAny Kind = iota
	KindString
	KindInteger
	KindNumber
	KindBoolean
	KindArray   // Items
	KindMap     // string keys, Items values
	KindRef     // Ref, a Named type
	KindEnum    // Enum, string values
	KindLiteral // Literal, one string value
	KindUnion   // Variants
)

// Type is a language-neutral type expression.
type Type struct {
	Kind     Kind
	Ref      string
	Enum     []string
	Literal  string
	Items    *Type
	Variants []*Type
	Nullable bool
}

// Named is a type the emitters declare: an object with fields, or an alias.
type Named struct {
	Name   string
	Doc    string
	Fields []*Field // an object when Alias is nil
	Alias  *Type
	line   int // position in the document, for ordering
}

// Field is a member of a Named object.
type Field struct {
	Name     string
	Type     *Type
	Required bool
	Doc      string
}

// Param is a path, query or header parameter of an operation.
type Param struct {
	Name     string // on the wire
	Ident    string // snake_case, the emitters derive their names from it
	In       string // path, query or header
	Required bool
	Type     *Type
	Doc      string
	Schema   *openapi3.Schema
}

// Body is the request body of an operation.
type Body struct {
	Binary      bool
	ContentType string
	Type        *Type // JSON bodies only
	Required    bool
	Schema      *openapi3.Schema
}

// Result kinds.
const (
	ResultNone   = "none"   // 204, nothing to read
	ResultJSON   = "json"   // a JSON document
	ResultBinary = "binary" // a file (CSV, Parquet)
)

// Operation is one API call the SDKs expose.
type Operation struct {
	ID          string
	Method      string
	Path        string
	Tag         string
	Summary     string
	Description string
	PathParams  []*Param
	QueryParams []*Param
	Headers     []*Param
	Idempotent  bool // honours Idempotency-Key
	Body        *Body
	Result      string
	ResultType  *Type  // ResultJSON only
	Paged       bool   // a cursor list: data and next_cursor
	ItemType    *Type  // the element of data when Paged
	Auth        string // "tenant", "user" or "none"
	line        int
}

// Load reads the OpenAPI document at path and builds the API model.
func Load(path string) (*API, error) {
	loader := openapi3.NewLoader()
	loader.IncludeOrigin = true
	doc, err := loader.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("%s is not a valid document: %w", path, err)
	}
	return build(doc)
}

type builder struct {
	doc   *openapi3.T
	named map[string]*Named
	inOps bool // building operations: their inline types go after the components
}

// opsOffset orders the types written in place in an operation after every
// component schema.
const opsOffset = 1 << 24

func build(doc *openapi3.T) (*API, error) {
	b := &builder{doc: doc, named: map[string]*Named{}}
	api := &API{Title: doc.Info.Title, Version: doc.Info.Version, Doc: doc.Info.Description}
	for _, t := range doc.Tags {
		api.Tags = append(api.Tags, t.Name)
	}

	names := make([]string, 0, len(doc.Components.Schemas))
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := b.component(name); err != nil {
			return nil, err
		}
	}

	b.inOps = true
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, path)
			}
			if _, skip := skipped[op.OperationID]; skip {
				continue
			}
			o, err := b.operation(path, method, item, op)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", op.OperationID, err)
			}
			api.Ops = append(api.Ops, o)
		}
	}
	sort.SliceStable(api.Ops, func(i, j int) bool { return api.Ops[i].line < api.Ops[j].line })

	for _, n := range b.named {
		api.Types = append(api.Types, n)
	}
	sort.Slice(api.Types, func(i, j int) bool {
		if api.Types[i].line != api.Types[j].line {
			return api.Types[i].line < api.Types[j].line
		}
		return api.Types[i].Name < api.Types[j].Name
	})
	return api, nil
}

func line(o *openapi3.Origin) int {
	if o == nil || o.Key == nil {
		return 0
	}
	return o.Key.Line
}

const componentPrefix = "#/components/schemas/"

// component declares the component schema name, once.
func (b *builder) component(name string) (*Named, error) {
	if n, ok := b.named[name]; ok {
		return n, nil
	}
	ref, ok := b.doc.Components.Schemas[name]
	if !ok {
		return nil, fmt.Errorf("no component schema %q", name)
	}
	n := &Named{Name: name, Doc: ref.Value.Description, line: line(ref.Value.Origin)}
	b.named[name] = n // before the fields, for recursive schemas
	if err := b.fill(n, ref.Value); err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	return n, nil
}

// inline declares a type for an object schema written in place, under a name
// derived from where it stands.
func (b *builder) inline(name string, s *openapi3.Schema) (*Named, error) {
	if _, ok := b.named[name]; ok {
		return nil, fmt.Errorf("type name %s is used twice", name)
	}
	n := &Named{Name: name, Doc: s.Description, line: line(s.Origin)}
	if b.inOps {
		n.line += opsOffset
	}
	b.named[name] = n
	if err := b.fill(n, s); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

// fill sets the fields of an object schema (allOf flattened), or the alias of
// any other.
func (b *builder) fill(n *Named, s *openapi3.Schema) error {
	if !isObject(s) {
		t, err := b.typeOf(&openapi3.SchemaRef{Value: s}, n.Name)
		if err != nil {
			return err
		}
		n.Alias = t
		return nil
	}
	props, required, err := b.flatten(s)
	if err != nil {
		return err
	}
	if len(props) == 0 {
		// A free-form object: a map.
		t, err := b.typeOf(&openapi3.SchemaRef{Value: s}, n.Name)
		if err != nil {
			return err
		}
		n.Alias = t
		return nil
	}
	for _, p := range props {
		t, err := b.typeOf(p.ref, n.Name+pascal(singular(p.name)))
		if err != nil {
			return fmt.Errorf("field %s: %w", p.name, err)
		}
		n.Fields = append(n.Fields, &Field{
			Name:     p.name,
			Type:     t,
			Required: required[p.name],
			Doc:      p.ref.Value.Description,
		})
	}
	return nil
}

type prop struct {
	name string
	ref  *openapi3.SchemaRef
	line int
}

// flatten merges allOf into one property list in document order, a later
// member overriding a property of an earlier one.
func (b *builder) flatten(s *openapi3.Schema) ([]prop, map[string]bool, error) {
	required := map[string]bool{}
	var out []prop
	index := map[string]int{}
	add := func(name string, ref *openapi3.SchemaRef) {
		if i, ok := index[name]; ok {
			out[i].ref = overlay(out[i].ref, ref)
			return
		}
		index[name] = len(out)
		out = append(out, prop{name: name, ref: ref, line: line(ref.Origin)})
	}
	var walk func(s *openapi3.Schema) error
	walk = func(s *openapi3.Schema) error {
		for _, m := range s.AllOf {
			if err := walk(m.Value); err != nil {
				return err
			}
		}
		for _, r := range s.Required {
			required[r] = true
		}
		own := make([]prop, 0, len(s.Properties))
		for name, ref := range s.Properties {
			own = append(own, prop{name: name, ref: ref, line: line(ref.Origin)})
		}
		sort.Slice(own, func(i, j int) bool {
			if own[i].line != own[j].line {
				return own[i].line < own[j].line
			}
			return own[i].name < own[j].name
		})
		for _, p := range own {
			add(p.name, p.ref)
		}
		return nil
	}
	if err := walk(s); err != nil {
		return nil, nil, err
	}
	return out, required, nil
}

// overlay merges a property redeclared by a later allOf member: the later
// declaration wins, keeping the earlier type when it states none (PageBase
// says data is an array, a page names the items).
func overlay(prev, next *openapi3.SchemaRef) *openapi3.SchemaRef {
	if next.Ref != "" || next.Value.Type != nil || prev.Value == nil {
		return next
	}
	merged := *next.Value
	merged.Type = prev.Value.Type
	if merged.Description == "" {
		merged.Description = prev.Value.Description
	}
	return &openapi3.SchemaRef{Value: &merged, Origin: prev.Origin}
}

func isObject(s *openapi3.Schema) bool {
	if len(s.AllOf) > 0 || len(s.Properties) > 0 {
		return true
	}
	return s.Type != nil && s.Type.Includes(openapi3.TypeObject)
}

// typeOf converts a schema to a Type. hint names the type declared for an
// object schema written in place.
func (b *builder) typeOf(ref *openapi3.SchemaRef, hint string) (*Type, error) {
	if ref == nil || ref.Value == nil {
		return &Type{Kind: KindAny}, nil
	}
	if ref.Ref != "" {
		name, ok := strings.CutPrefix(ref.Ref, componentPrefix)
		if !ok {
			return nil, fmt.Errorf("unsupported $ref %s", ref.Ref)
		}
		if _, err := b.component(name); err != nil {
			return nil, err
		}
		return &Type{Kind: KindRef, Ref: name}, nil
	}
	s := ref.Value
	nullable := s.Nullable || (s.Type != nil && s.Type.IncludesNull())
	t, err := b.shape(s, hint)
	if err != nil {
		return nil, err
	}
	t.Nullable = t.Nullable || nullable
	return t, nil
}

func (b *builder) shape(s *openapi3.Schema, hint string) (*Type, error) {
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
		variants := append(append(openapi3.SchemaRefs{}, s.OneOf...), s.AnyOf...)
		var out []*Type
		plain := true // every variant a plain string (a date or a date-time)
		for _, v := range variants {
			t, err := b.typeOf(v, hint)
			if err != nil {
				return nil, err
			}
			if t.Kind != KindString {
				plain = false
			}
			out = append(out, t)
		}
		if plain {
			return &Type{Kind: KindString}, nil
		}
		return &Type{Kind: KindUnion, Variants: out}, nil
	}
	if c, ok := s.Const.(string); ok {
		return &Type{Kind: KindLiteral, Literal: c}, nil
	}
	if len(s.Enum) > 0 {
		t := &Type{Kind: KindEnum}
		for _, v := range s.Enum {
			if v == nil {
				// A nullable enum lists null; the type list already says so.
				continue
			}
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("enum value %v is not a string", v)
			}
			t.Enum = append(t.Enum, str)
		}
		return t, nil
	}
	if isObject(s) {
		if len(s.Properties) > 0 || len(s.AllOf) > 0 {
			n, err := b.inline(hint, s)
			if err != nil {
				return nil, err
			}
			return &Type{Kind: KindRef, Ref: n.Name}, nil
		}
		values := &Type{Kind: KindAny}
		if ap := s.AdditionalProperties.Schema; ap != nil {
			v, err := b.typeOf(ap, hint+"Value")
			if err != nil {
				return nil, err
			}
			values = v
		}
		return &Type{Kind: KindMap, Items: values}, nil
	}
	if s.Items != nil || (s.Type != nil && s.Type.Includes(openapi3.TypeArray)) {
		items, err := b.typeOf(s.Items, hint)
		if err != nil {
			return nil, err
		}
		return &Type{Kind: KindArray, Items: items}, nil
	}
	switch {
	case s.Type == nil:
		return &Type{Kind: KindAny}, nil
	case s.Type.Includes(openapi3.TypeString):
		return &Type{Kind: KindString}, nil
	case s.Type.Includes(openapi3.TypeInteger):
		return &Type{Kind: KindInteger}, nil
	case s.Type.Includes(openapi3.TypeNumber):
		return &Type{Kind: KindNumber}, nil
	case s.Type.Includes(openapi3.TypeBoolean):
		return &Type{Kind: KindBoolean}, nil
	}
	return &Type{Kind: KindAny}, nil
}

func (b *builder) operation(path, method string, item *openapi3.PathItem, op *openapi3.Operation) (*Operation, error) {
	o := &Operation{
		ID:          op.OperationID,
		Method:      method,
		Path:        path,
		Summary:     op.Summary,
		Description: strings.TrimSpace(op.Description),
		Result:      ResultNone,
		Auth:        "tenant",
		line:        line(op.Origin),
	}
	if len(op.Tags) > 0 {
		o.Tag = op.Tags[0]
	}
	if op.Security != nil {
		switch {
		case len(*op.Security) == 0:
			o.Auth = "none"
		default:
			if _, ok := (*op.Security)[0]["userToken"]; ok {
				o.Auth = "user"
			}
		}
	}

	params := append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...)
	for _, pr := range params {
		p := pr.Value
		if p.In == openapi3.ParameterInHeader && p.Name == "Idempotency-Key" {
			o.Idempotent = true
			continue
		}
		t, err := b.typeOf(p.Schema, pascal(o.ID)+pascal(p.Name))
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", p.Name, err)
		}
		param := &Param{
			Name:     p.Name,
			In:       p.In,
			Required: p.Required,
			Type:     t,
			Doc:      strings.TrimSpace(p.Description),
			Schema:   p.Schema.Value,
		}
		switch p.In {
		case openapi3.ParameterInPath:
			param.Ident = pathIdent(path, p.Name)
			o.PathParams = append(o.PathParams, param)
		case openapi3.ParameterInQuery:
			param.Ident = snake(p.Name)
			o.QueryParams = append(o.QueryParams, param)
		case openapi3.ParameterInHeader:
			param.Ident = snake(strings.TrimPrefix(p.Name, "X-"))
			o.Headers = append(o.Headers, param)
		default:
			return nil, fmt.Errorf("parameter %s in %s is not supported", p.Name, p.In)
		}
	}
	// Path parameters in the order of the path.
	sort.SliceStable(o.PathParams, func(i, j int) bool {
		return strings.Index(path, "{"+o.PathParams[i].Name+"}") < strings.Index(path, "{"+o.PathParams[j].Name+"}")
	})
	// Required parameters first, each group in document order.
	for _, list := range [][]*Param{o.QueryParams, o.Headers} {
		sort.SliceStable(list, func(i, j int) bool { return list[i].Required && !list[j].Required })
	}

	if rb := op.RequestBody; rb != nil && rb.Value != nil {
		body, err := b.body(o, rb.Value)
		if err != nil {
			return nil, err
		}
		o.Body = body
	}
	if err := b.result(o, op); err != nil {
		return nil, err
	}
	return o, nil
}

func (b *builder) body(o *Operation, rb *openapi3.RequestBody) (*Body, error) {
	if mt := rb.Content.Get("application/json"); mt != nil {
		t, err := b.typeOf(mt.Schema, pascal(o.ID)+"Request")
		if err != nil {
			return nil, fmt.Errorf("request body: %w", err)
		}
		return &Body{ContentType: "application/json", Type: t, Required: rb.Required, Schema: mt.Schema.Value}, nil
	}
	types := make([]string, 0, len(rb.Content))
	for ct := range rb.Content {
		types = append(types, ct)
	}
	sort.Strings(types)
	if len(types) == 0 {
		return nil, fmt.Errorf("request body without content")
	}
	return &Body{Binary: true, ContentType: types[0], Required: rb.Required}, nil
}

// result reads the first 2xx response.
func (b *builder) result(o *Operation, op *openapi3.Operation) error {
	codes := make([]string, 0)
	for code := range op.Responses.Map() {
		if strings.HasPrefix(code, "2") {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	if len(codes) == 0 {
		return fmt.Errorf("no 2xx response")
	}
	resp := op.Responses.Value(codes[0]).Value
	if len(resp.Content) == 0 {
		return nil
	}
	for ct, mt := range resp.Content {
		if ct == "application/json" || strings.HasSuffix(ct, "+json") {
			t, err := b.typeOf(mt.Schema, pascal(o.ID)+"Response")
			if err != nil {
				return fmt.Errorf("response: %w", err)
			}
			o.Result, o.ResultType = ResultJSON, t
			b.paged(o)
			return nil
		}
	}
	o.Result = ResultBinary
	return nil
}

// paged marks a list read with cursor and answered with data and next_cursor.
func (b *builder) paged(o *Operation) {
	hasCursor := false
	for _, p := range o.QueryParams {
		hasCursor = hasCursor || p.Name == "cursor"
	}
	if !hasCursor || o.ResultType.Kind != KindRef {
		return
	}
	n := b.named[o.ResultType.Ref]
	var data, next *Field
	for _, f := range n.Fields {
		switch f.Name {
		case "data":
			data = f
		case "next_cursor":
			next = f
		}
	}
	if data != nil && next != nil && data.Type.Kind == KindArray {
		o.Paged, o.ItemType = true, data.Type.Items
	}
}

// pathIdent names a path parameter. A bare {id} takes the name of the
// collection before it: /users/{id} is user_id.
func pathIdent(path, name string) string {
	if name != "id" {
		return snake(name)
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == "{id}" && i > 0 {
			return snake(singular(segs[i-1])) + "_id"
		}
	}
	return "id"
}

// IterName is the name of the iterator over a paged operation: the leading
// verb becomes iter (listSamples, iterSamples).
func IterName(opID string) string {
	for _, verb := range []string{"list", "get", "search"} {
		if rest, ok := strings.CutPrefix(opID, verb); ok && rest != "" {
			return "iter" + rest
		}
	}
	return "iter" + pascal(opID)
}
