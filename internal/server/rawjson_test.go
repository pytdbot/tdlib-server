package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	nats "github.com/nats-io/nats.go"

	"github.com/pytdbot/tdlib-server/internal/tdjson"
	"github.com/pytdbot/tdlib-server/internal/utils"
)

// These tests pin the byte-relay paths to the behaviour of the
// decode-change-encode code they replaced. legacyProcessUpdate and
// legacyProcessRequest, further down, are those two functions of v0.3.0 (minus
// the scheduler cases, the scheduler being gone): on the same input the server
// must hand the same bytes to TDLib and to NATS.
//
// Like the server, they need libtdjson at link time: run them with `make test`.

// ---------------------------------------------------------------------------
// What one call made visible

type published struct {
	subject string
	body    any
}

type capture struct {
	sent      int64  // tdSend calls
	request   string // the request handed to TDLib
	published []published
	delivered []any // responses handed to a waiting Invoke()
	panicked  bool
}

const pendingID = "42" // a request id with an Invoke() waiting behind it

// record runs fn against srv with TDLib and NATS replaced by capturers.
func record(srv *Server, fn func()) (c capture) {
	waiting := srv.results.Make(pendingID)
	srv.tdSend = func(request string) {
		c.sent++
		c.request = request
	}
	srv.natsPublish = func(subject string, body []byte) error {
		c.published = append(c.published, published{subject, unorderedOptions(decodeExact(body))})
		return nil
	}
	defer func() {
		select {
		case v := <-waiting:
			c.delivered = append(c.delivered, unorderedOptions(decodeExact(utils.UnsafeMarshalBytes(v))))
		default:
		}
		srv.results.Clear()
		if p := recover(); p != nil {
			c.panicked = true
		}
	}()
	fn()
	return
}

func compareCaptures(t *testing.T, iter int, what, input string, want, got capture) {
	t.Helper()
	if want.sent != got.sent {
		t.Fatalf("iter %d (%s): v0.3.0 calls td_send %d time(s), relay %d\ninput: %s", iter, what, want.sent, got.sent, input)
	}
	if want.sent > 0 && !semanticallyEqual(want.request, got.request) {
		t.Fatalf("iter %d (%s): request handed to TDLib differs\ninput:  %s\nv0.3.0: %s\nrelay:  %s", iter, what, input, want.request, got.request)
	}
	if !reflect.DeepEqual(want.published, got.published) {
		t.Fatalf("iter %d (%s): NATS output differs\ninput: %s\nv0.3.0: %+v\nrelay:  %+v", iter, what, input, want.published, got.published)
	}
	if !reflect.DeepEqual(want.delivered, got.delivered) {
		t.Fatalf("iter %d (%s): delivery to Invoke() differs\nv0.3.0: %+v\nrelay:  %+v\n%s", iter, what, want.delivered, got.delivered, input)
	}
}

// decodeExact decodes JSON keeping numbers exactly as written.
func decodeExact(b []byte) any {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "NOT JSON: " + string(b)
	}
	return v
}

func semanticallyEqual(a, b string) bool {
	return reflect.DeepEqual(decodeExact([]byte(a)), decodeExact([]byte(b)))
}

// unorderedOptions drops the order of the "updates" list of an "updates" object
// (getCurrentState() builds it by ranging over a Go map) and the live fields of
// a "serverStats" reply: both differ from call to call in v0.3.0 already.
func unorderedOptions(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	switch m["@type"] {
	case "serverStats":
		delete(m, "uptime")
		delete(m, "updates_count")
		delete(m, "requests_count")
	case "updates":
		if list, ok := m["updates"].([]any); ok {
			parts := make([]string, len(list))
			for i, e := range list {
				parts[i] = fmt.Sprint(e)
			}
			sort.Strings(parts)
			m["updates"] = parts
		}
	}
	return m
}

func optionsOf(srv *Server) any {
	b, _ := json.Marshal(srv.options)
	return decodeExact(b)
}

// tdlibRespond lays a response out the way TDLib's from_response() does, with
// "@extra" re-encoded by TDLib's JSON writer (here: keys sorted by
// encoding/json, i.e. a different order than the request used).
func tdlibRespond(t *testing.T, requestText, body string) string {
	t.Helper()
	var req map[string]any
	dec := json.NewDecoder(strings.NewReader(requestText))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("tdlibRespond: request is not JSON: %v\n%s", err, requestText)
	}
	extra, err := json.Marshal(req["@extra"])
	if err != nil {
		t.Fatal(err)
	}
	return body[:len(body)-1] + `,"@extra":` + string(extra) + `,"@client_id":1}`
}

