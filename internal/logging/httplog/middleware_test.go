package httplog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// harness builds a mux with the chain mounted, at debug so every level shows,
// under the module "app".
func harness(t *testing.T, register func(*http.ServeMux)) (*bytes.Buffer, http.Handler) {
	t.Helper()
	return harnessFor(t, "app", register)
}

// harnessFor is harness with the module named explicitly, so a test can check
// behaviour that depends on which module wrote the line (see F4: the "module"
// field must come from exactly one place regardless of module). The logger
// carries the module tag itself, exactly as logging.Module does in
// production: Middleware never adds "module" on its own, it only installs
// whatever logger it is given.
func harnessFor(t *testing.T, module string, register func(*http.ServeMux)) (*bytes.Buffer, http.Handler) {
	t.Helper()
	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.DebugLevel).With().Str("module", module).Logger()
	mux := http.NewServeMux()
	register(mux)
	return &buf, Middleware(logger, module)(mux)
}

func lastLineBytes(t *testing.T, buf *bytes.Buffer) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	return lines[len(lines)-1]
}

func lastLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(lastLineBytes(t, buf), &out); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	return out
}

func TestLevelByStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, "debug"}, {302, "debug"},
		{400, "info"}, {401, "info"}, {404, "info"},
		{500, "error"}, {503, "error"},
	}
	for _, tc := range cases {
		buf, h := harness(t, func(mux *http.ServeMux) {
			mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
		})
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

		if got := lastLine(t, buf)["level"]; got != tc.want {
			t.Fatalf("status %d logged at %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestLogsMatchedRouteAndNotRawPath(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /v2/dashboards/{id}", func(w http.ResponseWriter, r *http.Request) {})
	})
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest("GET", "/v2/dashboards/3f9a-secret-uuid", nil))

	line := lastLine(t, buf)
	if line["route"] != "GET /v2/dashboards/{id}" {
		t.Fatalf("route = %v, want the matched pattern", line["route"])
	}
	if bytes.Contains(buf.Bytes(), []byte("3f9a-secret-uuid")) {
		t.Fatalf("the raw path leaked into the log: %s", buf.String())
	}
}

func TestUnmatchedRouteIsLabelledAndDoesNotLeakThePath(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {})
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest("GET", "/no/such/thing/user-42", nil))

	if got := lastLine(t, buf)["route"]; got != routeUnmatched {
		t.Fatalf("route = %v, want %q", got, routeUnmatched)
	}
}

func TestRequestIDIsReusedAndEchoed(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {})
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(RequestIDHeader, "client-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "client-1" {
		t.Fatalf("echoed %q, want client-1", got)
	}
	if got := lastLine(t, buf)["request_id"]; got != "client-1" {
		t.Fatalf("logged %v, want client-1", got)
	}
}

func TestRequestIDIsMintedAndEchoedWhenAbsent(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	id := rec.Header().Get(RequestIDHeader)
	if id == "" {
		t.Fatal("no id echoed")
	}
	if got := lastLine(t, buf)["request_id"]; got != id {
		t.Fatalf("log says %v, header says %v", got, id)
	}
}

func TestHandlerSeesTheCorrelatedLoggerOnItsContext(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
			zerolog.Ctx(r.Context()).Info().Msg("from the handler")
		})
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(RequestIDHeader, "client-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !bytes.Contains(buf.Bytes(), []byte(`"request_id":"client-1"`)) {
		t.Fatalf("handler line lost the correlation: %s", buf.String())
	}
}

func TestUserIDWrittenByAuthReachesTheAccessLine(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		auth := func(next http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				// Exactly what RequireAuth does: a value on a CHILD context.
				SetUserID(r.Context(), "user-1")
				next(w, r.WithContext(context.WithValue(
					r.Context(), struct{ k string }{"sub"}, "user-1")))
			}
		}
		mux.HandleFunc("GET /x", auth(func(w http.ResponseWriter, r *http.Request) {}))
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	if got := lastLine(t, buf)["user_id"]; got != "user-1" {
		t.Fatalf("user_id = %v, want user-1", got)
	}
}

func TestIPPrefersForwardedFor(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {})
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := lastLine(t, buf)["remote_ip"]; got != "203.0.113.7" {
		t.Fatalf("remote_ip = %v, want the first hop", got)
	}
}

func TestIPFallsBackToRemoteAddr(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {})
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := lastLine(t, buf)["remote_ip"]; got != "10.0.0.1" {
		t.Fatalf("remote_ip = %v, want 10.0.0.1 without the port", got)
	}
}

