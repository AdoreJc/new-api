package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type headerCommitWriter struct {
	*httptest.ResponseRecorder
	headerStarted chan struct{}
	once          sync.Once
}

func (w *headerCommitWriter) WriteHeader(code int) {
	// Let the scanner start while the first keepalive snapshots the headers.
	w.once.Do(func() { close(w.headerStarted) })
	w.ResponseRecorder.WriteHeader(code)
}

func TestStreamScannerHandler_RequestKeepAliveHandoff(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousInterval := settings.PingIntervalEnabled, settings.PingIntervalSeconds
	settings.PingIntervalEnabled, settings.PingIntervalSeconds = true, 60
	t.Cleanup(func() {
		settings.PingIntervalEnabled, settings.PingIntervalSeconds = previousEnabled, previousInterval
	})

	for _, test := range []struct {
		name        string
		interval    time.Duration
		waitForPing bool
	}{
		{name: "before first keepalive", interval: time.Hour},
		{name: "during header commit", interval: time.Millisecond, waitForPing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &headerCommitWriter{
				ResponseRecorder: httptest.NewRecorder(),
				headerStarted:    make(chan struct{}),
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			stop := StartStreamKeepAlive(c, test.interval)
			defer stop()
			if test.waitForPing {
				select {
				case <-writer.headerStarted:
				case <-time.After(time.Second):
					t.Fatal("keepalive did not start")
				}
			}

			resp := &http.Response{
				Header: http.Header{
					"X-Reasoning-Included": {"true"},
					"X-Codex-Turn-State":   {"turn-state"},
				},
				Body: io.NopCloser(strings.NewReader("data: chunk\n\ndata: [DONE]\n\n")),
			}
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
			var received []string
			StreamScannerHandler(c, resp, info, func(data string, _ *StreamResult) {
				received = append(received, data)
				assert.NoError(t, StringData(c, data))
			})
			stop()

			require.NotNil(t, info.StreamStatus)
			assert.True(t, info.StreamStatus.IsNormalEnd())
			assert.Equal(t, []string{"chunk"}, received)
			assert.True(t, strings.HasSuffix(writer.Body.String(), "data: chunk\n\n"))
			if test.waitForPing {
				assert.True(t, strings.HasPrefix(writer.Body.String(), ": PING\n\n"))
			} else {
				assert.Equal(t, "data: chunk\n\n", writer.Body.String())
				assert.Equal(t, "true", writer.Result().Header.Get("X-Reasoning-Included"))
				assert.Equal(t, "turn-state", writer.Result().Header.Get("X-Codex-Turn-State"))
			}
		})
	}
}

func init() {
	gin.SetMode(gin.TestMode)
	if constant.StreamingTimeout == 0 {
		constant.StreamingTimeout = 30
	}
}

func setupStreamTest(t *testing.T, body io.Reader) (*gin.Context, *http.Response, *relaycommon.RelayInfo) {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	resp := &http.Response{
		Body: io.NopCloser(body),
	}

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{},
	}

	return c, resp, info
}

func buildSSEBody(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "data: {\"id\":%d,\"choices\":[{\"delta\":{\"content\":\"token_%d\"}}]}\n", i, i)
	}
	b.WriteString("data: [DONE]\n")
	return b.String()
}

// ---------- Basic correctness ----------

func TestStreamScannerHandler_NilInputs(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

	StreamScannerHandler(c, nil, info, func(data string, sr *StreamResult) {})
	StreamScannerHandler(c, &http.Response{Body: io.NopCloser(strings.NewReader(""))}, info, nil)
}

func TestNewStreamScanner_AllowsLargeStreamLine(t *testing.T) {
	oldBufferMB := constant.StreamScannerMaxBufferMB
	constant.StreamScannerMaxBufferMB = 1
	t.Cleanup(func() {
		constant.StreamScannerMaxBufferMB = oldBufferMB
	})

	payload := strings.Repeat("x", 128<<10)
	scanner := NewStreamScanner(strings.NewReader("data: " + payload + "\n"))
	scanner.Split(bufio.ScanLines)

	require.True(t, scanner.Scan())
	assert.Equal(t, "data: "+payload, scanner.Text())
	require.NoError(t, scanner.Err())
}