func newTestServer() *Server {
	ready := make(chan struct{})
	close(ready)
	s := &Server{
		td:               &tdjson.TdJson{},
		requestID:        utils.NewIdGenerator(),
		results:          utils.NewSafeResultsMap(),
		options:          Data{},
		myIDInt:          508903,
		broadcast_types:  map[string]struct{}{"updateFile": {}, "x": {}},
		updatesSubject:   "bot.1.updates",
		broadcastSubject: "bot.1.broadcast",
		waitForReady:     ready,
		waitForClosed:    make(chan struct{}),
	}
	s.isRunning.Store(true) // publish() reaches natsPublish, which record() sets
	return s
}

// ---------------------------------------------------------------------------
// Random documents: any key order, whitespace everywhere, duplicate keys,
// decoy "@extra"/"routing_key" keys nested in user data, escaped quotes and
// backslashes, "@extra" in every position or absent or not an object.

type docgen struct{ r *rand.Rand }

type kv struct{ k, v string }

var (
	allKeys = []string{"@type", "@extra", "routing_key", "id", "request_id", "text", "data", "a", "b", "content", "user", "name", "value", "chat_id"}
	strBits = []string{
		"hello", "世界", "<b>&</b>", `\"@extra\":`, `\\"`, `{"@extra":1}`, `"routing_key":`, "\x01", "\n", "\t", "\x7f",
		"emoji 😀", "_INBOX.Jz1YeQp1lXCkkqHnNpTGrL.lXCkkqHnNpTGrLJz1YeQp1", "0123456789abcdef01", strings.Repeat("x", 40), " ", "",
	}
	replySubjects = []string{
		"_INBOX.Jz1YeQp1lXCkkqHnNpTGrL.lXCkkqHnNpTGrLJz1YeQp1", "bench.reply.1", "a", "x.y.z", "A1",
		// need escaping: must take the general path
		"weird \"quoted\"", "back\\slash", "tab\tsep", "ünïcode", "line\nbreak",
		"", // invalid request: dropped by both
	}
	requestTypes   = []string{`"sendMessage"`, `"getMe"`, `"close"`, `"getCurrentState"`, `"getServerStats"`, `"x"`, `"searchMessages"`}
	responseBodies = []string{
		`{"@type":"ok"}`,
		`{"@type":"error","code":400,"message":"Bad Request: <b>&</b> \"quoted\""}`,
		`{"@type":"message","id":538,"content":{"@type":"messageText","text":{"@type":"formattedText","text":"see {\"@extra\":1} and \\\"@extra\\\": here"}}}`,
		`{"@type":"jsonValueObject","value":{"@extra":{"routing_key":"decoy"},"x":[{"@extra":1}]}}`,
		`{"@type":"user","id":508903,"first_name":"A","data":{"@extra":{"routing_key":"decoy"}}}`,
	}
)

func (g *docgen) pick(list []string) string { return list[g.r.IntN(len(list))] }
func (g *docgen) ws() string {
	return g.pick([]string{"", "", "", "", " ", "\n", "\t", "  ", " \r\n"})
}

func (g *docgen) jsonKey(k string) string {
	b, _ := json.Marshal(k)
	return string(b)
}

func (g *docgen) str() string {
	var b strings.Builder
	for n := g.r.IntN(5); n > 0; n-- {
		b.WriteString(g.pick(strBits))
	}
	out, _ := json.Marshal(b.String()) // a real encoder's escaping, always valid JSON
	return string(out)
}

func (g *docgen) int() string {
	switch g.r.IntN(4) {
	case 0:
		return "508903" // the bot itself
	case 1:
		return "-1001234567890"
	default:
		return strconv.Itoa(g.r.IntN(100000))
	}
}

func (g *docgen) value(depth int) string {
	switch k := g.r.IntN(10); {
	case k < 3:
		return g.str()
	case k == 3:
		return g.int()
	case k == 4:
		return strconv.FormatFloat(g.r.Float64()*1e6, 'f', -1, 64)
	case k == 5:
		return g.pick([]string{"true", "false", "null"})
	case k < 8:
		if depth <= 0 {
			return "0"
		}
		parts := make([]string, g.r.IntN(4))
		for j := range parts {
			parts[j] = g.ws() + g.value(depth-1)
		}
		return "[" + strings.Join(parts, ",") + g.ws() + "]"
	default:
		return g.object(depth-1, 0, "")
	}
}

func (g *docgen) build(ms []kv) string {
	g.r.Shuffle(len(ms), func(i, j int) { ms[i], ms[j] = ms[j], ms[i] })
	var b strings.Builder
	b.WriteString("{")
	for i, m := range ms {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(g.ws() + m.k + g.ws() + ":" + g.ws() + m.v + g.ws())
	}
	b.WriteString(g.ws() + "}")
	return b.String()
}

