package server

import (
	"bytes"
	"strconv"
	"unsafe"
)

// Locating members in the JSON of TDLib and pytdbot without decoding it.
//
// TDLib (td/telegram/ClientJson.cpp) and pytdbot both emit compact JSON. The
// helpers below walk the structure just far enough to find a member. They
// never build maps, and on the hot paths they do not allocate. Anything that
// is not strictly well-formed JSON makes them report ok == false, so callers
// fall back to the general, decoding path and behave exactly as before.

// member visits one member of a JSON object: its key (still quoted), the index
// of its first byte, the index of the comma before it (-1 for the first
// member) and the byte range of its value. Returning false stops the walk.
type member func(key []byte, keyStart, prevComma, valStart, valEnd int) bool

const clientIDKey = `,"@client_id":`

var (
	clientIDKeyBytes = []byte(clientIDKey)
	extraNameBytes   = []byte(`"@extra"`)
)

// eq reports whether b equals s without allocating.
func eq(b []byte, s string) bool {
	return string(b) == s
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	return i
}

// skipString expects the opening quote at i and returns the index just past
// the closing one, or -1.
func skipString(b []byte, i int) int {
	for {
		j := bytes.IndexByte(b[i+1:], '"')
		if j < 0 {
			return -1
		}
		i += 1 + j // i is on a quote
		escapes := 0
		for k := i - 1; k >= 0 && b[k] == '\\'; k-- {
			escapes++
		}
		if escapes&1 == 0 {
			return i + 1
		}
	}
}

// maxDepth bounds nesting; deeper input is left to the general path.
const maxDepth = 200

// skipValue expects a JSON value at i (after optional whitespace) and returns
// the index just past it, or -1 unless it is well-formed JSON. The content of
// strings is not checked (no encoder emits bad escapes or raw control bytes).
func skipValue(b []byte, i, depth int) int {
	i = skipSpace(b, i)
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{':
		end, ok := scanObjectDepth(b, i, depth, nil)
		if !ok {
			return -1
		}
		return end
	case '[':
		return skipArray(b, i, depth)
	case 't':
		return matchLiteral(b, i, "true")
	case 'f':
		return matchLiteral(b, i, "false")
	case 'n':
		return matchLiteral(b, i, "null")
	}
	return skipNumber(b, i)
}