func TestStreamScannerHandler_EmptyBody(t *testing.T) {
	t.Parallel()

	c, resp, info := setupStreamTest(t, strings.NewReader(""))

	var called atomic.Bool
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		called.Store(true)
	})

	assert.False(t, called.Load(), "handler should not be called for empty body")
}

func TestStreamScannerHandler_1000Chunks(t *testing.T) {
	t.Parallel()

	const numChunks = 1000
	body := buildSSEBody(numChunks)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	var count atomic.Int64
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		count.Add(1)
	})

	assert.Equal(t, int64(numChunks), count.Load())
	assert.Equal(t, numChunks, info.ReceivedResponseCount)
}

func TestStreamScannerHandler_OrderPreserved(t *testing.T) {
	t.Parallel()

	const numChunks = 500
	body := buildSSEBody(numChunks)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	var mu sync.Mutex
	received := make([]string, 0, numChunks)

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		mu.Lock()
		received = append(received, data)
		mu.Unlock()
	})

	require.Equal(t, numChunks, len(received))
	for i := range numChunks {
		expected := fmt.Sprintf("{\"id\":%d,\"choices\":[{\"delta\":{\"content\":\"token_%d\"}}]}", i, i)
		assert.Equal(t, expected, received[i], "chunk %d out of order", i)
	}
}

func TestStreamScannerHandler_DoneStopsScanner(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		line string
		want []string
	}{
		{name: "bare", line: "[DONE]", want: []string{"before"}},
		{name: "bare with whitespace", line: " \t[DONE] \t\r", want: []string{"before"}},
		{name: "data prefixed", line: "data: [DONE]", want: []string{"before"}},
		{name: "data without space", line: "data:[DONE]", want: []string{"before"}},
		{name: "data with whitespace", line: "data: \t[DONE] \t\r", want: []string{"before"}},
		{name: "bare prefix is not done", line: "[DONE]extra", want: []string{"before", "after"}},
		{name: "data prefix is not done", line: "data: [DONE]extra", want: []string{"before", "[DONE]extra", "after"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := "data: before\n" + test.line + "\ndata: after\ndata: [DONE]\n"
			c, resp, info := setupStreamTest(t, strings.NewReader(body))
			info.DisablePing = true

			var received []string
			StreamScannerHandler(c, resp, info, func(data string, _ *StreamResult) {
				received = append(received, data)
			})

			assert.Equal(t, test.want, received)
			require.NotNil(t, info.StreamStatus)
			assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
			assert.NoError(t, info.StreamStatus.EndError)
			assert.Equal(t, len(test.want), info.ReceivedResponseCount)
		})
	}
}

func TestStreamScannerHandler_StopStopsStream(t *testing.T) {
	t.Parallel()

	const numChunks = 200
	body := buildSSEBody(numChunks)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	const stopAt int64 = 50
	var count atomic.Int64
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		n := count.Add(1)
		if n >= stopAt {
			sr.Stop(fmt.Errorf("fatal at %d", n))
		}
	})

	assert.Equal(t, stopAt, count.Load())
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
}

func TestStreamScannerHandler_SkipsNonDataLines(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString(": comment line\n")
	b.WriteString("event: message\n")
	b.WriteString("id: 12345\n")
	b.WriteString("retry: 5000\n")
	for i := range 100 {
		fmt.Fprintf(&b, "data: payload_%d\n", i)
		b.WriteString(": interleaved comment\n")
	}
	b.WriteString("data: [DONE]\n")

	c, resp, info := setupStreamTest(t, strings.NewReader(b.String()))

	var count atomic.Int64
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		count.Add(1)
	})

	assert.Equal(t, int64(100), count.Load())
}

