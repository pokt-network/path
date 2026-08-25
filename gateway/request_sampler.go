package gateway

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// RequestSampler answers one question about a service's traffic: is it many different
// requests, or the same few requests over and over?
//
// Why it exists. Every quality signal PATH has — latency, success rate, hedge wins, the
// reputation score — rewards an endpoint that answers fast. An endpoint fronted by a cache
// answers a repeated request in sub-millisecond time without touching a node, so against
// repetitive traffic it wins every race and accumulates every reward, while against unique
// traffic it is an ordinary node. Whether a fast operator is fast or merely cached therefore
// cannot be read from the operator; it has to be read from the traffic. Nothing in PATH
// recorded what the traffic looked like: the method label exists, the params do not, and a
// thousand getAccountInfo calls for a thousand accounts and a thousand for the same account
// are one number.
//
// What it records. One request in every `rate` is fingerprinted: for JSON-RPC, each item's
// method plus its params with the JSON compacted (so formatting and the request id do not
// split one logical request into many fingerprints); for anything else, the HTTP method,
// path and compacted body. Fingerprints are counted per service in a fixed-length window;
// the previous completed window is kept so a reader always has one full window to look at.
// The table is bounded: past maxFingerprints distinct entries, new fingerprints are counted
// in an overflow bucket rather than stored — the report says so, and a large overflow is
// itself the answer (the traffic is diverse).
//
// What it costs. One hash per sampled request and a bounded table per service. No label on
// any metric carries a fingerprint or a method; the two gauges are per service_id only.
//
// What it cannot tell. It sees requests, not clients — PATH has no client identity behind
// the edge — so "the same request over and over" cannot be attributed to one sender. And a
// low uniqueness ratio is a property of the traffic, not evidence against any operator: it
// says the conditions under which a cache wins are present, not that anyone is running one.
type RequestSampler struct {
	logger          polylog.Logger
	rate            uint64
	window          time.Duration
	maxFingerprints int
	snippetBytes    int
	now             func() time.Time

	counter atomic.Uint64

	mu       sync.Mutex
	services map[protocol.ServiceID]*serviceSample
}

type serviceSample struct {
	current  *sampleWindow
	previous *sampleWindow
}

type sampleWindow struct {
	start        time.Time
	end          time.Time // zero while current
	requestsSeen uint64    // every request, sampled or not
	sampled      uint64    // fingerprinted items (a batch contributes one per item)
	overflow     uint64    // items whose fingerprint was new but the table was full
	fingerprints map[uint64]*fingerprintEntry
	methods      map[string]*methodSample
}

type fingerprintEntry struct {
	method    string
	snippet   string
	count     uint64
	firstSeen time.Time
	lastSeen  time.Time
}

type methodSample struct {
	sampled  uint64
	distinct uint64
}

const (
	defaultRequestSampleRate         = 100
	defaultRequestSampleWindow       = 10 * time.Minute
	defaultRequestSampleMaxFPs       = 5000
	defaultRequestSampleSnippetBytes = 200
	// requestSampleMaxWindowBytes caps how much of a body is hashed and snippeted. Bodies
	// past it are fingerprinted on their prefix — a huge batch still gets one fingerprint
	// per item up to the limit.
	requestSampleMaxBodyBytes = 1 << 20
)

// NewRequestSamplerFromEnv builds a sampler from PATH_REQUEST_SAMPLE_RATE (1-in-N, default
// 100, 0 disables), PATH_REQUEST_SAMPLE_WINDOW (Go duration, default 10m) and
// PATH_REQUEST_SAMPLE_MAX_FINGERPRINTS (default 5000). Returns nil when disabled so the
// gateway and the admin endpoint can both treat "no sampler" uniformly.
func NewRequestSamplerFromEnv(logger polylog.Logger) *RequestSampler {
	rate := uint64(defaultRequestSampleRate)
	if v := os.Getenv("PATH_REQUEST_SAMPLE_RATE"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			logger.Warn().Str("value", v).Msg("PATH_REQUEST_SAMPLE_RATE is not an unsigned integer; using default")
		} else {
			rate = n
		}
	}
	if rate == 0 {
		logger.Info().Msg("request sampling disabled (PATH_REQUEST_SAMPLE_RATE=0)")
		return nil
	}
	window := defaultRequestSampleWindow
	if v := os.Getenv("PATH_REQUEST_SAMPLE_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			logger.Warn().Str("value", v).Msg("PATH_REQUEST_SAMPLE_WINDOW is not a positive duration; using default")
		} else {
			window = d
		}
	}
	maxFPs := defaultRequestSampleMaxFPs
	if v := os.Getenv("PATH_REQUEST_SAMPLE_MAX_FINGERPRINTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			logger.Warn().Str("value", v).Msg("PATH_REQUEST_SAMPLE_MAX_FINGERPRINTS is not a positive integer; using default")
		} else {
			maxFPs = n
		}
	}
	return NewRequestSampler(logger, rate, window, maxFPs)
}