// skipArray expects '[' at i.
func skipArray(b []byte, i, depth int) int {
	if depth++; depth > maxDepth {
		return -1
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == ']' {
		return i + 1
	}
	for {
		if i = skipValue(b, i, depth); i < 0 {
			return -1
		}
		switch i = skipSpace(b, i); {
		case i < len(b) && b[i] == ',':
			i++ // a value must follow: "1,]" fails in skipValue
		case i < len(b) && b[i] == ']':
			return i + 1
		default:
			return -1
		}
	}
}

// matchLiteral expects lit at i, followed by a delimiter.
func matchLiteral(b []byte, i int, lit string) int {
	if len(b)-i < len(lit) || string(b[i:i+len(lit)]) != lit {
		return -1
	}
	i += len(lit)
	if i < len(b) && !isSpace(b[i]) && b[i] != ',' && b[i] != '}' && b[i] != ']' {
		return -1
	}
	return i
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// skipNumber expects a JSON number at i: -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func skipNumber(b []byte, i int) int {
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && '1' <= b[i] && b[i] <= '9':
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	default:
		return -1
	}
	if i < len(b) && b[i] == '.' {
		if i++; i >= len(b) || !isDigit(b[i]) {
			return -1
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		if i++; i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || !isDigit(b[i]) {
			return -1
		}
		for i < len(b) && isDigit(b[i]) {
			i++
		}
	}
	return i
}

// scanObject walks the object whose opening brace is at raw[i] and returns the
// index just past its closing brace. ok is false if raw is not a well-formed
// object. If fn stops the walk, the rest of raw is not checked.
func scanObject(raw []byte, i int, fn member) (end int, ok bool) {
	return scanObjectDepth(raw, i, 0, fn)
}

func scanObjectDepth(raw []byte, i, depth int, fn member) (end int, ok bool) {
	if depth++; depth > maxDepth {
		return 0, false
	}
	i = skipSpace(raw, i+1)
	if i < len(raw) && raw[i] == '}' {
		return i + 1, true
	}
	prevComma := -1
	for {
		if i >= len(raw) || raw[i] != '"' {
			return 0, false
		}
		keyStart := i
		if i = skipString(raw, i); i < 0 {
			return 0, false
		}
		key := raw[keyStart:i]
		if i = skipSpace(raw, i); i >= len(raw) || raw[i] != ':' {
			return 0, false
		}
		valStart := skipSpace(raw, i+1)
		valEnd := skipValue(raw, valStart, depth)
		if valEnd < 0 {
			return 0, false
		}
		if fn != nil && !fn(key, keyStart, prevComma, valStart, valEnd) {
			return valEnd, true
		}
		if i = skipSpace(raw, valEnd); i >= len(raw) {
			return 0, false
		}
		if raw[i] == ',' {
			prevComma = i
			if i = skipSpace(raw, i+1); i < len(raw) && raw[i] == '}' {
				return 0, false // trailing comma
			}
			continue
		}
		if raw[i] == '}' {
			return i + 1, true
		}
		return 0, false
	}
}

// unescaped returns the content of a JSON string value, or nil.
func unescaped(raw []byte, valStart, valEnd int) []byte {
	if valEnd-valStart < 2 || raw[valStart] != '"' || raw[valEnd-1] != '"' ||
		bytes.IndexByte(raw[valStart:valEnd], '\\') >= 0 {
		return nil
	}
	return raw[valStart+1 : valEnd-1]
}

// jsonType returns the "@type" of a TDLib object, unquoted, or nil. TDLib
// writes "@type" first and once.
func jsonType(raw []byte) []byte {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var t []byte
	scanObject(raw, 0, func(key []byte, _, _, valStart, valEnd int) bool {
		if eq(key, `"@type"`) {
			t = unescaped(raw, valStart, valEnd)
			return false
		}
		return true
	})
	return t
}

// extraWindow is how far back from the end of a message the "@extra" key of a
// response is looked for before the whole message is searched.
const extraWindow = 512

// locateExtra classifies a message from TDLib. For a response it returns the
// byte range of the top-level "@extra" value with isResponse set.
//
// TDLib appends "@extra" as the last object member, before "@client_id"
// (td/telegram/ClientJson.cpp), so for a response the key sits right at the
// end: the last extraWindow bytes are searched first and a hit is checked by
// parsing forward from it, which must reach the end of the root object. No
// nested "@extra" can pass that, so a key inside user data is never taken for
// it. Only when nothing is found there is the whole message searched once,
// which is what an update costs.
//
// seen reports whether "@extra" occurs anywhere at all; seen without
// isResponse means an unusual layout, which callers leave to the general path.
func locateExtra(raw []byte) (valStart, valEnd int, isResponse, seen bool) {
	start := len(raw) - extraWindow
	if start < 0 {
		start = 0
	}
	if valStart, valEnd, isResponse, seen = extraIn(raw, start); isResponse || start == 0 {
		return
	}
	var seenHead bool
	if valStart, valEnd, isResponse, seenHead = extraIn(raw, 0); !seen {
		seen = seenHead
	}
	return
}

// extraIn searches raw[from:] for the "@extra" member of a TDLib response.
func extraIn(raw []byte, from int) (valStart, valEnd int, found, seen bool) {
	for i := from; ; i++ {
		k := bytes.Index(raw[i:], extraNameBytes)
		if k < 0 {
			return 0, 0, false, seen
		}
		k += i
		seen = true
		if valStart, valEnd, found = tailExtraAt(raw, k); found {
			return
		}
	}
}

// tailExtraAt expects the key "@extra" at raw[k] and checks that it is the
// member TDLib appends last, in its compact layout ,"@extra":<value> or
// {"@extra":<value>}: the value must be followed by the end of the root
// object, optionally after "@client_id".
func tailExtraAt(raw []byte, k int) (valStart, valEnd int, ok bool) {
	colon := k + len(extraNameBytes)
	if k == 0 || colon >= len(raw) || raw[colon] != ':' ||
		(raw[k-1] != ',' && raw[k-1] != '{') {
		return 0, 0, false
	}
	valStart = skipSpace(raw, colon+1)
	if valEnd = skipValue(raw, valStart, 0); valEnd < 0 {
		return 0, 0, false
	}
	end := len(raw)
	for end > 0 && isSpace(raw[end-1]) {
		end--
	}
	j := skipSpace(raw, valEnd)
	if j >= end {
		return 0, 0, false
	}
	if raw[j] == ',' {
		if !bytes.HasPrefix(raw[j:end], clientIDKeyBytes) {
			return 0, 0, false
		}
		if j = skipValue(raw, j+len(clientIDKey), 0); j < 0 {
			return 0, 0, false
		}
		j = skipSpace(raw, j)
	}
	if j != end-1 || raw[j] != '}' {
		return 0, 0, false
	}
	return valStart, valEnd, true
}

// scanRequest walks a whole request object once. It reports the last top-level
// "@extra" and "@type" (with duplicate keys a decoder keeps the last one).
// ok is false unless raw is a complete, well-formed JSON object that has an
// "@extra" member.
func scanRequest(raw []byte) (extraStart, extraEnd int, typ []byte, ok bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return 0, 0, nil, false
	}
	end, wellFormed := scanObject(raw, 0, func(key []byte, _, _, valStart, valEnd int) bool {
		switch string(key) {
		case `"@extra"`:
			extraStart, extraEnd, ok = valStart, valEnd, true
		case `"@type"`:
			typ = unescaped(raw, valStart, valEnd)
		}
		return true
	})
	if !wellFormed || skipSpace(raw, end) != len(raw) {
		return 0, 0, nil, false
	}
	return extraStart, extraEnd, typ, ok
}