func TestStreamScannerHandler_DataWithExtraSpaces(t *testing.T) {
	t.Parallel()

	body := "data:   {\"trimmed\":true}  \ndata: [DONE]\n"
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	var got string
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		got = data
	})

	assert.Equal(t, "{\"trimmed\":true}", got)
}

// TestStreamScannerHandler_ClientCancelAbortsUpstreamAndReturns pins the
// disconnect contract: when the client goes away, the handler must return
// promptly (all goroutines joined, so the gin.Context can never leak into a
// pooled reuse), the upstream body must be closed to stop token generation,
// and no data received after the disconnect may be processed or written.
func TestStreamScannerHandler_ClientCancelAbortsUpstreamAndReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	resp := &http.Response{Body: pr}
	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}

	var count atomic.Int64
	firstHandled := make(chan struct{})
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
			count.Add(1)
			_ = StringData(c, data)
			if data == "first" {
				close(firstHandled)
			}
		})
		close(done)
	}()

	_, err := fmt.Fprint(pw, "data: first\n")
	require.NoError(t, err)

	select {
	case <-firstHandled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	cancel()

	// The handler must return without any further upstream input: cleanup
	// closes resp.Body, which unblocks the scanner goroutine.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}

	// Upstream read side must be closed so the provider stops generating
	// (and billing) for a request nobody is listening to.
	_, err = fmt.Fprint(pw, "data: second\n")
	require.ErrorIs(t, err, io.ErrClosedPipe, "upstream body should be closed after client disconnect")

	assert.Equal(t, int64(1), count.Load(), "no chunk after disconnect should be processed")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonClientGone, info.StreamStatus.EndReason)

	body := recorder.Body.String()
	assert.Contains(t, body, "first")
	assert.NotContains(t, body, "second")
}

// ---------- Ping tests ----------

func TestStreamScannerHandler_PingSentDuringSlowUpstream(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	oldEnabled := setting.PingIntervalEnabled
	oldSeconds := setting.PingIntervalSeconds
	setting.PingIntervalEnabled = true
	setting.PingIntervalSeconds = 1
	t.Cleanup(func() {
		setting.PingIntervalEnabled = oldEnabled
		setting.PingIntervalSeconds = oldSeconds
	})

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for i := range 4 {
			fmt.Fprintf(pw, "data: chunk_%d\n", i)
			time.Sleep(400 * time.Millisecond)
		}
		fmt.Fprint(pw, "data: [DONE]\n")
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	resp := &http.Response{Body: pr}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

	var count atomic.Int64
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
			count.Add(1)
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream to finish")
	}

	assert.Equal(t, int64(4), count.Load())

	body := recorder.Body.String()
	pingCount := strings.Count(body, ": PING")
	assert.GreaterOrEqual(t, pingCount, 1,
		"expected at least 1 ping during slow stream with 1s interval; got %d", pingCount)
}