// NewRequestSampler builds a sampler that fingerprints one request in every `rate`.
func NewRequestSampler(logger polylog.Logger, rate uint64, window time.Duration, maxFingerprints int) *RequestSampler {
	if rate == 0 {
		rate = 1
	}
	return &RequestSampler{
		logger:          logger.With("component", "request_sampler"),
		rate:            rate,
		window:          window,
		maxFingerprints: maxFingerprints,
		snippetBytes:    defaultRequestSampleSnippetBytes,
		now:             time.Now,
		services:        make(map[protocol.ServiceID]*serviceSample),
	}
}

// Observe is the gateway's hook: called once per HTTP service request after the service ID
// is known. Cheap when the request is not the one-in-N sampled: one atomic increment and
// one counter bump under the lock.
func (s *RequestSampler) Observe(serviceID protocol.ServiceID, httpMethod, path string, body []byte) {
	if s == nil {
		return
	}
	n := s.counter.Add(1)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	w := s.currentWindowLocked(serviceID, now)
	w.requestsSeen++
	if n%s.rate != 0 {
		return
	}
	if len(body) > requestSampleMaxBodyBytes {
		body = body[:requestSampleMaxBodyBytes]
	}
	for _, item := range fingerprintRequest(httpMethod, path, body) {
		s.recordLocked(w, item, now)
	}
}

func (s *RequestSampler) recordLocked(w *sampleWindow, item requestFingerprint, now time.Time) {
	w.sampled++
	ms := w.methods[item.method]
	if ms == nil {
		ms = &methodSample{}
		w.methods[item.method] = ms
	}
	ms.sampled++

	if e, ok := w.fingerprints[item.hash]; ok {
		e.count++
		e.lastSeen = now
		return
	}
	ms.distinct++
	if len(w.fingerprints) >= s.maxFingerprints {
		w.overflow++
		return
	}
	snippet := item.canonical
	if len(snippet) > s.snippetBytes {
		snippet = snippet[:s.snippetBytes] + "…"
	}
	w.fingerprints[item.hash] = &fingerprintEntry{
		method:    item.method,
		snippet:   snippet,
		count:     1,
		firstSeen: now,
		lastSeen:  now,
	}
}

// currentWindowLocked returns the service's live window, rotating it if it has run past
// its length. Rotation is what publishes the gauges: they describe the last COMPLETED
// window, so a reader never sees a ratio computed over three samples.
func (s *RequestSampler) currentWindowLocked(serviceID protocol.ServiceID, now time.Time) *sampleWindow {
	svc := s.services[serviceID]
	if svc == nil {
		svc = &serviceSample{current: newSampleWindow(now)}
		s.services[serviceID] = svc
		return svc.current
	}
	if now.Sub(svc.current.start) >= s.window {
		svc.current.end = now
		svc.previous = svc.current
		svc.current = newSampleWindow(now)
		s.publishLocked(serviceID, svc.previous)
	}
	return svc.current
}

func newSampleWindow(now time.Time) *sampleWindow {
	return &sampleWindow{
		start:        now,
		fingerprints: make(map[uint64]*fingerprintEntry),
		methods:      make(map[string]*methodSample),
	}
}

func (s *RequestSampler) publishLocked(serviceID protocol.ServiceID, w *sampleWindow) {
	if w.sampled == 0 {
		return
	}
	distinct := uint64(len(w.fingerprints)) + w.overflow
	var top1 uint64
	for _, e := range w.fingerprints {
		if e.count > top1 {
			top1 = e.count
		}
	}
	metrics.SetRequestSampleUniqueness(string(serviceID),
		float64(distinct)/float64(w.sampled),
		float64(top1)/float64(w.sampled))
}

// requestFingerprint is one logical request: a JSON-RPC item, or a whole non-JSON-RPC body.
type requestFingerprint struct {
	method    string
	canonical string
	hash      uint64
}