// extraMode: 0 = no "@extra", 1 = an object, 2 = something else.
func (g *docgen) object(depth, extraMode int, typ string) string {
	ms := make([]kv, 0, 8)
	if typ != "" {
		ms = append(ms, kv{`"@type"`, typ})
	}
	for n := g.r.IntN(7); n > 0; n-- {
		k := g.pick(allKeys) // may collide with another key, "@type" or "@extra" included
		v := g.value(depth)
		if k == "@type" && g.r.IntN(2) == 0 {
			v = g.pick(requestTypes)
		}
		ms = append(ms, kv{g.jsonKey(k), v})
	}
	switch extraMode {
	case 1:
		ms = append(ms, kv{`"@extra"`, g.extraObject(depth)})
	case 2:
		ms = append(ms, kv{`"@extra"`, g.pick([]string{g.str(), "42", "[1,2]", "null", "true"})})
	}
	return g.build(ms)
}

// extraObject is what a client sends as "@extra": normally {"id": ...}.
func (g *docgen) extraObject(depth int) string {
	ms := []kv{{`"id"`, g.str()}}
	if g.r.IntN(5) == 0 { // answers the server's own Invoke()
		ms = append(ms, kv{`"request_id"`, `"` + pendingID + `"`})
	}
	for n := g.r.IntN(4); n > 0; n-- {
		ms = append(ms, kv{g.jsonKey(g.pick(allKeys[3:])), g.value(depth)})
	}
	if g.r.IntN(6) == 0 { // the client already used the name routing_key itself
		ms = append(ms, kv{`"routing_key"`, g.value(depth)})
	}
	return g.build(ms)
}

func (g *docgen) request() (string, string) {
	mode := 1
	switch g.r.IntN(10) {
	case 8:
		mode = 2
	case 9:
		mode = 0
	}
	req := g.ws() + g.object(3, mode, g.pick(requestTypes)) + g.ws()
	return req, g.pick(replySubjects)
}

// update mimics TDLib's output: "@type" first, every top-level key written
// once (its serialiser emits each field a single time), "@client_id" last.
func (g *docgen) update() string {
	typ := g.pick([]string{`"updateNewMessage"`, `"updateChatReadInbox"`, `"updateFile"`, `"x"`, `"updateOption"`, `"updateUser"`, `"updateConnectionState"`})
	ms := []kv{{`"@type"`, typ}}
	used := map[string]bool{"@type": true}
	add := func(k, v string) {
		if k == "@type" || used[k] {
			return
		}
		used[k] = true
		ms = append(ms, kv{g.jsonKey(k), v})
	}
	switch typ {
	case `"updateOption"`:
		add("name", g.str())
		add("value", g.value(2))
	case `"updateUser"`:
		if g.r.IntN(8) > 0 {
			add("user", `{"@type":"user","id":`+g.int()+`}`)
		}
	}
	for n := g.r.IntN(6); n > 0; n-- {
		add(g.pick(allKeys), g.value(2))
	}
	return g.build(append(ms, kv{`"@client_id"`, "1"}))
}

func (g *docgen) responseBody() string {
	if g.r.IntN(2) == 0 {
		return g.pick(responseBodies)
	}
	return `{"@type":"x","payload":` + g.object(3, g.r.IntN(2), "") + `}`
}

// takesFastPath mirrors forwardRequest's conditions, for the statistics.
func takesFastPath(req []byte, reply string) bool {
	extraStart, _, typ, ok := scanRequest(req)
	return ok && !eq(typ, "close") && !eq(typ, "getCurrentState") && !eq(typ, "getServerStats") &&
		req[extraStart] == '{' && jsonSafe(reply)
}

// ---------------------------------------------------------------------------
// The differential test