func TestPanicBecomes500AndOneErrorLineWithAStack(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !bytes.Contains(buf.Bytes(), []byte("boom")) ||
		!bytes.Contains(buf.Bytes(), []byte(`"stack"`)) {
		t.Fatalf("no panic line with a stack: %s", buf.String())
	}
	if got := lastLine(t, buf)["level"]; got != "error" {
		t.Fatalf("access line for a panic logged at %v, want error", got)
	}
}

func TestFlusherAndHijackerSurviveTheWrapper(t *testing.T) {
	// Handlers type-assert http.Flusher behind an ok check for
	// a text/event-stream response, and WebSocket upgraders
	// need http.Hijacker. A hand-rolled ResponseWriter wrapper answers false to
	// both and breaks them silently.
	var sawFlusher bool
	_, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
			_, sawFlusher = w.(http.Flusher)
		})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	if !sawFlusher {
		t.Fatal("the wrapper hid http.Flusher, which breaks SSE")
	}
}

func TestNonMuxHandlerStillWorks(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
	plain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := Middleware(logger, "app")(plain)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", rec.Code)
	}
	if got := lastLine(t, &buf)["route"]; got != routeUnmatched {
		t.Fatalf("route = %v, want %q for a non-mux handler", got, routeUnmatched)
	}
}

// TestModuleFieldAppearsExactlyOnceAcrossModules is F4: hlog.NewHandler already
// seeds the request logger with "module", so accessLine must not add it a
// second time on any listener, whatever the module
// alike.
func TestModuleFieldAppearsExactlyOnceAcrossModules(t *testing.T) {
	for _, module := range []string{"app", "worker"} {
		buf, h := harnessFor(t, module, func(mux *http.ServeMux) {
			mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {})
		})
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

		line := lastLineBytes(t, buf)
		if count := bytes.Count(line, []byte(`"module":`)); count != 1 {
			t.Fatalf("module %q: \"module\" field appeared %d times, want 1: %s",
				module, count, line)
		}
		if got := lastLine(t, buf)["module"]; got != module {
			t.Fatalf("module = %v, want %v", got, module)
		}
	}
}

// TestDurationIsMilliseconds pins the field name and its unit. The value is a
// float with sub-millisecond precision, so a fast handler does not read 0.
func TestDurationIsMilliseconds(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Millisecond)
		})
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	line := lastLine(t, buf)
	if _, stale := line["duration_us"]; stale {
		t.Fatalf("duration_us is back: %v", line)
	}
	ms, ok := line["duration_ms"].(float64)
	if !ok {
		t.Fatalf("no duration_ms on the line: %v", line)
	}
	// A 2 ms sleep lands between 2 and well under 1000 (seconds would be 0.002).
	if ms < 2 || ms > 1000 {
		t.Fatalf("duration_ms = %v, want milliseconds around 2", ms)
	}
}

func TestRequestIDUnsafeReplaced(t *testing.T) {
	long := string(bytes.Repeat([]byte("a"), 65))
	for _, bad := range []string{"a\nb", long} {
		buf, h := harness(t, func(mux *http.ServeMux) {
			mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {})
		})
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header["X-Request-Id"] = []string{bad}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		id := rec.Header().Get(RequestIDHeader)
		if id == "" || id == bad {
			t.Fatalf("unsafe id %q was echoed as %q", bad, id)
		}
		if got := lastLine(t, buf)["request_id"]; got != id {
			t.Fatalf("log says %v, header says %v", got, id)
		}
	}
}

func TestRouteNeverRawPath(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /known", func(w http.ResponseWriter, r *http.Request) {})
	})
	h.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest("GET", "/x/9c1e7b-random-secret", nil))

	if got := lastLine(t, buf)["route"]; got != "unmatched" {
		t.Fatalf("route = %v, want unmatched", got)
	}
	if bytes.Contains(buf.Bytes(), []byte("9c1e7b-random-secret")) {
		t.Fatalf("the raw path leaked: %s", buf.String())
	}
}

func TestPanicGives500(t *testing.T) {
	buf, h := harness(t, func(mux *http.ServeMux) {
		mux.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var panics int
	for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		if m["msg"] == "http_panic" || m["message"] == "http_panic" {
			panics++
			if m["level"] != "error" || m["stack"] == nil {
				t.Fatalf("panic line needs level error and a stack: %v", m)
			}
		}
	}
	if panics != 1 {
		t.Fatalf("got %d panic lines, want 1: %s", panics, buf.String())
	}
}

func TestNoMetricsOption(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
	h := Middleware(logger, "app")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if got := lastLine(t, &buf)["status"]; got != float64(202) {
		t.Fatalf("access line status = %v, want 202", got)
	}
}