// fingerprintRequest reduces a body to its fingerprints. JSON-RPC (single or batch): one per
// item, keyed on method + compacted params, id deliberately excluded. Anything else: one
// fingerprint for the HTTP method, path and compacted body. Malformed JSON falls back to the
// raw bytes so a garbage request still counts as a request.
func fingerprintRequest(httpMethod, path string, body []byte) []requestFingerprint {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		if fps := fingerprintJSONRPC(trimmed); len(fps) > 0 {
			return fps
		}
	}
	canonical := httpMethod + " " + path
	if len(trimmed) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, trimmed); err == nil {
			canonical += " " + compact.String()
		} else {
			canonical += " " + string(trimmed)
		}
	}
	return []requestFingerprint{{method: httpMethod + " " + path, canonical: canonical, hash: fnvHash(canonical)}}
}

type jsonrpcItemForFingerprint struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func fingerprintJSONRPC(body []byte) []requestFingerprint {
	var items []jsonrpcItemForFingerprint
	if body[0] == '[' {
		if err := json.Unmarshal(body, &items); err != nil {
			return nil
		}
	} else {
		var single jsonrpcItemForFingerprint
		if err := json.Unmarshal(body, &single); err != nil {
			return nil
		}
		items = []jsonrpcItemForFingerprint{single}
	}
	out := make([]requestFingerprint, 0, len(items))
	for _, it := range items {
		if it.Method == "" {
			continue
		}
		canonical := it.Method
		if len(it.Params) > 0 {
			var compact bytes.Buffer
			if err := json.Compact(&compact, it.Params); err == nil {
				canonical += " " + compact.String()
			} else {
				canonical += " " + string(it.Params)
			}
		}
		out = append(out, requestFingerprint{method: it.Method, canonical: canonical, hash: fnvHash(canonical)})
	}
	return out
}

func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// ---------------------------------------------------------------------------
// Reporting — backs GET /admin/request-sample/{serviceId}
// ---------------------------------------------------------------------------

// RequestSampleReport is the JSON body of the admin endpoint for one service and window.
type RequestSampleReport struct {
	ServiceID      string    `json:"service_id"`
	Window         string    `json:"window"` // "current" | "previous"
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end,omitempty"`
	WindowLength   string    `json:"window_length"`
	SampleRate     string    `json:"sample_rate"` // "1-in-N"
	RequestsSeen   uint64    `json:"requests_seen"`
	Sampled        uint64    `json:"sampled"`
	Distinct       uint64    `json:"distinct"`
	TableOverflow  uint64    `json:"table_overflow"`
	MaxFingerprint int       `json:"max_fingerprints"`
	// Uniqueness = distinct / sampled. 1.0 = every sampled request different; near 0 = the
	// same few requests repeated.
	Uniqueness float64 `json:"uniqueness"`
	// Top1Share / TopNShare = fraction of sampled requests that were the single most
	// repeated fingerprint / the N listed below. A high Top1Share on a method where
	// parameters should vary (account lookups, transactions) is the shape to look for.
	Top1Share float64                    `json:"top1_share"`
	TopNShare float64                    `json:"topn_share"`
	Methods   []RequestSampleMethodEntry `json:"methods"`
	Top       []RequestSampleEntry       `json:"top"`
}

// RequestSampleMethodEntry gives per-method sampled vs distinct counts. Uniqueness per
// method is the more telling number: block-height calls are legitimately repetitive,
// account lookups are not.
type RequestSampleMethodEntry struct {
	Method     string  `json:"method"`
	Sampled    uint64  `json:"sampled"`
	Distinct   uint64  `json:"distinct"`
	Uniqueness float64 `json:"uniqueness"`
	Share      float64 `json:"share"`
}

