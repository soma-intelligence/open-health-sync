// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpUsage = "usage: ohs mcp (reads OHS_MCP_URL, default OHS_PUBLIC_URL, and OHS_MCP_KEY)"

// runMCP serves MCP over stdin and stdout for a local agent, forwarding
// everything to the /mcp endpoint of an Open Health Sync instance.
//
// It is a proxy, not a second server: it connects to the remote server as an
// MCP client, re-exposes the tools that server lists, and forwards each call
// unchanged. The tool set, argument schemas, authorization and results are
// therefore defined once, on the server; a stdio server calling the REST API
// would have to duplicate all of it (and the REST API has no route for some
// tools). It needs no database or encryption key, only the URL and a secret.
func runMCP(rest []string, _ io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(rest); err != nil || fs.NArg() != 0 {
		return usageErr(mcpUsage)
	}
	endpoint, err := mcpEndpoint(os.Getenv)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(os.Getenv("OHS_MCP_KEY"))
	if key == "" {
		return errors.New("OHS_MCP_KEY is required: a tenant API key (ohs_sk_) or a user token with scope read (ohs_ut_)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return proxyMCP(ctx, endpoint, key, &drainTransport{Transport: &sdk.StdioTransport{}})
}

// drainTransport keeps a local connection open after its input ends until
// every request read so far has its response written. Without it, a client
// that writes its requests and closes stdin (a script, `ohs mcp < file`) gets
// nothing back: the SDK closes the session on EOF and cancels the calls still
// in flight.
type drainTransport struct {
	sdk.Transport
}

func (t *drainTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	d := &drainConn{Connection: conn, closed: make(chan struct{})}
	d.idle = sync.NewCond(&d.mu)
	return d, nil
}

// drainWait bounds the wait at EOF, so a response that never comes cannot
// hold the process forever.
const drainWait = proxyCallTimeout + 10*time.Second

type drainConn struct {
	sdk.Connection
	mu      sync.Mutex
	idle    *sync.Cond
	pending map[string]bool // ids of requests read and not yet answered
	closed  chan struct{}
	once    sync.Once
}

func (c *drainConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err == nil {
		if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
			c.mu.Lock()
			if c.pending == nil {
				c.pending = map[string]bool{}
			}
			c.pending[idKey(req.ID)] = true
			c.mu.Unlock()
		}
		return msg, nil
	}
	if errors.Is(err, io.EOF) {
		c.waitIdle(ctx)
	}
	return nil, err
}

func (c *drainConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	err := c.Connection.Write(ctx, msg)
	if resp, ok := msg.(*jsonrpc.Response); ok {
		c.mu.Lock()
		delete(c.pending, idKey(resp.ID))
		c.idle.Broadcast()
		c.mu.Unlock()
	}
	return err
}

func (c *drainConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		c.mu.Lock()
		c.idle.Broadcast()
		c.mu.Unlock()
	})
	return c.Connection.Close()
}

// waitIdle returns once no request is pending, the connection is closed, ctx
// ends, or drainWait has passed.
func (c *drainConn) waitIdle(ctx context.Context) {
	timer := time.AfterFunc(drainWait, func() {
		c.mu.Lock()
		c.pending = nil
		c.idle.Broadcast()
		c.mu.Unlock()
	})
	defer timer.Stop()
	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		c.idle.Broadcast()
		c.mu.Unlock()
	})
	defer stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.pending) > 0 && ctx.Err() == nil {
		select {
		case <-c.closed:
			return
		default:
		}
		c.idle.Wait()
	}
}

func idKey(id jsonrpc.ID) string {
	return fmt.Sprintf("%#v", id.Raw())
}