func TestRelayMatchesDecodeAndEncode(t *testing.T) {
	const iterations = 20000
	stats := map[string]int{}
	for i := 0; i < iterations; i++ {
		g := &docgen{r: rand.New(rand.NewPCG(1, uint64(i)))}
		legacy, relay := newTestServer(), newTestServer()
		req, reply := g.request()
		msg := func() *nats.Msg { return &nats.Msg{Subject: "bot.1.requests", Reply: reply, Data: []byte(req)} }

		// ---- request: NATS -> server -> TDLib
		want := record(legacy, func() { legacyProcessRequest(legacy, msg()) })
		got := record(relay, func() { relay.processRequest(msg()) })
		if want.panicked || got.panicked {
			t.Fatalf("iter %d: panic on request (v0.3.0=%v relay=%v)\n%s", i, want.panicked, got.panicked, req)
		}
		compareCaptures(t, i, "request", req, want, got)
		switch {
		case want.sent == 0 && len(want.published) > 0:
			stats["request: answered by the server itself"]++
		case want.sent == 0:
			stats["request: dropped by both"]++
		case takesFastPath([]byte(req), reply):
			stats["request: forwarded without decoding"]++
		default:
			stats["request: forwarded by the general path"]++
		}
		if want.sent == 0 {
			continue
		}

		// ---- response: TDLib -> server -> NATS, to the request v0.3.0 sends
		resp := tdlibRespond(t, want.request, g.responseBody())
		want = record(legacy, func() { legacyProcessUpdate(legacy, utils.UnsafeUnmarshal(resp)) })
		got = record(relay, func() { relay.processUpdate([]byte(resp)) })
		if want.panicked || got.panicked {
			t.Fatalf("iter %d: panic on response (v0.3.0=%v relay=%v)\n%s", i, want.panicked, got.panicked, resp)
		}
		compareCaptures(t, i, "response", resp, want, got)
		if _, _, isResponse, _ := locateExtra([]byte(resp)); isResponse {
			stats["response: routed from the tail"]++
		} else {
			stats["response: general path"]++
		}

		// ---- the same answer with "@extra" first instead of last (a layout
		// TDLib does not produce; it must not be misrouted)
		var whole map[string]any
		dec := json.NewDecoder(strings.NewReader(resp))
		dec.UseNumber()
		if err := dec.Decode(&whole); err != nil {
			t.Fatal(err)
		}
		extra, _ := json.Marshal(whole["@extra"])
		delete(whole, "@extra")
		rest, _ := json.Marshal(whole)
		moved := `{"@extra":` + string(extra) + `,` + string(rest[1:])
		want = record(legacy, func() { legacyProcessUpdate(legacy, utils.UnsafeUnmarshal(moved)) })
		got = record(relay, func() { relay.processUpdate([]byte(moved)) })
		if want.panicked || got.panicked {
			t.Fatalf("iter %d: panic on moved response (v0.3.0=%v relay=%v)\n%s", i, want.panicked, got.panicked, moved)
		}
		compareCaptures(t, i, "response with @extra first", moved, want, got)

		// ---- update: TDLib -> server -> NATS
		upd := g.update()
		want = record(legacy, func() { legacyProcessUpdate(legacy, utils.UnsafeUnmarshal(upd)) })
		got = record(relay, func() { relay.processUpdate([]byte(upd)) })
		if want.panicked || got.panicked {
			t.Fatalf("iter %d: panic on update (v0.3.0=%v relay=%v)\n%s", i, want.panicked, got.panicked, upd)
		}
		compareCaptures(t, i, "update", upd, want, got)
		if !reflect.DeepEqual(optionsOf(legacy), optionsOf(relay)) {
			t.Fatalf("iter %d: stored options differ\nv0.3.0: %v\nrelay:  %v\n%s", i, optionsOf(legacy), optionsOf(relay), upd)
		}
		stats["update: identical"]++
	}
	t.Logf("%d iterations: %v", iterations, stats)
}

