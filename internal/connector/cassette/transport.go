// SPDX-License-Identifier: AGPL-3.0-only

package cassette

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// maxBody caps what the recorder reads of one response.
const maxBody = 64 << 20

// RawExchange is one exchange as it happened, before anonymization. It only
// ever lives in memory.
type RawExchange struct {
	Method, URL string
	ReqBody     []byte
	ReqType     string // request Content-Type
	Status      int
	Header      http.Header
	Body        []byte
}

// Recorder is an http.RoundTripper that passes requests to Next and, while
// it is on, keeps each exchange. Off, it records nothing (for the OAuth
// exchange, whose answers hold tokens).
type Recorder struct {
	Next http.RoundTripper

	mu        sync.Mutex
	on        bool
	exchanges []RawExchange
}

// NewRecorder returns a recorder in front of next (http.DefaultTransport
// when nil), switched off.
func NewRecorder(next http.RoundTripper) *Recorder {
	if next == nil {
		next = http.DefaultTransport
	}
	return &Recorder{Next: next}
}

// Client returns an *http.Client on the recorder.
func (r *Recorder) Client() *http.Client { return &http.Client{Transport: r} }

// Start switches recording on.
func (r *Recorder) Start() { r.mu.Lock(); r.on = true; r.mu.Unlock() }

// Stop switches recording off.
func (r *Recorder) Stop() { r.mu.Lock(); r.on = false; r.mu.Unlock() }

// Exchanges returns what was recorded, in order.
func (r *Recorder) Exchanges() []RawExchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RawExchange(nil), r.exchanges...)
}

// RoundTrip implements http.RoundTripper.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	on := r.on
	r.mu.Unlock()
	if !on {
		return r.Next.RoundTrip(req)
	}
	var reqBody []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	res, err := r.Next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	r.exchanges = append(r.exchanges, RawExchange{
		Method: req.Method, URL: req.URL.String(), ReqBody: reqBody, ReqType: req.Header.Get("Content-Type"),
		Status: res.StatusCode, Header: res.Header.Clone(), Body: body,
	})
	r.mu.Unlock()
	return res, nil
}

// Replayer is an http.RoundTripper that answers from a cassette and never
// touches the network. Each recorded exchange answers once, in recorded
// order among the exchanges of the same request.
type Replayer struct {
	mu     sync.Mutex
	queues map[string][]Response
	misses []string
}

// NewReplayer returns a transport answering from c.
func NewReplayer(c *Cassette) *Replayer {
	r := &Replayer{queues: map[string][]Response{}}
	for _, it := range c.Interactions {
		k := Key(it.Request.Method, it.Request.URL, it.Request.Body)
		r.queues[k] = append(r.queues[k], it.Response)
	}
	return r
}

// Client returns an *http.Client on the replayer.
func (r *Replayer) Client() *http.Client { return &http.Client{Transport: r} }

// RoundTrip implements http.RoundTripper.
func (r *Replayer) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		if bodyKept(req.Header.Get("Content-Type")) {
			body = string(b)
		}
	}
	k := Key(req.Method, req.URL.String(), body)
	r.mu.Lock()
	q := r.queues[k]
	if len(q) == 0 {
		r.misses = append(r.misses, k)
		r.mu.Unlock()
		return nil, fmt.Errorf("%w for %s", ErrNoAnswer, k)
	}
	ans := q[0]
	r.queues[k] = q[1:]
	r.mu.Unlock()

	h := http.Header{}
	for name, v := range ans.Header {
		h.Set(name, v)
	}
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", ans.Status, http.StatusText(ans.Status)),
		StatusCode: ans.Status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: io.NopCloser(bytes.NewReader(ans.Body)), ContentLength: int64(len(ans.Body)),
		Request: req,
	}, nil
}

// Misses lists the requests the cassette had no answer for: the connector
// asked for something the recording never saw.
func (r *Replayer) Misses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.misses...)
}

// Unused lists the recorded requests replay never asked for: the connector
// no longer asks for something the real API was asked.
func (r *Replayer) Unused() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for k, q := range r.queues {
		for range q {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// bodyKept reports whether a request body of this type is recorded (and so
// takes part in replay matching): forms and JSON only.
func bodyKept(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.Contains(ct, "json")
}
