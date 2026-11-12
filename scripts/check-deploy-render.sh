#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Renders and validates everything under deploy/ that a unit test cannot see:
#
#   helm     lints the chart, renders it for every optional feature, checks
#            the manifests against the Kubernetes schemas (kubeconform) and
#            checks that what the pods run (`ohs serve --allow-newer-schema`,
#            the `/ohs doctor --quick` probe) exists in cmd/ohs and the image.
#   caddy    validates deploy/Caddyfile (and its commented second site block),
#            then runs Caddy in front of a dummy upstream and checks that the
#            security headers reach the client and that an event stream and a
#            WebSocket pass through unbuffered.
#   compose  renders deploy/docker-compose.yml and every overlay, and checks
#            that it refuses to render without POSTGRES_PASSWORD.
#
#   scripts/check-deploy-render.sh [helm] [caddy] [compose]
#
# No argument runs the three. Needs docker (with the compose plugin) and curl,
# nothing else: helm, kubeconform, caddy and python run from their official
# images. The repository is only read. The images are pinned by tag below;
# override one with its variable, for example HELM_IMAGE=alpine/helm:3.19.0.
# The schema check downloads the Kubernetes schemas, so it needs the network.
# Exit status: 0 every check passed, 1 at least one failed, 2 it could not run.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
HELM_IMAGE="${HELM_IMAGE:-alpine/helm:3.16.2}"
KUBECONFORM_IMAGE="${KUBECONFORM_IMAGE:-ghcr.io/yannh/kubeconform:v0.6.7}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-1.34.0}"
CADDY_IMAGE="${CADDY_IMAGE:-caddy:2.10.2-alpine}"
PYTHON_IMAGE="${PYTHON_IMAGE:-python:3.13-alpine}"
# The ServiceMonitor is a custom resource: its schema comes from this catalog.
CRD_SCHEMAS='https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

command -v docker > /dev/null || { echo "check-deploy-render: docker is required" >&2; exit 2; }
docker info > /dev/null 2>&1 || { echo "check-deploy-render: the docker daemon does not answer" >&2; exit 2; }

tmp="$(mktemp -d)"
id="ohs-render-$$"
failures=0
cleanup() {
  docker rm -f "$id-up" "$id-caddy" > /dev/null 2>&1
  docker network rm "$id" > /dev/null 2>&1
  rm -rf "$tmp"
}
trap cleanup EXIT

pass() { echo "ok    $*"; }
fail() { echo "FAIL  $*"; failures=$((failures + 1)); }
# check NAME COMMAND...: passes when the command succeeds.
check() {
  local name="$1"
  shift
  if "$@" > "$tmp/check.out" 2>&1; then pass "$name"; else
    fail "$name"
    sed 's/^/      /' "$tmp/check.out" | tail -n 15
  fi
}
# count PATTERN FILE: lines of FILE that match the fixed string PATTERN.
count() { grep -cF -- "$1" "$2" || true; }
# expect NAME WANT GOT
expect() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1: want $2, got $3"; fi; }

# ---------------------------------------------------------------------------
# helm
# ---------------------------------------------------------------------------

helm() { docker run --rm -v "$root/deploy/helm:/helm:ro" "$HELM_IMAGE" "$@"; }

# render NAME ARGS...: `helm template` into $tmp/NAME.yaml, then the schemas.
render() {
  local name="$1"
  shift
  if ! helm template ohs /helm/ohs "$@" > "$tmp/$name.yaml" 2> "$tmp/$name.err"; then
    fail "helm template ($name)"
    sed 's/^/      /' "$tmp/$name.err" | tail -n 5
    return
  fi
  if docker run --rm -i "$KUBECONFORM_IMAGE" -strict -summary -kubernetes-version "$KUBERNETES_VERSION" \
    -schema-location default -schema-location "$CRD_SCHEMAS" - < "$tmp/$name.yaml" > "$tmp/$name.conform" 2>&1 &&
    ! grep -q 'Skipped: [1-9]' "$tmp/$name.conform"; then
    pass "helm template + kubeconform $KUBERNETES_VERSION ($name): $(count 'kind: ' "$tmp/$name.yaml") objects"
  else
    fail "kubeconform ($name)"
    sed 's/^/      /' "$tmp/$name.conform" | tail -n 10
  fi
}