// TestResponseToInvokeIsDelivered: an answer to the server's own request is
// handed to the Invoke() waiting for it, and the pending entry is removed.
func TestResponseToInvokeIsDelivered(t *testing.T) {
	srv := newTestServer()
	srv.natsPublish = func(string, []byte) error { return nil }

	waiting := srv.results.Make("7")
	srv.processUpdate([]byte(`{"@type":"ok","@extra":{"request_id":"7"},"@client_id":1}`))
	select {
	case res := <-waiting:
		if res["@type"] != "ok" || res["@client_id"] != int64(1) {
			t.Fatalf("Invoke() got %v", res)
		}
	default:
		t.Fatal("Invoke() was not answered")
	}
	if n := srv.results.Len(); n != 0 {
		t.Fatalf("%d pending requests left behind", n)
	}

	// and one meant for a client goes to NATS with the routing key removed
	var got published
	srv.natsPublish = func(subject string, body []byte) error {
		got = published{subject, decodeExact(body)}
		return nil
	}
	srv.processUpdate([]byte(`{"@type":"ok","@extra":{"id":"1","routing_key":"_INBOX.a.b"},"@client_id":1}`))
	want := published{"_INBOX.a.b", map[string]any{"@type": "ok", "@extra": map[string]any{"id": "1"}, "@client_id": json.Number("1")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published %+v, want %+v", got, want)
	}
	if n := srv.results.Len(); n != 0 {
		t.Fatalf("%d pending requests left behind", n)
	}
}

// TestMalformedMessagesDoNotPanic: v0.3.0 crashed the whole server on each of
// these (unchecked type assertions).
func TestMalformedMessagesDoNotPanic(t *testing.T) {
	srv := newTestServer()
	srv.tdSend = func(string) {}
	srv.natsPublish = func(string, []byte) error { return nil }

	// 1. A request whose "@type" is not a string: any NATS client can send it.
	for _, data := range []string{
		`{"@type": 1, "@extra": {"id": "1"}}`,
		`{"@type": ["x"], "@extra": {"id": "1"}}`,
		`{"@extra": {"id": "1"}, "@type": {"a":1}}`,
	} {
		srv.processRequest(&nats.Msg{Subject: "bot.1.requests", Reply: "_INBOX.a.b", Data: []byte(data)})
		srv.processDecoded(utils.UnsafeUnmarshal(data))
	}

	// 2. An updateUser without a numeric user.id.
	for _, upd := range []string{
		`{"@type":"updateUser","user":{"@type":"user"},"@client_id":1}`,
		`{"@type":"updateUser","@client_id":1}`,
		`{"@type":"updateUser","user":{"@type":"user","id":"508903"},"@client_id":1}`,
	} {
		srv.processUpdate([]byte(upd))
		srv.processDecoded(utils.UnsafeUnmarshal(upd))
	}

	// 3. An updateOption whose name is not a string.
	srv.processUpdate([]byte(`{"@type":"updateOption","name":5,"value":1,"@client_id":1}`))
	srv.processDecoded(utils.UnsafeUnmarshal(`{"@type":"updateOption","name":5,"value":1}`))

	// 4. A response whose routing_key is not a string.
	srv.processUpdate([]byte(`{"@type":"ok","@extra":{"routing_key":5},"@client_id":1}`))
	srv.processDecoded(utils.UnsafeUnmarshal(`{"@type":"ok","@extra":{"routing_key":5}}`))

	// 5. Requests the server answers itself, and ones it leaves to TDLib's
	//    judgement, are told apart without decoding: forwardRequest reports
	//    true when it handed the request over or dropped it, false when the
	//    general path has to deal with it.
	for data, forwarded := range map[string]bool{
		`{"@type":"getServerStats","@extra":{"id":"1"}}`:                    false,
		`{"@type":"close","@extra":{"id":"1"}}`:                             false,
		`{"@type":"getCurrentState","@extra":{"id":"1"}}`:                   false,
		`{"@type":"scheduleEvent","@extra":{"id":"1"}}`:                     true, // not a server request any more: TDLib answers the client with an error
		`{"@type":"cancelScheduledEvent","@extra":{"id":"1"}}`:              true,
		`{"@type":"sendMessage","@extra":{"id":"1"}}`:                       true,
		`{"@type":"getCurrentState ","@extra":{"id":"1"}}`:                  true,  // not one of them
		`{"x":1,"@type":"close","@extra":{"id":"1"}}`:                       false, // the last "@type" is the one that counts
		`{"@type":"close","x":{"@type":"sendMessage"},"@extra":{"id":"1"}}`: false, // only the top level counts
		`{"@type":1,"@extra":{"id":"1"}}`:                                   true,  // TDLib then answers the client with an error
		`{"@type":"sendMessage","@extra":"x"}`:                              true,  // "@extra" is not an object: ignored
		`{"@type":"sendMessage"}`:                                           false, // no "@extra": dropped by the general path
		`{"@type":"sendMessage","@extra":{"\u006b":"1"}}`:                   false, // an escaped member key
		`not json`: false,
	} {
		if got := srv.forwardRequest(&nats.Msg{Subject: "bot.1.requests", Reply: "_INBOX.a.b", Data: []byte(data)}); got != forwarded {
			t.Errorf("forwardRequest(%s) = %v, want %v", data, got, forwarded)
		}
	}
}

// ---------------------------------------------------------------------------
// The helpers themselves

func TestLocateExtraIgnoresNestedDecoys(t *testing.T) {
	const good = `{"@type":"ok","data":{"@extra":{"routing_key":"decoy"}},"@extra":{"id":"1","routing_key":"_INBOX.a.b"},"@client_id":1}`
	vs, ve, isResponse, seen := locateExtra([]byte(good))
	if !isResponse || !seen || string(good[vs:ve]) != `{"id":"1","routing_key":"_INBOX.a.b"}` {
		t.Fatalf("got response=%v seen=%v value=%q", isResponse, seen, good[vs:ve])
	}
	if vs, _, isResponse, _ := locateExtra([]byte(`{"@type":"ok","@extra":{"request_id":"7"}}`)); !isResponse || vs != len(`{"@type":"ok","@extra":`) {
		t.Errorf("response without @client_id not recognised: %v %d", isResponse, vs)
	}
	// whitespace after the colon: still a response, and valStart sits on '{'
	spaced := `{"@type":"ok","@extra":` + "\n\t" + `{"id":"1"},"@client_id":1}`
	if vs, ve, isResponse, _ := locateExtra([]byte(spaced)); !isResponse || string([]byte(spaced)[vs:ve]) != `{"id":"1"}` {
		t.Errorf("whitespace after the colon not recognised: %v %d %d", isResponse, vs, ve)
	}
	// a large "@extra" (here: beyond the tail window) is still found
	big := `{"@type":"ok","@extra":{"id":"` + strings.Repeat("y", 2000) + `","routing_key":"_INBOX.a.b"},"@client_id":1}`
	if vs, ve, isResponse, _ := locateExtra([]byte(big)); !isResponse || ve-vs != 2000+len(`{"id":"","routing_key":"_INBOX.a.b"}`) {
		t.Errorf("large @extra not found: %v %d %d", isResponse, vs, ve)
	}
	tricky, _ := json.Marshal(map[string]any{"a": map[string]any{"b": []any{1, 2, map[string]any{"c": "},\", \"}, [ ] {"}}}})
	nested := `{"@type":"ok","@extra":` + string(tricky) + `,"@client_id":12}` + "\n"
	if vs, ve, isResponse, _ := locateExtra([]byte(nested)); !isResponse || ve != len(`{"@type":"ok","@extra":`)+len(tricky) {
		t.Errorf("nested value not skipped correctly: %v %d %d", isResponse, vs, ve)
	}

	// seen but not a response: must be left to the general path
	for _, bad := range []string{
		`{"@type":"ok","data":{"@extra":{"routing_key":"decoy"}}}`,
		`{"@type":"ok","data":{"@extra":{"routing_key":"decoy"}},"@client_id":1}`,
		`{"@extra":{"a":1},"@type":"ok","@client_id":1}`, // first instead of last
		`{"@type":"ok","@extra":{"a":1},"x":2,"@client_id":1}`,
		`{"@type":"ok","@extra":{"a":1},"@client_id":1} x`,
		`{"@type":"ok","@extra":{"a":1 "@extra": }}`,
	} {
		if _, _, isResponse, seen := locateExtra([]byte(bad)); isResponse || !seen {
			t.Errorf("locateExtra(%s) = response %v, seen %v; want an unusual message", bad, isResponse, seen)
		}
	}
	// no "@extra" member at all: an update
	for _, upd := range []string{
		`{"@type":"updateNewMessage","message":{"@type":"message"},"@client_id":1}`,
		`{"@type":"text with \"@extra\": inside","@client_id":1}`,
		``, `[1,2]`, `{"a":`, `{"@type":"x","text":"a\"@extra\"b"}`,
	} {
		if _, _, isResponse, seen := locateExtra([]byte(upd)); isResponse || seen {
			t.Errorf("locateExtra(%q) = response %v, seen %v; want a plain update", upd, isResponse, seen)
		}
	}
}

func TestCutMemberEveryPosition(t *testing.T) {
	cases := map[string]string{
		`{"routing_key":{"a":[1,{"b":"}"},2]},"z":1}`:    `{"z":1}`,
		`{"routing_key":"x"}`:                            `{}`,
		`{"routing_key":"x","id":"1"}`:                   `{"id":"1"}`,
		`{"id":"1","routing_key":"x"}`:                   `{"id":"1"}`,
		`{"id":"1","routing_key":"x","a":2}`:             `{"id":"1","a":2}`,
		`{ "id" : 1 , "routing_key" : "x" , "a" : 2 }`:   `{ "id" : 1 , "a" : 2 }`,
		`{"a":{"routing_key":"deep"},"routing_key":"x"}`: `{"a":{"routing_key":"deep"}}`,
		`{"routing_key":"it has, commas { and } \"q\""}`: `{}`,
	}
	for in, want := range cases {
		raw := []byte(in)
		val, from, to, ok := cutMember(raw, 0, `"routing_key"`)
		if !ok {
			t.Errorf("cutMember(%s): not found", in)
			continue
		}
		got := string(append(append([]byte{}, raw[:from]...), raw[to:]...))
		if !semanticallyEqual(got, want) {
			t.Errorf("cutMember(%s) = %s, want %q", in, got, want)
		}
		if val[0] == '"' {
			if _, ok := unquote(val); !ok {
				t.Errorf("cutMember(%s): value %q is not a string", in, val)
			}
		}
	}
}

func TestNestedIntAndType(t *testing.T) {
	for _, c := range []struct {
		raw    string
		want   int64
		wantOK bool
	}{
		{`{"@type":"updateUser","user":{"@type":"user","id":508903},"@client_id":1}`, 508903, true},
		{`{"@type":"updateUser","user":{"@type":"user","id":"508903"}}`, 508903, true}, // int53 as string
		{`{"@type":"updateUser","user":{"@type":"user","first_name":"x","id":-100123}}`, -100123, true},
		{`{"@type":"updateUser","user":{"@type":"user"},"@client_id":1}`, 0, false},
		{`{"@type":"updateUser","@client_id":1}`, 0, false},
		{`{"@type":"updateUser","user":"x"}`, 0, false},
	} {
		if got, ok := jsonNestedInt([]byte(c.raw), `"user"`, `"id"`); ok != c.wantOK || got != c.want {
			t.Errorf("jsonNestedInt(%s) = %d,%v want %d,%v", c.raw, got, ok, c.want, c.wantOK)
		}
	}
	const two = `{"@type":"x","data":{"user":{"id":1}},"user":{"id":2}}`
	if v, ok := jsonNestedInt([]byte(two), `"user"`, `"id"`); !ok || v != 2 {
		t.Errorf("took the nested user: %d %v", v, ok)
	}

	for raw, want := range map[string]string{
		`{"@type":"a","x":1}`:                          "a",
		`{ "@type" : "b" }`:                            "b",
		`{"x":{"@type":"c"},"@type":"d"}`:              "d",
		`{"x":"@type\",\"@type\":\"e"}`:                "", // only a real member counts
		`{"x":1}`:                                      "",
		`{"@type":{"a":"b"}}`:                          "",
		`{"@type":"update\"New\"Message","x":1}`:       "", // escapes can only spell an unknown type
		`{"text":"escaped \\\" quote","@type":"f"}`:    "f",
		`{"text":"😀 \u003c \\ \\","@type":"g"}`:        "g",
		`{ "@type" : "h", "x" : { "y" : [ 1 , 2 ] } }`: "h",
	} {
		if got := jsonType([]byte(raw)); string(got) != want {
			t.Errorf("jsonType(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestHelpersNeverPanic(t *testing.T) {
	exercise := func(raw []byte) {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("panic on %q: %v", raw, p)
			}
		}()
		_ = jsonType(raw)
		_, _, _, _ = locateExtra(raw)
		_, _, _, _ = scanRequest(raw)
		_, _, _, _ = cutMember(raw, 0, `"routing_key"`)
		_, _ = jsonNestedInt(raw, `"user"`, `"id"`)
		if extraStart, extraEnd, _, ok := scanRequest(raw); ok && len(raw) > extraStart && raw[extraStart] == '{' {
			_, _ = spliceRoutingKey(raw, extraStart, extraEnd, "_INBOX.a.b")
		}
		if vs, ve, isResponse, _ := locateExtra(raw); isResponse {
			_, _, _ = stripRoutingKey(raw, vs, ve)
		}
	}
	r := rand.New(rand.NewPCG(7, 7))
	alphabet := []byte(`{}[]",:\/ abc0123456789.eE+-trufalsnl@`)
	for i := 0; i < 200000; i++ {
		raw := make([]byte, r.IntN(60))
		for j := range raw {
			raw[j] = alphabet[r.IntN(len(alphabet))]
		}
		exercise(raw)
	}
	g := &docgen{r: r}
	for i := 0; i < 50000; i++ {
		raw := []byte(g.object(3, 1, `"x"`))
		raw = append(raw, `,"@extra":{"routing_key":"a"},"@client_id":1}`[1:]...)
		for m := r.IntN(4); m >= 0 && len(raw) > 0; m-- {
			pos := r.IntN(len(raw))
			switch r.IntN(3) {
			case 0:
				raw[pos] = alphabet[r.IntN(len(alphabet))]
			case 1:
				raw = append(raw[:pos], raw[pos+1:]...)
			default:
				raw = raw[:pos]
			}
		}
		exercise(append([]byte(nil), raw...))
	}
}

// TestSpliceIsLossless: whenever the fast path accepts a request, its output
// is valid JSON that adds routing_key == reply, in place of any the client
// sent, and changes nothing else.
func TestSpliceIsLossless(t *testing.T) {
	const reply = "_INBOX.Jz1YeQp1lXCkkqHnNpTGrL.lXCkkqHnNpTGrLJz1YeQp1"
	r := rand.New(rand.NewPCG(11, 11))
	g := &docgen{r: r}
	accepted := 0
	for i := 0; i < 60000; i++ {
		raw := []byte(g.object(3, 1, `"x"`))
		extraStart, extraEnd, _, ok := scanRequest(raw)
		if !ok || raw[extraStart] != '{' {
			continue
		}
		out, ok := spliceRoutingKey(raw, extraStart, extraEnd, reply)
		if !ok {
			continue // escaped member keys: forwardRequest leaves this to the general path
		}
		accepted++
		var orig, got map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&orig); err != nil {
			t.Fatalf("iter %d: fast path accepted a document encoding/json rejects (%v)\n%s", i, err, raw)
		}
		dec = json.NewDecoder(bytes.NewReader(out))
		dec.UseNumber()
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("iter %d: spliced output is not JSON: %v\nin:  %s\nout: %s", i, err, raw, out)
		}
		extra, _ := got["@extra"].(map[string]any)
		if extra["routing_key"] != reply {
			t.Fatalf("iter %d: routing_key missing or wrong\nin:  %s\nout: %s", i, raw, out)
		}
		delete(extra, "routing_key")
		if origExtra, _ := orig["@extra"].(map[string]any); origExtra != nil {
			delete(origExtra, "routing_key")
		}
		if !reflect.DeepEqual(orig, got) {
			t.Fatalf("iter %d: spliced output changed something else\nin:  %s\nout: %s", i, raw, out)
		}
	}
	if accepted < 20000 {
		t.Fatalf("only %d documents took the fast path; the test proves little", accepted)
	}
	t.Logf("%d documents took the fast path: all valid, all lossless", accepted)
}

