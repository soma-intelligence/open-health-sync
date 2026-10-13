// SPDX-License-Identifier: AGPL-3.0-only

package sdkgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// The Postman Collection v2.1 format, the parts written here. Bruno and
// Insomnia import it as well.
type pmCollection struct {
	Info     pmInfo       `json:"info"`
	Auth     *pmAuth      `json:"auth,omitempty"`
	Variable []pmVariable `json:"variable"`
	Item     []pmFolder   `json:"item"`
}

type pmInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema"`
}

type pmAuth struct {
	Type   string       `json:"type"`
	Bearer []pmVariable `json:"bearer,omitempty"`
}

type pmVariable struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

type pmFolder struct {
	Name string   `json:"name"`
	Item []pmItem `json:"item"`
}

type pmItem struct {
	Name    string    `json:"name"`
	Request pmRequest `json:"request"`
}

type pmRequest struct {
	Method      string       `json:"method"`
	Description string       `json:"description,omitempty"`
	Auth        *pmAuth      `json:"auth,omitempty"`
	Header      []pmVariable `json:"header"`
	URL         pmURL        `json:"url"`
	Body        *pmBody      `json:"body,omitempty"`
}

type pmURL struct {
	Raw      string       `json:"raw"`
	Host     []string     `json:"host"`
	Path     []string     `json:"path"`
	Query    []pmVariable `json:"query,omitempty"`
	Variable []pmVariable `json:"variable,omitempty"`
}

type pmBody struct {
	Mode    string         `json:"mode"`
	Raw     string         `json:"raw,omitempty"`
	File    *pmFile        `json:"file,omitempty"`
	Options *pmBodyOptions `json:"options,omitempty"`
}

type pmFile struct {
	Src string `json:"src"`
}

type pmBodyOptions struct {
	Raw pmRawOptions `json:"raw"`
}

type pmRawOptions struct {
	Language string `json:"language"`
}

func bearer(variable string) *pmAuth {
	return &pmAuth{Type: "bearer", Bearer: []pmVariable{{Key: "token", Value: "{{" + variable + "}}", Type: "string"}}}
}

// Postman writes the collection: one folder per tag, one request per
// operation, with example values.
func Postman(api *API) ([]byte, error) {
	c := pmCollection{
		Info: pmInfo{
			Name: api.Title,
			Description: "Generated from api/openapi.yaml (version " + api.Version + ") by internal/sdkgen. " +
				"Set baseUrl and apiKey (a tenant API key, ohs_sk_...) on the collection; userToken " +
				"(ohs_ut_...) is used by the upload requests. See docs/api-collections.md.",
			Schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
		Auth: bearer("apiKey"),
		Variable: []pmVariable{
			{Key: "baseUrl", Value: "http://localhost:8080", Description: "The instance, without a trailing slash."},
			{Key: "apiKey", Value: "", Description: "Tenant API key, ohs_sk_..."},
			{Key: "userToken", Value: "", Description: "User token with the upload scope, ohs_ut_..., for the ingest requests."},
		},
	}

	pathVars := map[string]pmVariable{}
	folders := map[string]*pmFolder{}
	for _, tag := range api.Tags {
		folders[tag] = &pmFolder{Name: tag}
	}
	for _, op := range api.Ops {
		item, err := postmanItem(op, pathVars)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.ID, err)
		}
		f, ok := folders[op.Tag]
		if !ok {
			return nil, fmt.Errorf("%s: tag %q is not declared", op.ID, op.Tag)
		}
		f.Item = append(f.Item, item)
	}
	for _, tag := range api.Tags {
		if f := folders[tag]; len(f.Item) > 0 {
			c.Item = append(c.Item, *f)
		}
	}
	keys := make([]string, 0, len(pathVars))
	for k := range pathVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Variable = append(c.Variable, pathVars[k])
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func postmanItem(op *Operation, pathVars map[string]pmVariable) (pmItem, error) {
	req := pmRequest{
		Method:      strings.ToUpper(op.Method),
		Description: opDoc(op),
		Header:      []pmVariable{},
	}
	switch op.Auth {
	case "none":
		req.Auth = &pmAuth{Type: "noauth"}
	case "user":
		req.Auth = bearer("userToken")
	}

	segs := strings.Split(strings.TrimPrefix(op.Path, "/"), "/")
	for _, pp := range op.PathParams {
		v := camel(pp.Ident)
		for i, s := range segs {
			if s == "{"+pp.Name+"}" {
				segs[i] = ":" + pp.Name
			}
		}
		req.URL.Variable = append(req.URL.Variable, pmVariable{Key: pp.Name, Value: "{{" + v + "}}", Description: pp.Doc})
		if _, ok := pathVars[v]; !ok {
			pathVars[v] = pmVariable{Key: v, Value: exampleText(pp.Schema, false), Description: pathVarDoc(pp)}
		}
	}
	req.URL.Host = []string{"{{baseUrl}}"}
	req.URL.Path = segs
	raw := "{{baseUrl}}/" + strings.Join(segs, "/")
	var query []string
	for _, p := range op.QueryParams {
		q := pmVariable{Key: p.Name, Value: exampleText(p.Schema, true), Description: p.Doc, Disabled: !p.Required}
		req.URL.Query = append(req.URL.Query, q)
		if p.Required {
			query = append(query, p.Name+"="+q.Value)
		}
	}
	if len(query) > 0 {
		raw += "?" + strings.Join(query, "&")
	}
	req.URL.Raw = raw

	for _, h := range op.Headers {
		req.Header = append(req.Header, pmVariable{Key: h.Name, Value: exampleText(h.Schema, true), Description: h.Doc, Disabled: !h.Required})
	}
	if op.Idempotent {
		req.Header = append(req.Header, pmVariable{
			Key:         "Idempotency-Key",
			Value:       op.ID + "-0001",
			Description: "Optional. A retry with the same key and the same request gets the first response back.",
			Disabled:    true,
		})
	}
	if op.Body != nil {
		req.Header = append(req.Header, pmVariable{Key: "Content-Type", Value: op.Body.ContentType})
		if op.Body.Binary {
			req.Body = &pmBody{Mode: "file", File: &pmFile{Src: ""}}
		} else {
			ex, err := json.MarshalIndent(example(op.Body.Schema, 0), "", "  ")
			if err != nil {
				return pmItem{}, err
			}
			req.Body = &pmBody{Mode: "raw", Raw: string(ex), Options: &pmBodyOptions{Raw: pmRawOptions{Language: "json"}}}
		}
	}
	name := op.Summary
	if name == "" {
		name = op.ID
	}
	return pmItem{Name: name, Request: req}, nil
}