# refuse NAME MESSAGE ARGS...: the render must stop with MESSAGE.
refuse() {
  local name="$1" message="$2"
  shift 2
  if helm template ohs /helm/ohs "$@" > /dev/null 2> "$tmp/$name.err"; then
    fail "helm template ($name) rendered, it must refuse"
  elif grep -qF -- "$message" "$tmp/$name.err"; then
    pass "helm template ($name) refuses: $message"
  else
    fail "helm template ($name) failed without saying '$message'"
    sed 's/^/      /' "$tmp/$name.err" | tail -n 5
  fi
}

check_helm() {
  echo "== helm ($HELM_IMAGE)"
  local db="database.url=postgres://ohs:ohs@db:5432/ohs"
  local s3="rawStore.s3.url=s3://bucket?region=eu-west-3"
  local rwx="rawStore.persistence.accessMode=ReadWriteMany"

  check "helm lint (default values)" helm lint --strict /helm/ohs
  check "helm lint (database.url)" helm lint --strict /helm/ohs --set "$db"
  check "helm lint (every optional template)" helm lint --strict /helm/ohs -f /helm/ci/all-optional.yaml
  check "helm lint (every optional template, allowNewerSchema)" \
    helm lint --strict /helm/ohs -f /helm/ci/all-optional.yaml --set allowNewerSchema=true
  check "helm lint (kind values)" helm lint --strict /helm/ohs -f /helm/ci/kind.yaml

  render default --set "$db"
  render kind -f /helm/ci/kind.yaml
  render all-optional -f /helm/ci/all-optional.yaml
  render all-optional-newer-schema -f /helm/ci/all-optional.yaml --set allowNewerSchema=true
  render newer-schema --set "$db" --set allowNewerSchema=true
  render newer-schema-string-false --set "$db" --set-string allowNewerSchema=false
  render existing-secret --set existingSecret=my-secret
  render s3 --set "$db" --set rawStore.type=s3 --set "$s3" --set replicaCount=3
  render split-roles --set "$db" --set splitRoles.enabled=true --set "$rwx"
  render split-roles-no-probe --set "$db" --set splitRoles.enabled=true --set "$rwx" --set workerLivenessProbe=null
  render replicas-rwx --set "$db" --set replicaCount=3 --set "$rwx"
  render ingress --set "$db" --set ingress.enabled=true --set ingress.className=nginx
  render empty-dir --set "$db" --set rawStore.persistence.enabled=false
  render existing-claim --set "$db" --set rawStore.persistence.existingClaim=mine
  render autoscaling --set "$db" --set autoscaling.enabled=true --set podDisruptionBudget.enabled=true \
    --set rawStore.type=s3 --set "$s3"
  render network-policy --set "$db" --set networkPolicy.enabled=true
  render service-monitor --set "$db" --set serviceMonitor.enabled=true
  render no-probes --set "$db" --set startupProbe=null --set livenessProbe=null --set readinessProbe=null
  render no-migrations --set "$db" --set migrations.enabled=false
  render image-digest --set "$db" \
    --set image.digest=sha256:0000000000000000000000000000000000000000000000000000000000000000
  # Names YAML would read as a boolean or a number when left unquoted.
  render odd-names --set "$db" --set nameOverride=y --set-string serviceAccountName=123 \
    --set-string priorityClassName=true --set ingress.enabled=true --set-string ingress.className=no
  render long-name --set "$db" \
    --set fullnameOverride=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

  refuse no-database "set database.url"
  refuse s3-without-url "rawStore.s3.url is required" --set "$db" --set rawStore.type=s3
  refuse unknown-store "rawStore.type must be file or s3" --set "$db" --set rawStore.type=gcs
  refuse split-roles-rwo "needs a shared raw store" --set "$db" --set splitRoles.enabled=true
  refuse replicas-rwo "needs a shared raw store" --set "$db" --set replicaCount=2
  refuse autoscaling-rwo "needs a shared raw store" --set "$db" --set autoscaling.enabled=true
  refuse split-roles-empty-dir "needs a shared raw store" --set "$db" --set splitRoles.enabled=true \
    --set rawStore.persistence.enabled=false
  refuse monitor-without-metrics "serviceMonitor.enabled needs config.metrics=true" --set "$db" \
    --set serviceMonitor.enabled=true --set config.metrics=false

  # What the rendered pods run.
  local f="$tmp/default.yaml"
  expect "default: one Deployment" 1 "$(count 'kind: Deployment' "$f")"
  expect "default: the pod runs \`serve\`" 1 "$(count 'args: ["serve"]' "$f")"
  expect "default: no exec probe on the pod that serves HTTP" 0 "$(count 'exec:' "$f")"
  expect "default: Recreate on the single-writer volume" 1 "$(count 'type: Recreate' "$f")"
  f="$tmp/all-optional.yaml"
  local kind
  for kind in Ingress ServiceMonitor NetworkPolicy HorizontalPodAutoscaler PodDisruptionBudget; do
    expect "all-optional: one $kind" 1 "$(grep -cx "kind: $kind" "$f" || true)"
  done
  expect "all-optional: three Deployments" 3 "$(grep -cx 'kind: Deployment' "$f" || true)"
  f="$tmp/split-roles.yaml"
  expect "split-roles: the collect and etl pods have the exec liveness probe" 2 "$(count 'exec:' "$f")"
  expect "split-roles: the exec probe runs /ohs doctor --quick" 2 \
    "$(tr -d ' \n' < "$f" | grep -oF 'livenessProbe:exec:command:-/ohs-doctor---quick' | wc -l | tr -d ' ')"
  expect "split-roles: only the api pod has HTTP probes" 3 "$(count 'httpGet:' "$f")"
  expect "split-roles: no Recreate on a shared volume" 0 "$(count 'type: Recreate' "$f")"
  expect "split-roles-no-probe: workerLivenessProbe=null removes the probe" 0 "$(count 'exec:' "$tmp/split-roles-no-probe.yaml")"
  expect "no-probes: null removes the HTTP probes" 0 "$(count 'httpGet:' "$tmp/no-probes.yaml")"
  expect "newer-schema: the pod runs \`serve --allow-newer-schema\`" 1 \
    "$(count 'args: ["serve", "--allow-newer-schema"]' "$tmp/newer-schema.yaml")"
  expect "all-optional-newer-schema: the three Deployments get the flag" 3 \
    "$(count 'args: ["serve", "--allow-newer-schema"]' "$tmp/all-optional-newer-schema.yaml")"
  expect "newer-schema-string-false: the string \"false\" does not turn the flag on" 0 \
    "$(count 'allow-newer-schema' "$tmp/newer-schema-string-false.yaml")"
  expect "existing-secret: no Secret is created" 0 "$(grep -cx 'kind: Secret' "$tmp/existing-secret.yaml" || true)"
  expect "no-migrations: no Job" 0 "$(grep -cx 'kind: Job' "$tmp/no-migrations.yaml" || true)"
  expect "empty-dir: no PersistentVolumeClaim" 0 "$(grep -cx 'kind: PersistentVolumeClaim' "$tmp/empty-dir.yaml" || true)"

  # The chart and the binary agree: the flag and the probe command exist in
  # cmd/ohs, and the image has the binary where the probe calls it.
  check "cmd/ohs: \`ohs serve\` has --allow-newer-schema" grep -qF '"allow-newer-schema"' "$root/cmd/ohs/serve.go"
  check "cmd/ohs: \`ohs doctor\` has --quick" grep -qF '"quick"' "$root/cmd/ohs/doctor.go"
  check "deploy/Dockerfile: the binary is /ohs" grep -qE '^COPY --from=build /out/ohs /ohs$' "$root/deploy/Dockerfile"
  check "values.yaml: workerLivenessProbe calls /ohs doctor --quick" \
    grep -qF 'command: ["/ohs", "doctor", "--quick"]' "$root/deploy/helm/ohs/values.yaml"
}