func TestHotHelpersDoNotAllocate(t *testing.T) {
	upd := []byte(`{"@type":"updateNewMessage","message":{"@type":"message","id":538,"content":{"@type":"messageText","text":{"@type":"formattedText","text":"Hello <b>world</b> & welcome!"}}},"@client_id":1}`)
	if n := testing.AllocsPerRun(2000, func() { _ = jsonType(upd) }); n != 0 {
		t.Errorf("jsonType: %.0f allocs/op, want 0", n)
	}
	if n := testing.AllocsPerRun(2000, func() { _, _, _, _ = locateExtra(upd) }); n != 0 {
		t.Errorf("locateExtra(update): %.0f allocs/op, want 0", n)
	}

	req := []byte(`{"@type":"sendMessage","chat_id":-1001234567890,"input_message_content":{"@type":"inputMessageText","text":{"@type":"formattedText","text":"Hello <b>world</b>"}},"@extra":{"id":"0123456789abcdef01"}}`)
	extraStart, extraEnd, typ, ok := scanRequest(req)
	if !ok || string(typ) != "sendMessage" || req[extraStart] != '{' {
		t.Fatalf("scanRequest: ok=%v typ=%q", ok, typ)
	}
	if n := testing.AllocsPerRun(2000, func() { _, _, _, _ = scanRequest(req) }); n != 0 {
		t.Errorf("scanRequest: %.0f allocs/op, want 0", n)
	}
	if n := testing.AllocsPerRun(2000, func() {
		out, _ := spliceRoutingKey(req, extraStart, extraEnd, benchReplyLike)
		if len(out) <= len(req) {
			t.Fatal("spliceRoutingKey did not grow the request")
		}
	}); n != 1 {
		t.Errorf("spliceRoutingKey: %.0f allocs/op, want 1 (the output buffer)", n)
	}

	resp := []byte(`{"@type":"ok","text":"Hello <b>world</b>","@extra":{"id":"0123456789abcdef01","routing_key":"_INBOX.Jz1YeQp1lXCkkqHnNpTGrL.lXCkkqHnNpTGrLJz1YeQp1"},"@client_id":1}`)
	vs, ve, isResponse, _ := locateExtra(resp)
	if !isResponse {
		t.Fatal("response not recognised")
	}
	_, from, to, _ := cutMember(resp, vs, `"routing_key"`)
	pristine := append([]byte(nil), resp...)
	// the strip is undone after every run, so each run strips a pristine fixture
	if n := testing.AllocsPerRun(2000, func() {
		_, body, ok := stripRoutingKey(resp, vs, ve)
		if !ok || len(body) != len(resp)-(to-from) {
			t.Fatal("stripRoutingKey did not shorten the response")
		}
		copy(resp[from:], pristine[from:])
	}); n != 1 {
		t.Errorf("stripRoutingKey: %.0f allocs/op, want 1 (the reply subject; the response itself is edited in place)", n)
	}
}