func TestStreamScannerHandler_PingDisabledByRelayInfo(t *testing.T) {
	setting := operation_setting.GetGeneralSetting()
	oldEnabled := setting.PingIntervalEnabled
	oldSeconds := setting.PingIntervalSeconds
	setting.PingIntervalEnabled = true
	setting.PingIntervalSeconds = 1
	t.Cleanup(func() {
		setting.PingIntervalEnabled = oldEnabled
		setting.PingIntervalSeconds = oldSeconds
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	resp := &http.Response{Body: io.NopCloser(strings.NewReader(buildSSEBody(5)))}
	info := &relaycommon.RelayInfo{
		DisablePing: true,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}

	var count atomic.Int64
	done := make(chan struct{})
	go func() {
		StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
			count.Add(1)
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}

	assert.Equal(t, int64(5), count.Load())

	body := recorder.Body.String()
	pingCount := strings.Count(body, ": PING")
	assert.Equal(t, 0, pingCount, "pings should be disabled when DisablePing=true")
}

// ---------- StreamStatus integration ----------

func TestStreamScannerHandler_StreamStatus_DoneReason(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(10)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Nil(t, info.StreamStatus.EndError)
	assert.True(t, info.StreamStatus.IsNormalEnd())
	assert.False(t, info.StreamStatus.HasErrors())
}

func TestStreamScannerHandler_StreamStatus_EOFWithoutDone(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := range 5 {
		fmt.Fprintf(&b, "data: {\"id\":%d}\n", i)
	}
	c, resp, info := setupStreamTest(t, strings.NewReader(b.String()))

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.IsNormalEnd())
}

func TestStreamScannerHandler_StreamStatus_HandlerStop(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(100)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	var count atomic.Int64
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		n := count.Add(1)
		if n >= 10 {
			sr.Stop(fmt.Errorf("stop at 10"))
		}
	})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
}

func TestStreamScannerHandler_StreamStatus_HandlerDone(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(20)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	var count atomic.Int64
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		n := count.Add(1)
		if n >= 5 {
			sr.Done()
		}
	})

	assert.Equal(t, int64(5), count.Load())
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.False(t, info.StreamStatus.HasErrors())
}

func TestStreamScannerHandler_StreamStatus_Timeout(t *testing.T) {
	// Not parallel: modifies global constant.StreamingTimeout
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	pr, pw := io.Pipe()
	go func() {
		fmt.Fprint(pw, "data: {\"id\":1}\n")
		time.Sleep(2 * time.Second)
		pw.Close()
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	resp := &http.Response{Body: pr}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}

	done := make(chan struct{})
	go func() {
		StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream timeout")
	}

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonTimeout, info.StreamStatus.EndReason)
	assert.False(t, info.StreamStatus.IsNormalEnd())
}

func TestStreamScannerHandler_StreamStatus_SoftErrors(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(10)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		sr.Error(fmt.Errorf("soft error for chunk"))
	})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasErrors())
	assert.Equal(t, 10, info.StreamStatus.TotalErrorCount())
}

func TestStreamScannerHandler_StreamStatus_MultipleErrorsPerChunk(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(5)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		sr.Error(fmt.Errorf("error A"))
		sr.Error(fmt.Errorf("error B"))
	})

	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Equal(t, 10, info.StreamStatus.TotalErrorCount())
}

func TestStreamScannerHandler_StreamStatus_ErrorThenStop(t *testing.T) {
	t.Parallel()

	// Use a large body without [DONE] to avoid race between scanner's [DONE]
	// and handler's Stop on the sync.Once EndReason.
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "data: {\"id\":%d}\n", i)
	}
	c, resp, info := setupStreamTest(t, strings.NewReader(b.String()))

	var count atomic.Int64
	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {
		count.Add(1)
		sr.Error(fmt.Errorf("soft error"))
		sr.Stop(fmt.Errorf("fatal"))
	})

	assert.Equal(t, int64(1), count.Load())
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonHandlerStop, info.StreamStatus.EndReason)
	assert.Equal(t, 2, info.StreamStatus.TotalErrorCount())
}

func TestStreamScannerHandler_StreamStatus_InitializedIfNil(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(1)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	assert.Nil(t, info.StreamStatus)

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {})

	assert.NotNil(t, info.StreamStatus)
}

func TestStreamScannerHandler_StreamStatus_ReplacesPreInitialized(t *testing.T) {
	t.Parallel()

	body := buildSSEBody(5)
	c, resp, info := setupStreamTest(t, strings.NewReader(body))

	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.RecordError("pre-existing error")

	StreamScannerHandler(c, resp, info, func(data string, sr *StreamResult) {})

	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.Equal(t, 0, info.StreamStatus.TotalErrorCount())
}