# ---------------------------------------------------------------------------
# caddy
# ---------------------------------------------------------------------------

# The dummy upstream: a plain page, a page with security headers of its own,
# a slow stream (as text/event-stream and as another type) and a WebSocket
# echo. One event a second, so a buffering proxy shows.
write_upstream() {
  cat > "$tmp/upstream.py" << 'EOF'
import base64, hashlib, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "dummy-upstream"
    def log_message(self, *a): pass
    def do_HEAD(self): self.do_GET()
    def do_GET(self):
        body = self.command != "HEAD"
        if self.path == "/ws" and self.headers.get("Upgrade", "").lower() == "websocket":
            key = self.headers["Sec-WebSocket-Key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", base64.b64encode(hashlib.sha1(key.encode()).digest()).decode())
            self.end_headers()
            while True:
                h = self.rfile.read(2)
                if len(h) < 2: return
                mask = self.rfile.read(4)
                data = bytes(b ^ mask[i % 4] for i, b in enumerate(self.rfile.read(h[1] & 0x7F)))
                if h[0] & 0x0F == 8: return
                self.wfile.write(bytes([0x81, len(data)]) + data)
                self.wfile.flush()
        if self.path in ("/sse", "/chunked"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream" if self.path == "/sse" else "application/x-ndjson")
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            if not body: return
            for i in range(3):
                d = ("data: %d\n\n" % i).encode()
                self.wfile.write(b"%x\r\n%s\r\n" % (len(d), d))
                self.wfile.flush()
                time.sleep(1)
            self.wfile.write(b"0\r\n\r\n")
            return
        data = b"ok\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(data)))
        if self.path == "/own":
            self.send_header("X-Frame-Options", "SAMEORIGIN")
            self.send_header("Referrer-Policy", "same-origin")
        self.end_headers()
        if body: self.wfile.write(data)

ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
EOF
  # The client: each event must arrive when it is sent (the first at once, the
  # last two seconds later), and the WebSocket must upgrade and echo.
  cat > "$tmp/client.py" << 'EOF'
import base64, os, socket, ssl, sys, time
host, addr = sys.argv[1], sys.argv[2]
ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE
def connect():
    return ctx.wrap_socket(socket.create_connection((addr, 443), timeout=10), server_hostname=host)
failed = False
for path in ("/sse", "/chunked"):
    s = connect()
    s.sendall(("GET %s HTTP/1.1\r\nHost: %s\r\n\r\n" % (path, host)).encode())
    start, stamps, buf = time.time(), [], b""
    try:
        while b"data: 2" not in buf:
            chunk = s.recv(65536)
            if not chunk: break
            buf += chunk
            for i in range(3):
                if len(stamps) == i and (b"data: %d" % i) in buf: stamps.append(time.time() - start)
    except OSError as e:
        print("      %s: %s" % (path, e))
    s.close()
    ok = len(stamps) == 3 and stamps[0] < 0.8 and stamps[2] - stamps[0] > 1.5
    print("%s caddy: %s is not buffered (events after %s s)" % ("ok   " if ok else "FAIL ", path, " ".join("%.1f" % x for x in stamps)))
    failed |= not ok
s = connect()
s.sendall(("GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
           "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n" % (host, base64.b64encode(os.urandom(16)).decode())).encode())
head, echo = b"", b""
try:
    while b"\r\n\r\n" not in head:
        chunk = s.recv(4096)
        if not chunk: break
        head += chunk
    status = head.split(b"\r\n")[0].decode()
    if " 101 " in status:
        mask = os.urandom(4)
        s.sendall(bytes([0x81, 0x84]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(b"ping")))
        echo = head.split(b"\r\n\r\n", 1)[1]
        while len(echo) < 6: echo += s.recv(4096)
except OSError as e:
    status = str(e)
ok = echo[2:6] == b"ping"
print("%s caddy: a WebSocket upgrades and echoes (%s)" % ("ok   " if ok else "FAIL ", status))
failed |= not ok
sys.exit(1 if failed else 0)
EOF
}

# caddy_stdin ARGS... < Caddyfile: runs `caddy ARGS` on the file read from stdin.
caddy_stdin() {
  docker run --rm -i "$CADDY_IMAGE" sh -c 'cat > /tmp/Caddyfile && caddy "$@"' caddy "$@"
}

# header_is FILE NAME VALUE: the response headers in FILE hold NAME: VALUE.
header_is() {
  local got
  got="$(tr -d '\r' < "$1" | grep -i "^$2:" | sed -E 's/^[^:]*: *//' | head -n 1)"
  if [ "$got" = "$3" ]; then pass "caddy: $4 $2: $3"; else fail "caddy: $4 $2: want '$3', got '$got'"; fi
}

# serve_caddy FILE: (re)starts Caddy on FILE in front of the upstream, with a
# certificate of its own authority instead of a public one, and sets $port.
serve_caddy() {
  docker rm -f "$id-caddy" > /dev/null 2>&1
  {
    printf '{\n\tlocal_certs\n\tskip_install_trust\n}\n'
    sed "s/127\.0\.0\.1:8080/$id-up:8080/" "$1"
  } > "$tmp/Caddyfile.run"
  docker create --name "$id-caddy" --network "$id" -p 127.0.0.1::443 "$CADDY_IMAGE" \
    caddy run --config /etc/caddy/Caddyfile --adapter caddyfile > /dev/null &&
    docker cp "$tmp/Caddyfile.run" "$id-caddy:/etc/caddy/Caddyfile" > /dev/null &&
    docker start "$id-caddy" > /dev/null || return 1
  port="$(docker port "$id-caddy" 443/tcp | head -n 1 | sed 's/.*://')"
  local _
  for _ in $(seq 1 40); do
    fetch -o /dev/null /healthz && return 0
    sleep 0.5
  done
  docker logs "$id-caddy" 2>&1 | tail -n 5
  return 1
}

# fetch CURL_ARGS... PATH: curl to the running Caddy as health.example.org.
fetch() {
  local n=$#
  local path="${!n}"
  curl -sk --max-time 10 --resolve "health.example.org:$port:127.0.0.1" "${@:1:$((n - 1))}" "https://health.example.org:$port$path"
}

check_caddy() {
  echo "== caddy ($CADDY_IMAGE)"
  command -v curl > /dev/null || { fail "caddy: curl is required"; return; }
  local file="$root/deploy/Caddyfile"

  check "caddy validate (deploy/Caddyfile)" caddy_stdin validate --config /tmp/Caddyfile --adapter caddyfile < "$file"
  check "caddy adapt (deploy/Caddyfile)" caddy_stdin adapt --config /tmp/Caddyfile --adapter caddyfile --validate < "$file"
  check "caddy fmt (deploy/Caddyfile is formatted)" caddy_stdin fmt --diff /tmp/Caddyfile < "$file"

  # The second site block ships commented out: uncomment it in place of the
  # first one, so it is checked too.
  awk '
    /^[^#[:space:](].*\{$/ { skip = 1 }
    skip { if ($0 == "}") skip = 0; next }
    /^# [^[:space:]]+ \{$/ { open = 1 }
    open { line = $0; sub(/^# ?/, "", line); print line; if ($0 == "# }") open = 0; next }
    { print }
  ' "$file" > "$tmp/Caddyfile.second"
  if grep -q '^health\.example\.org {$' "$tmp/Caddyfile.second" && grep -q 'handle @ohs' "$tmp/Caddyfile.second"; then
    check "caddy validate (the commented site block)" \
      caddy_stdin validate --config /tmp/Caddyfile --adapter caddyfile < "$tmp/Caddyfile.second"
  else
    fail "caddy: cannot find the commented site block in deploy/Caddyfile"
  fi

  write_upstream
  if ! {
    docker network create "$id" > /dev/null &&
      docker create --name "$id-up" --network "$id" "$PYTHON_IMAGE" python /upstream.py > /dev/null &&
      docker cp "$tmp/upstream.py" "$id-up:/upstream.py" > /dev/null &&
      docker cp "$tmp/client.py" "$id-up:/client.py" > /dev/null &&
      docker start "$id-up" > /dev/null
  }; then
    fail "caddy: cannot start the dummy upstream"
    return
  fi

  if ! serve_caddy "$file"; then
    fail "caddy: does not serve deploy/Caddyfile"
    return
  fi
  local h="$tmp/headers"
  # A page that sets none of the headers gets every default.
  fetch -I / > "$h"
  header_is "$h" strict-transport-security "max-age=31536000" "HEAD /"
  header_is "$h" x-content-type-options "nosniff" "HEAD /"
  header_is "$h" x-frame-options "DENY" "HEAD /"
  header_is "$h" referrer-policy "no-referrer" "HEAD /"
  header_is "$h" permissions-policy "camera=(), microphone=(), geolocation=(), payment=()" "HEAD /"
  header_is "$h" server "" "HEAD /"
  # A page with two headers of its own keeps them and still gets the others.
  fetch -I /own > "$h"
  header_is "$h" x-frame-options "SAMEORIGIN" "HEAD /own"
  header_is "$h" referrer-policy "same-origin" "HEAD /own"
  header_is "$h" x-content-type-options "nosniff" "HEAD /own"
  header_is "$h" permissions-policy "camera=(), microphone=(), geolocation=(), payment=()" "HEAD /own"
  header_is "$h" strict-transport-security "max-age=31536000" "HEAD /own"
  # The same over HTTP/1.1, on a GET and on a stream.
  fetch --http1.1 -o /dev/null -D "$h" /
  header_is "$h" strict-transport-security "max-age=31536000" "GET / (HTTP/1.1)"
  header_is "$h" x-content-type-options "nosniff" "GET / (HTTP/1.1)"
  fetch -I /sse > "$h"
  header_is "$h" content-type "text/event-stream" "HEAD /sse"
  header_is "$h" strict-transport-security "max-age=31536000" "HEAD /sse"
  docker exec "$id-up" python /client.py health.example.org "$id-caddy" || failures=$((failures + 1))

  # The second site block: the routes of ohs are proxied, the rest redirects,
  # and both carry the headers.
  if ! serve_caddy "$tmp/Caddyfile.second"; then
    fail "caddy: does not serve the commented site block"
    return
  fi
  local path
  for path in /healthz /readyz /v1/users /mcp /connect /console /oauth/callback; do
    expect "caddy: second block proxies $path" 200 "$(fetch -o /dev/null -w '%{http_code}' "$path")"
  done
  expect "caddy: second block redirects the rest" 302 "$(fetch -o /dev/null -w '%{http_code}' /anything-else)"
  fetch -I /healthz > "$h"
  header_is "$h" strict-transport-security "max-age=31536000" "second block, HEAD /healthz"
  header_is "$h" x-content-type-options "nosniff" "second block, HEAD /healthz"
  fetch -I /anything-else > "$h"
  header_is "$h" strict-transport-security "max-age=31536000" "second block, redirect"
}

# ---------------------------------------------------------------------------
# compose
# ---------------------------------------------------------------------------

check_compose() {
  echo "== compose ($(docker compose version --short 2> /dev/null || echo missing))"
  docker compose version > /dev/null 2>&1 || { fail "compose: the docker compose plugin is required"; return; }
  # A copy, so the .env the compose file requires at the repository root is a
  # throwaway one and the checkout is not written to.
  local tree="$tmp/tree"
  mkdir -p "$tree/deploy"
  cp "$root"/deploy/docker-compose*.yml "$tree/deploy/"
  printf 'OHS_ENCRYPTION_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n' > "$tree/.env"
  { cat "$tree/.env"; echo 'POSTGRES_PASSWORD=0123456789abcdef0123456789abcdef0123456789abcdef'; } > "$tree/with-password.env"
  local base="$tree/deploy/docker-compose.yml"
  compose() { (cd "$tree" && env -u POSTGRES_PASSWORD -u OHS_VERSION -u OHS_HTTP_BIND -u OHS_HTTP_PORT docker compose "$@"); }

  local value
  for value in unset empty; do
    [ "$value" = empty ] && echo 'POSTGRES_PASSWORD=' >> "$tree/.env"
    if compose --env-file "$tree/.env" -f "$base" config > /dev/null 2> "$tmp/compose.err"; then
      fail "compose: config passes with POSTGRES_PASSWORD $value"
    elif grep -q 'set POSTGRES_PASSWORD in .env' "$tmp/compose.err"; then
      pass "compose: config refuses POSTGRES_PASSWORD $value and says what to set"
    else
      fail "compose: config fails with POSTGRES_PASSWORD $value, without saying what to set"
      sed 's/^/      /' "$tmp/compose.err" | tail -n 5
    fi
  done

  local overlay args
  for overlay in "" build pi s3 "build pi s3"; do
    args=(-f "$base")
    for value in $overlay; do args+=(-f "$tree/deploy/docker-compose.$value.yml"); done
    check "compose: config (base${overlay:+ + $overlay})" compose --env-file "$tree/with-password.env" "${args[@]}" config
    [ -z "$overlay" ] && cp "$tmp/check.out" "$tmp/compose-base.yaml"
  done
  # The port stays on the loopback interface unless OHS_HTTP_BIND says so,
  # and the password reaches the URL ohs connects with.
  expect "compose: the port is published on 127.0.0.1 only" 1 "$(count 'host_ip: 127.0.0.1' "$tmp/compose-base.yaml")"
  expect "compose: POSTGRES_PASSWORD is in OHS_DATABASE_URL" 1 \
    "$(count 'OHS_DATABASE_URL: postgres://ohs:0123456789abcdef0123456789abcdef0123456789abcdef@postgres:5432/ohs' "$tmp/compose-base.yaml")"
}

# ---------------------------------------------------------------------------

[ $# -eq 0 ] && set -- helm caddy compose
for part in "$@"; do
  case "$part" in
    helm) check_helm ;;
    caddy) check_caddy ;;
    compose) check_compose ;;
    *) echo "usage: scripts/check-deploy-render.sh [helm] [caddy] [compose]" >&2; exit 2 ;;
  esac
done

if [ "$failures" -gt 0 ]; then
  echo "check-deploy-render: $failures check(s) failed"
  exit 1
fi
echo "check-deploy-render: every check passed"