// mcpEndpoint is OHS_MCP_URL, else OHS_PUBLIC_URL, else the local default,
// with /mcp appended unless the URL already ends with it. Plain http is only
// accepted for a loopback host: anywhere else the bearer secret would cross
// the network in clear. Errors show the URL redacted, never a password.
func mcpEndpoint(getenv func(string) string) (*url.URL, error) {
	base := getenv("OHS_MCP_URL")
	if base == "" {
		base = getenv("OHS_PUBLIC_URL")
	}
	if base == "" {
		base = "http://localhost:8080"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, errors.New("OHS_MCP_URL is not a valid URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// Not echoed: a value without scheme parses as opaque, so Redacted
		// would not hide a password in it.
		return nil, errors.New("OHS_MCP_URL must be an http or https URL")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("OHS_MCP_URL %s sends the secret in clear: use https (plain http only for localhost)", u.Redacted())
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/mcp") {
		u.Path += "/mcp"
	}
	return u, nil
}

func loopback(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// bearerTransport signs requests to the configured endpoint's origin only.
type bearerTransport struct {
	base   http.RoundTripper
	scheme string
	host   string
	secret string
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.EqualFold(r.URL.Scheme, b.scheme) && strings.EqualFold(r.URL.Host, b.host) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+b.secret)
	}
	return b.base.RoundTrip(r)
}

// newProxyClient returns the HTTP client of `ohs mcp`: it signs requests to
// endpoint's origin and follows no redirect, so the secret cannot be carried
// to another host or downgraded to http. base nil means
// http.DefaultTransport.
func newProxyClient(endpoint *url.URL, secret string, base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport: bearerTransport{base: base, scheme: endpoint.Scheme, host: endpoint.Host, secret: secret},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: proxyCallTimeout,
	}
}

// statusRecorder keeps the status of the last response, so a failed
// handshake can say what to fix rather than "cannot reach".
type statusRecorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	code int
}

func (s *statusRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := s.base.RoundTrip(r)
	if err == nil {
		s.mu.Lock()
		s.code = res.StatusCode
		s.mu.Unlock()
	}
	return res, err
}

func (s *statusRecorder) last() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// mcpConnectHint explains the HTTP status that ended the handshake, or ""
// when there is nothing more useful to say than the error itself.
func mcpConnectHint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "refused OHS_MCP_KEY (401): check it, a user token may have expired, mint a new one"
	case http.StatusForbidden:
		return "refused OHS_MCP_KEY (403): a user token needs the scope read, or the request came from a browser origin"
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return fmt.Sprintf("has no MCP endpoint (%d): set OHS_MCP_URL to the base URL of the instance", status)
	}
	return ""
}

// proxyCallTimeout bounds one forwarded request.
const proxyCallTimeout = 2 * time.Minute

func proxyMCP(ctx context.Context, endpoint *url.URL, secret string, local sdk.Transport) error {
	shown := endpoint.Redacted()
	client := sdk.NewClient(&sdk.Implementation{Name: "ohs-mcp", Version: version}, nil)
	httpClient := newProxyClient(endpoint, secret, nil)
	seen := &statusRecorder{base: httpClient.Transport}
	httpClient.Transport = seen
	remote, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:   endpoint.String(),
		HTTPClient: httpClient,
		MaxRetries: -1,
	}, nil)
	if err != nil {
		// The SDK error carries the HTTP status (net/http strips URL
		// passwords from its own errors), never the bearer secret.
		if hint := mcpConnectHint(seen.last()); hint != "" {
			return fmt.Errorf("mcp: %s %s: %w", shown, hint, err)
		}
		return fmt.Errorf("mcp: cannot reach %s: %w", shown, err)
	}
	defer func() { _ = remote.Close() }()

	opts := &sdk.ServerOptions{}
	if init := remote.InitializeResult(); init != nil {
		opts.Instructions = init.Instructions
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "open-health-sync", Title: "Open Health Sync", Version: version}, opts)
	n := 0
	for tool, err := range remote.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("mcp: list tools of %s: %w", shown, err)
		}
		name := tool.Name
		srv.AddTool(tool, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			res, err := remote.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: req.Params.Arguments})
			if err != nil {
				// The instance did not answer: a tool error the agent can report.
				out := &sdk.CallToolResult{}
				out.SetError(fmt.Errorf("Open Health Sync did not answer: %w", err))
				return out, nil
			}
			return res, nil
		})
		n++
	}
	if n == 0 {
		return fmt.Errorf("mcp: %s lists no tools", shown)
	}
	return srv.Run(ctx, local)
}