const benchReplyLike = "_INBOX.Jz1YeQp1lXCkkqHnNpTGrL.lXCkkqHnNpTGrLJz1YeQp1"

// ---------------------------------------------------------------------------
// v0.3.0, for reference: processUpdate and processRequest as they were, minus
// the scheduler cases (the scheduler is gone), and with the one unchecked
// assertion that concerns a reply made safe.

func legacyProcessUpdate(srv *Server, update Data) {
	if srv.isDebug {
		fmt.Println("Received:", utils.UnsafeMarshalWithIndent(update))
	}

	if extra, exists := update["@extra"]; exists { // it's a response
		srv.requests_count.Add(1)

		extraMap := utils.AsMap(extra)

		if routingKey, ok := extraMap["routing_key"].(string); ok {
			delete(extraMap, "routing_key")
			srv.sendResponse(routingKey, update)
		} else { // local request
			if requestID, ok := extraMap["request_id"].(string); ok {
				if channel, found := srv.results.Get(requestID); found {
					srv.results.SafeSend(channel, update)
					srv.results.Delete(requestID)
				}
			}
		}
	} else { // it's an update
		srv.updates_count.Add(1)

		update_type := utils.Type(update)

		switch update_type {

		case "updateOption":
			srv.handleUpdateOption(update)
		case "updateAuthorizationState":
			srv.handleUpdateAuthorizationState(update)
		case "updateConnectionState":
			srv.handleUpdateConnectionState(update)
		case "updateUser":
			srv.handleUpdateUser(update)
		default:
			if _, exists := srv.broadcast_types[update_type]; exists {
				srv.broadcast(update)
			} else {
				srv.sendUpdate(update)
			}
		}
	}
}

func legacyProcessRequest(srv *Server, r *nats.Msg) {

	if r.Reply == "" || !srv.isRunning.Load() {
		return // invalid request
	}

	request, err := utils.UnmarshalBytes(r.Data)
	if err != nil {
		return
	}

	extra, ok := request["@extra"].(map[string]interface{})
	if !ok {
		return
	}

	switch utils.Type(request) {
	case "close":
		srv.handleCloseRequest(r, extra) // ignore close requests; returns OK immediately
	case "getCurrentState":
		srv.handleGetCurrentStateRequest(r, extra)
	case "getServerStats":
		srv.handleGetServerStatsRequest(r, extra)
	default:
		select {
		case <-srv.waitForReady:
		case <-srv.waitForClosed:
			return
		}
		extra["routing_key"] = r.Reply
		srv.send(request)
	}
}
