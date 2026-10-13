# Project 7: A message when your night syncs

**Goal:** a small server of your own that Open Health Sync calls the moment new sleep arrives,
and that writes you one line about last night. No polling, no cron: your instance tells you.

**Time:** 30 minutes. **Level:** some Python. **Needs:** [project 2](read-your-sleep.md).

## How it works

You register an address with your instance (a webhook). Each time a sync or an import writes new
data, the instance sends a short signed message to that address saying what changed: which
source, which kind of records, which days. Never a health value. Your server checks the
signature, then reads what it needs with the API, as in every other project.

## 1. Let your instance call your computer

A webhook usually goes to a public `https` address. For one on your own computer, allow private
addresses: add this line to `.env` and restart.

```sh
echo "OHS_WEBHOOK_ALLOW_PRIVATE=true" >> .env
docker compose --env-file .env -f deploy/docker-compose.yml up -d --wait
```

Your instance runs inside Docker, where `localhost` means the container itself. Use your
computer's address on your local network instead:

```sh
IP=$(ipconfig getifaddr en0 2>/dev/null || hostname -I | cut -d' ' -f1)   # macOS, then Linux
echo $IP
```

## 2. Register the hook

```sh
set -a; . ~/.ohs.env; set +a
SECRET=$(curl -s -X POST "$OHS_URL/v1/webhooks" -H "Authorization: Bearer $OHS_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"url\":\"http://$IP:3000/hook\",\"event_types\":[\"data.written\",\"connection.needs_reauth\"]}" | jq -r .secret)
echo "OHS_WEBHOOK_SECRET=$SECRET" >> ~/.ohs.env
```

The secret is shown only in that answer: it now sits in `~/.ohs.env` with your key. Your server
uses it to check that a message really comes from your instance.

## 3. Write the server

```sh
awk '/^def nights_by_app/{exit} {print}' recipes/sleep-report.py > sleep-alert.py
```

Paste this at the end of `sleep-alert.py`:

```python
import base64
import hashlib
import hmac
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ.get("OHS_WEBHOOK_SECRET", "")


def verify(headers, body):
    """True when Open Health Sync signed this request with our secret."""
    key = base64.b64decode(SECRET.removeprefix("whsec_"))
    msg_id, ts = headers.get("webhook-id", ""), headers.get("webhook-timestamp", "0")
    if abs(time.time() - int(ts)) > 300:
        return False
    signed = f"{msg_id}.{ts}.".encode() + body
    expected = base64.b64encode(hmac.new(key, signed, hashlib.sha256).digest()).decode()
    return any(hmac.compare_digest(expected, sig.removeprefix("v1,"))
               for sig in headers.get("webhook-signature", "").split())


def morning_line():
    """Last night against the week before, in one sentence."""
    nights = {}
    since = (datetime.now() - timedelta(days=9)).date().isoformat()
    for s in records("sleep", start=since):
        if s["bedtime_end"]:
            end = datetime.fromisoformat(s["bedtime_end"][:19]).replace(tzinfo=timezone.utc).astimezone()
            morning = (end + timedelta(hours=10)).date()
            nights[morning] = nights.get(morning, 0) + (s["total_sleep_seconds"] or 0) / 60
    if not nights:
        return "No sleep recorded yet."
    last = max(nights)
    before = [m for d, m in nights.items() if d < last]
    line = f"{last:%a %d %b}: you slept {int(nights[last] // 60)}h{int(nights[last] % 60):02d}"
    if before:
        diff = nights[last] - sum(before) / len(before)
        line += f", {abs(diff):.0f} min {'more' if diff >= 0 else 'less'} than your average of the nights before."
    return line


said = set()  # one sync can send several events: say each line once


class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length", 0)))
        if not verify(self.headers, body):
            self.send_response(401)
            self.end_headers()
            return
        self.send_response(204)  # answer first, then do the work
        self.end_headers()
        event = json.loads(body)
        if event["type"] == "webhook.test":
            print("Test event received: the hook works.")
        elif event["type"] == "data.written" and "sleep_sessions" in event["data"]["records"]:
            line = morning_line()
            if line not in said:
                said.add(line)
                print(line)
        elif event["type"] == "connection.needs_reauth":
            print(f"{event['data']['provider']} needs you to sign in again: python3 recipes/connect.py")

    def log_message(self, *args):  # keep the terminal for our own lines
        pass


if not SECRET:
    sys.exit("missing OHS_WEBHOOK_SECRET: register the hook first (step 2)")
print("Listening on port 3000. Ctrl-C to stop.")
HTTPServer(("0.0.0.0", 3000), Hook).serve_forever()
```

```sh
python3 sleep-alert.py
```

```text
Listening on port 3000. Ctrl-C to stop.
```

## 4. Try it

In a second terminal, ask your instance to send a test message:

```sh
set -a; . ~/.ohs.env; set +a
ID=$(curl -s "$OHS_URL/v1/webhooks" -H "Authorization: Bearer $OHS_KEY" | jq -r '.data[0].id')
curl -s -X POST "$OHS_URL/v1/webhooks/$ID/test" -H "Authorization: Bearer $OHS_KEY" > /dev/null
```

The first terminal says `Test event received: the hook works.` From now on, each time a ring or
a band syncs a night, or you import a new Health export, it writes:

```text
Wed 14 Oct: you slept 7h30, 32 min more than your average of the nights before.
```

**What the code does.** `verify()` recomputes the signature from the secret, the message id, the
time and the body, and refuses a message that does not match or is more than five minutes old.
The server answers at once, then does its work, because your instance waits at most 15 seconds
for an answer. A message can come twice or several at once: the line is printed only when it is
new.

## Make it yours

- **To your phone.** Replace `print(line)` with a call to a notification service you trust, or a
  message to yourself in a chat app with a bot. Remember that the line then leaves your machine.
- **More events.** Your ring's `daily_summaries` arrive the same way: say your readiness score in
  the same line.
- **Always on.** Run the server on the same Raspberry Pi as your instance, with a systemd
  service or in the compose file, and point the hook at it.

The [webhooks guide](../webhooks.md) has every event, the retries and the delivery log.

## Share it

What your server does with the events, in
[Show and tell](https://github.com/soma-intelligence/open-health-sync/discussions/categories/show-and-tell).

**Next:** the [prompt library](prompts.md), or [write a recipe](../recipes/README.md#write-your-own)
for the next person.