func pathVarDoc(p *Param) string {
	if p.Doc != "" {
		return p.Doc
	}
	return "The " + strings.ReplaceAll(p.Ident, "_", " ") + "."
}

// exampleText is an example value of a parameter, as text. A uuid has none: it
// is left for the caller to fill. inQuery gives a date range a sensible start.
func exampleText(s *openapi3.Schema, inQuery bool) string {
	if s == nil {
		return ""
	}
	v := example(s, 0)
	if v == nil {
		return ""
	}
	if str, ok := v.(string); ok {
		if (str == exampleUUID && !inQuery) || str == exampleString {
			return ""
		}
		return str
	}
	b, _ := json.Marshal(v)
	return string(b)
}

const (
	exampleUUID   = "00000000-0000-0000-0000-000000000000"
	exampleString = "example"
)

// example builds an example value from a schema: its examples, default, const
// or first enum value, else one made from its type and format.
func example(s *openapi3.Schema, depth int) any {
	if s == nil || depth > 6 {
		return nil
	}
	switch {
	case len(s.Examples) > 0:
		return s.Examples[0]
	case s.Example != nil:
		return s.Example
	case s.Default != nil:
		return s.Default
	case s.Const != nil:
		return s.Const
	case len(s.Enum) > 0:
		return s.Enum[0]
	case len(s.OneOf) > 0:
		return example(s.OneOf[0].Value, depth+1)
	}
	if len(s.Properties) > 0 || len(s.AllOf) > 0 {
		out := map[string]any{}
		for _, m := range s.AllOf {
			if mm, ok := example(m.Value, depth+1).(map[string]any); ok {
				for k, v := range mm {
					out[k] = v
				}
			}
		}
		for name, p := range s.Properties {
			out[name] = example(p.Value, depth+1)
		}
		return out
	}
	if s.Items != nil {
		return []any{example(s.Items.Value, depth+1)}
	}
	if s.Type == nil {
		return nil
	}
	switch {
	case s.Type.Includes(openapi3.TypeString):
		switch s.Format {
		case "uuid":
			return exampleUUID
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "date":
			return "2026-01-01"
		case "uri":
			return "https://app.example.com/health/connected"
		}
		return exampleString
	case s.Type.Includes(openapi3.TypeInteger):
		if s.Min != nil {
			return int(*s.Min)
		}
		return 1
	case s.Type.Includes(openapi3.TypeNumber):
		return 1.0
	case s.Type.Includes(openapi3.TypeBoolean):
		return true
	case s.Type.Includes(openapi3.TypeObject):
		return map[string]any{}
	}
	return nil
}