func TestNewStreamScannerCallerLimit(t *testing.T) {
	// The smaller buffer must actually constrain a line; a preallocated 64 KiB
	// buffer would otherwise bypass this caller's 1 KiB limit in bufio.Scanner.
	scanner := NewStreamScanner(strings.NewReader(strings.Repeat("x", 2048)+"\n"), 1024)
	assert.False(t, scanner.Scan())
	require.Error(t, scanner.Err())
	scanner = NewStreamScanner(strings.NewReader("data: ok\n"), 1024)
	require.True(t, scanner.Scan())
	assert.Equal(t, "data: ok", scanner.Text())
	require.NoError(t, scanner.Err())
}

// ---------- Write deadlines ----------

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func TestStreamWriteBudget(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	var budget StreamWriteBudget
	steps := []struct {
		name  string
		at    time.Duration
		bytes int
		want  time.Duration
	}{
		{"small event", 0, 8 << 10, 31 * time.Second},
		{"image event gets the time a slow client needs", 0, 3600 << 10, 481 * time.Second},
		{"next write keeps the image budget", 60 * time.Second, 8 << 10, 482 * time.Second},
		{"budget starts over once it ran out", 600 * time.Second, 8 << 10, 631 * time.Second},
	}
	for _, step := range steps {
		assert.Equal(t, start.Add(step.want), budget.Deadline(start.Add(step.at), step.bytes), step.name)
	}
}

func TestSetEventStreamHeadersBoundsStreamWritesByBudget(t *testing.T) {
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	SetEventStreamHeaders(c)

	before := time.Now()
	require.NoError(t, StringData(c, strings.Repeat("A", 3600<<10)))

	require.NotEmpty(t, recorder.deadlines)
	assert.False(t, recorder.deadlines[0].Before(before.Add(streamWriteTimeout+450*time.Second)))
	assert.Contains(t, recorder.Body.String(), "data: AAAA")
}