// RequestSampleEntry is one fingerprint.
type RequestSampleEntry struct {
	Method    string    `json:"method"`
	Count     uint64    `json:"count"`
	Share     float64   `json:"share"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Snippet   string    `json:"snippet"`
}

// RequestSampleSummary is one row of GET /admin/request-sample (all services).
type RequestSampleSummary struct {
	ServiceID    string  `json:"service_id"`
	Window       string  `json:"window"`
	RequestsSeen uint64  `json:"requests_seen"`
	Sampled      uint64  `json:"sampled"`
	Distinct     uint64  `json:"distinct"`
	Uniqueness   float64 `json:"uniqueness"`
	Top1Share    float64 `json:"top1_share"`
	TopMethod    string  `json:"top_method"`
}

// Report renders one service's window. previous=true reads the last completed window;
// otherwise the live one. found=false when the service has not been observed at all.
func (s *RequestSampler) Report(serviceID string, previous bool, top int) (report RequestSampleReport, found bool) {
	if s == nil {
		return RequestSampleReport{}, false
	}
	if top <= 0 {
		top = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	svc := s.services[protocol.ServiceID(serviceID)]
	if svc == nil {
		return RequestSampleReport{}, false
	}
	// Rotate if due, so "previous" is never staler than one window.
	s.currentWindowLocked(protocol.ServiceID(serviceID), s.now())
	w, label := svc.current, "current"
	if previous {
		if svc.previous == nil {
			return RequestSampleReport{}, false
		}
		w, label = svc.previous, "previous"
	}
	return s.renderLocked(serviceID, label, w, top), true
}

func (s *RequestSampler) renderLocked(serviceID, label string, w *sampleWindow, top int) RequestSampleReport {
	r := RequestSampleReport{
		ServiceID:      serviceID,
		Window:         label,
		WindowStart:    w.start,
		WindowEnd:      w.end,
		WindowLength:   s.window.String(),
		SampleRate:     "1-in-" + strconv.FormatUint(s.rate, 10),
		RequestsSeen:   w.requestsSeen,
		Sampled:        w.sampled,
		Distinct:       uint64(len(w.fingerprints)) + w.overflow,
		TableOverflow:  w.overflow,
		MaxFingerprint: s.maxFingerprints,
	}
	if w.sampled == 0 {
		return r
	}
	r.Uniqueness = float64(r.Distinct) / float64(w.sampled)

	entries := make([]RequestSampleEntry, 0, len(w.fingerprints))
	for _, e := range w.fingerprints {
		entries = append(entries, RequestSampleEntry{
			Method:    e.method,
			Count:     e.count,
			Share:     float64(e.count) / float64(w.sampled),
			FirstSeen: e.firstSeen,
			LastSeen:  e.lastSeen,
			Snippet:   e.snippet,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Snippet < entries[j].Snippet
	})
	if len(entries) > 0 {
		r.Top1Share = entries[0].Share
	}
	if len(entries) > top {
		entries = entries[:top]
	}
	for _, e := range entries {
		r.TopNShare += e.Share
	}
	r.Top = entries

	r.Methods = make([]RequestSampleMethodEntry, 0, len(w.methods))
	for m, ms := range w.methods {
		r.Methods = append(r.Methods, RequestSampleMethodEntry{
			Method:     m,
			Sampled:    ms.sampled,
			Distinct:   ms.distinct,
			Uniqueness: float64(ms.distinct) / float64(ms.sampled),
			Share:      float64(ms.sampled) / float64(w.sampled),
		})
	}
	sort.Slice(r.Methods, func(i, j int) bool {
		if r.Methods[i].Sampled != r.Methods[j].Sampled {
			return r.Methods[i].Sampled > r.Methods[j].Sampled
		}
		return r.Methods[i].Method < r.Methods[j].Method
	})
	return r
}

// Summary renders one row per observed service, sorted by requests seen. Uses the
// previous (completed) window when there is one, otherwise the live one.
func (s *RequestSampler) Summary() []RequestSampleSummary {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]RequestSampleSummary, 0, len(s.services))
	for id := range s.services {
		s.currentWindowLocked(id, now)
		svc := s.services[id]
		w, label := svc.current, "current"
		if svc.previous != nil {
			w, label = svc.previous, "previous"
		}
		row := RequestSampleSummary{ServiceID: string(id), Window: label, RequestsSeen: w.requestsSeen, Sampled: w.sampled}
		row.Distinct = uint64(len(w.fingerprints)) + w.overflow
		if w.sampled > 0 {
			row.Uniqueness = float64(row.Distinct) / float64(w.sampled)
			var top1 uint64
			for _, e := range w.fingerprints {
				if e.count > top1 {
					top1 = e.count
				}
			}
			row.Top1Share = float64(top1) / float64(w.sampled)
			var topM string
			var topN uint64
			for m, ms := range w.methods {
				if ms.sampled > topN || (ms.sampled == topN && m < topM) {
					topM, topN = m, ms.sampled
				}
			}
			row.TopMethod = topM
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestsSeen != out[j].RequestsSeen {
			return out[i].RequestsSeen > out[j].RequestsSeen
		}
		return out[i].ServiceID < out[j].ServiceID
	})
	return out
}