// cutMember finds member name in the object whose opening brace is at
// raw[objStart] and returns the span to cut out to remove it cleanly: the
// member plus the comma after it, or else the comma before it.
func cutMember(raw []byte, objStart int, name string) (val []byte, from, to int, ok bool) {
	scanObject(raw, objStart, func(key []byte, keyStart, prevComma, valStart, valEnd int) bool {
		if !eq(key, name) {
			return true
		}
		val, ok = raw[valStart:valEnd], true
		if next := skipSpace(raw, valEnd); next < len(raw) && raw[next] == ',' {
			from, to = keyStart, next+1
		} else if prevComma >= 0 {
			from, to = prevComma, valEnd
		} else {
			from, to = objStart+1, valEnd
		}
		return false
	})
	return val, from, to, ok
}

// jsonNestedInt returns the integer at obj -> inner of a TDLib object. Numbers
// are accepted in quotes too: older TDLib versions serialise int53 as strings.
func jsonNestedInt(raw []byte, obj, inner string) (int64, bool) {
	var outer, val []byte
	scanObject(raw, 0, func(key []byte, _, _, vs, ve int) bool {
		if !eq(key, obj) {
			return true
		}
		outer = raw[vs:ve]
		return false
	})
	if len(outer) == 0 || outer[0] != '{' {
		return 0, false
	}
	scanObject(outer, 0, func(key []byte, _, _, vs, ve int) bool {
		if !eq(key, inner) {
			return true
		}
		val = outer[vs:ve]
		return false
	})
	if n := len(val); n >= 2 && val[0] == '"' && val[n-1] == '"' {
		val = val[1 : n-1]
	}
	n, err := strconv.ParseInt(string(val), 10, 64)
	return n, err == nil
}

// unquote returns a JSON string value as a Go string.
func unquote(b []byte) (string, bool) {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return "", false
	}
	if bytes.IndexByte(b, '\\') < 0 {
		return string(b[1 : len(b)-1]), true
	}
	s, err := strconv.Unquote(string(b))
	return s, err == nil
}

// bytesToString returns b as a string without copying it. Only for bytes that
// are handed on and never changed afterwards: tdjson.Send copies them into C
// memory before it returns.
func bytesToString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// jsonSafe reports whether s is copied to JSON verbatim, with nothing to escape.
func jsonSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// spliceRoutingKey returns raw with "routing_key": reply as the first member of
// the "@extra" object at raw[extraStart:extraEnd], in place of any routing_key
// members the client sent. Other members are copied as written; only the
// whitespace between them is normalised. The result is what decoding the
// request, setting extra["routing_key"] = reply and encoding it again produces,
// without doing either. ok is false if a member key is written with escapes,
// which could spell "routing_key" some other way.
func spliceRoutingKey(raw []byte, extraStart, extraEnd int, reply string) (out []byte, ok bool) {
	out = make([]byte, 0, len(raw)+len(`,"routing_key":"",`)+len(reply))
	out = append(out, raw[:extraStart+1]...)
	out = append(out, `"routing_key":"`...)
	out = append(out, reply...)
	out = append(out, '"')
	ok = true
	scanObject(raw, extraStart, func(key []byte, keyStart, _, _, valEnd int) bool {
		if eq(key, `"routing_key"`) {
			return true
		}
		if bytes.IndexByte(key, '\\') >= 0 {
			ok = false
			return false
		}
		out = append(out, ',')
		out = append(out, raw[keyStart:valEnd]...)
		return true
	})
	out = append(out, '}')
	return append(out, raw[extraEnd:]...), ok
}

// stripRoutingKey removes "routing_key" from the top-level "@extra" of a TDLib
// response, whose value locateExtra found at raw[valStart:valEnd], and returns
// the reply subject it held. The removal happens in place, so a response is
// never copied: only the few bytes behind the member move. ok is false when the
// response has no such member, i.e. it answers a request the server made itself.
func stripRoutingKey(raw []byte, valStart, valEnd int) (reply string, out []byte, ok bool) {
	val, from, to, ok := cutMember(raw, valStart, `"routing_key"`)
	if !ok {
		return "", nil, false
	}
	if reply, ok = unquote(val); !ok {
		return "", nil, false
	}
	copy(raw[from:], raw[to:])
	return reply, raw[:len(raw)-(to-from)], true
}