// Subscription OAuth channels pass only generic upstream headers to clients;
// the bound account's usage-limit headers reach only the user its binding
// serves, on the bound single-account channel, while the site option is on.
func TestSubscriptionOAuthResponseHeaders(t *testing.T) {
	claudeUpstream := http.Header{
		"Content-Type": {"application/json"},
		"Request-Id":   {"req_1"},
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.42"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {"1760000000"},
		"Anthropic-Ratelimit-Unified-Status":         {"allowed"},
		"Anthropic-Ratelimit-Tokens-Remaining":       {"100"},
		"Anthropic-Organization-Id":                  {"org-1"},
		"Retry-After":                                {"30"},
		"Cf-Ray":                                     {"ray-1"},
	}
	codexUpstream := http.Header{
		"Content-Type":                    {"text/event-stream"},
		"X-Codex-Turn-State":              {"turn-1"},
		"X-Reasoning-Included":            {"true"},
		"X-Codex-Primary-Used-Percent":    {"12"},
		"X-Codex-Secondary-Reset-At":      {"1760000000"},
		"X-Codex-Primary-Limit-Name":      {"5h"},
		"X-Codex-Credits-Balance":         {"3"},
		"X-Codex-Rate-Limit-Reached-Type": {"primary"},
		"X-Codex-Plan-Type":               {"plus"},
		"X-Codex-Active-Limit":            {"codex"},
		"Openai-Organization":             {"org-2"},
		"Chatgpt-Account-Id":              {"acct-1"},
	}
	claudeLimits := []string{"Anthropic-Ratelimit-Unified-5h-Utilization", "Anthropic-Ratelimit-Unified-7d-Reset", "Anthropic-Ratelimit-Unified-Status", "Retry-After"}
	codexLimits := []string{"X-Codex-Primary-Used-Percent", "X-Codex-Secondary-Reset-At", "X-Codex-Primary-Limit-Name", "X-Codex-Credits-Balance", "X-Codex-Rate-Limit-Reached-Type"}
	neverForOAuth := []string{"Anthropic-Ratelimit-Tokens-Remaining", "Anthropic-Organization-Id", "Cf-Ray", "X-Codex-Plan-Type", "X-Codex-Active-Limit", "Openai-Organization", "Chatgpt-Account-Id"}

	const boundChannel = 7
	bound := func() *relaycommon.SubscriptionRoute { return &relaycommon.SubscriptionRoute{ChannelId: boundChannel} }
	tests := []struct {
		name        string
		channelType int
		channelId   int
		multiKey    bool
		route       *relaycommon.SubscriptionRoute
		optionOff   bool
		wantLimits  bool
	}{
		{name: "unbound", channelId: boundChannel},
		{name: "bound on its channel", channelId: boundChannel, route: bound(), wantLimits: true},
		{name: "bound on another channel", channelId: boundChannel + 1, route: bound()},
		{name: "bound after fallback", channelId: boundChannel, route: &relaycommon.SubscriptionRoute{ChannelId: boundChannel, FellBack: true}},
		{name: "bound multi-key", channelId: boundChannel, multiKey: true, route: bound()},
		{name: "bound option off", channelId: boundChannel, route: bound(), optionOff: true},
	}
	for _, channelType := range []int{constant.ChannelTypeClaudeOAuth, constant.ChannelTypeCodex} {
		upstreamHeaders, limits := claudeUpstream, claudeLimits
		if channelType == constant.ChannelTypeCodex {
			upstreamHeaders, limits = codexUpstream, codexLimits
		}
		for _, tt := range tests {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("type=%d %s stream=%v", channelType, tt.name, stream), func(t *testing.T) {
					previous := operation_setting.SubscriptionRateLimitHeaderPassthrough
					operation_setting.SubscriptionRateLimitHeaderPassthrough = !tt.optionOff
					t.Cleanup(func() { operation_setting.SubscriptionRateLimitHeaderPassthrough = previous })

					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
					common.SetContextKey(c, constant.ContextKeyChannelId, tt.channelId)
					common.SetContextKey(c, constant.ContextKeyChannelIsMultiKey, tt.multiKey)
					if tt.route != nil {
						common.SetContextKey(c, constant.ContextKeySubscriptionRoute, tt.route)
					}
					upstream := &http.Response{StatusCode: http.StatusOK, Header: upstreamHeaders.Clone()}
					service.RecordSubscriptionLimitHeaders(c, upstream.Header)

					if stream {
						copyCodexSSEHeaders(c, upstream)
					} else {
						service.IOCopyBytesGracefully(c, upstream, []byte("{}"))
					}
					got := c.Writer.Header()

					for _, name := range limits {
						if tt.wantLimits {
							assert.Equal(t, upstreamHeaders.Get(name), got.Get(name), name)
						} else {
							assert.Empty(t, got.Values(name), name)
						}
					}
					for _, name := range neverForOAuth {
						assert.Empty(t, got.Values(name), name)
					}
					if channelType == constant.ChannelTypeCodex {
						assert.Equal(t, "turn-1", got.Get("X-Codex-Turn-State"))
						assert.Equal(t, "true", got.Get("X-Reasoning-Included"))
					}
					if !stream {
						assert.Equal(t, upstreamHeaders.Get("Content-Type"), got.Get("Content-Type"))
					}
				})
			}
		}
	}

	t.Run("anthropic api key channel keeps passing upstream headers", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeAnthropic)
		common.SetContextKey(c, constant.ContextKeyChannelId, boundChannel)
		common.SetContextKey(c, constant.ContextKeySubscriptionRoute, bound())
		upstream := &http.Response{StatusCode: http.StatusOK, Header: claudeUpstream.Clone()}

		service.IOCopyBytesGracefully(c, upstream, []byte("{}"))

		for name := range claudeUpstream {
			assert.Equal(t, claudeUpstream.Get(name), recorder.Header().Get(name), name)
		}
	})
}
