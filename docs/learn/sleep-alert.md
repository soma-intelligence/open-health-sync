# Project 7: A message when your night syncs

**Goal:** a small server of your own that Open Health Sync calls the moment new sleep arrives,
and that writes you one line about last night. No polling, no cron: your instance tells you.

**Time:** 30 minutes. **Level:** some Go. **Needs:** [project 2](read-your-sleep.md).

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
mkdir sleep-alert && cp recipes/starter/main.go sleep-alert/
```

In `sleep-alert/main.go`, add these lines to the `import (...)` block at the top:

```go
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"math"
	"strconv"
	"sync"
```

Then replace everything from `// main is yours to replace.` to the end with:

```go
var secret = os.Getenv("OHS_WEBHOOK_SECRET")

// verify is true when Open Health Sync signed this request with our secret.
func verify(h http.Header, body []byte) bool {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return false
	}
	id, ts := h.Get("webhook-id"), h.Get("webhook-timestamp")
	sent, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-sent)) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, sig := range strings.Fields(h.Get("webhook-signature")) {
		if hmac.Equal([]byte(want), []byte(strings.TrimPrefix(sig, "v1,"))) {
			return true
		}
	}
	return false
}

type session struct {
	BedtimeEnd        string  `json:"bedtime_end"`
	TotalSleepSeconds float64 `json:"total_sleep_seconds"`
}

// morningLine is last night against the nights before, in one sentence.
func morningLine() string {
	minutes := map[string]float64{} // morning: minutes asleep
	for _, s := range records[session]("sleep", url.Values{"start": {daysAgo(9).Format(iso)}}) {
		if end, err := time.Parse(time.RFC3339, s.BedtimeEnd); err == nil {
			minutes[end.Local().Add(10*time.Hour).Format(iso)] += s.TotalSleepSeconds / 60
		}
	}
	if len(minutes) == 0 {
		return "No sleep recorded yet."
	}
	days := slices.Sorted(maps.Keys(minutes))
	last := days[len(days)-1]
	t, _ := time.Parse(iso, last)
	m := minutes[last]
	line := fmt.Sprintf("%s: you slept %dh%02d", t.Format("Mon 02 Jan"), int(m)/60, int(m)%60)
	if before := days[:len(days)-1]; len(before) > 0 {
		var sum float64
		for _, d := range before {
			sum += minutes[d]
		}
		diff, word := m-sum/float64(len(before)), "more"
		if diff < 0 {
			word = "less"
		}
		line += fmt.Sprintf(", %.0f min %s than your average of the nights before.", math.Abs(diff), word)
	}
	return line
}

type event struct {
	Type string `json:"type"`
	Data struct {
		Provider string         `json:"provider"`
		Records  map[string]int `json:"records"`
	} `json:"data"`
}

func main() {
	if secret == "" {
		fail("missing OHS_WEBHOOK_SECRET: register the hook first (step 2)")
	}
	said := map[string]bool{} // one sync can send several events: say each line once
	var mu sync.Mutex
	http.HandleFunc("POST /hook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !verify(r.Header, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent) // answer first, then do the work
		var e event
		if json.Unmarshal(body, &e) != nil {
			return
		}
		go func() {
			switch {
			case e.Type == "webhook.test":
				fmt.Println("Test event received: the hook works.")
			case e.Type == "data.written" && e.Data.Records["sleep_sessions"] > 0:
				line := morningLine()
				mu.Lock()
				defer mu.Unlock()
				if !said[line] {
					said[line] = true
					fmt.Println(line)
				}
			case e.Type == "connection.needs_reauth":
				fmt.Printf("%s needs you to sign in again: go run ./recipes/connect\n", e.Data.Provider)
			}
		}()
	})
	fmt.Println("Listening on port 3000. Ctrl-C to stop.")
	fail("%v", http.ListenAndServe(":3000", nil))
}
```

```sh
go run ./sleep-alert
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

**What the code does.** `verify` recomputes the signature from the secret, the message id, the
time and the body, and refuses a message that does not match or is more than five minutes old.
The server answers at once, then does its work, because your instance waits at most 15 seconds
for an answer. A message can come twice or several at once: the line is printed only when it is
new.

## Make it yours

- **To your phone.** Replace `fmt.Println(line)` with a call to a notification service you trust, or a
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
