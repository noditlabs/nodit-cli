package cli

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeStream struct {
	mu          sync.Mutex
	reads       [][]byte
	writes      [][]byte
	messageType int
	closed      bool
	readErr     error
}

func (f *fakeStream) ReadMessage() (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) == 0 {
		return 0, nil, f.readErr
	}
	value := f.reads[0]
	f.reads = f.reads[1:]
	messageType := f.messageType
	if messageType == 0 {
		messageType = websocket.TextMessage
	}
	return messageType, value, nil
}
func (f *fakeStream) WriteMessage(_ int, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), value...))
	return nil
}
func (f *fakeStream) Close() error { f.mu.Lock(); defer f.mu.Unlock(); f.closed = true; return nil }

func TestStreamSocketIOProtocolAndJSONL(t *testing.T) {
	a := productTestApp(t)
	a.now = func() time.Time { return time.Unix(123, 456) }
	f := &fakeStream{reads: [][]byte{
		[]byte(`0{"sid":"engine","pingInterval":25000}`),
		[]byte(`40/v1/websocket,{"sid":"socket"}`),
		[]byte(`42/v1/websocket,["subscription_connected","connected"]`),
		[]byte(`2`),
		[]byte(`42/v1/websocket,["subscription_registered","subscriptionId: 1"]`),
		[]byte(`42/v1/websocket,["subscription_event",{"height":9007199254740993,"balance":"9007199254740993"}]`),
	}, readErr: errors.New("closed")}
	var endpoint string
	a.streamDial = func(_ context.Context, raw string, headers http.Header) (streamConnection, *http.Response, error) {
		endpoint = raw
		if len(headers) != 0 {
			t.Fatal("credentials sent as headers")
		}
		return f, nil, nil
	}
	code, out, stderr := run(t, a, "stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS_ACTIVITY", "--condition", `{"addresses":["`+testAddress+`"]}`, "--messages", "1", "-o", "jsonl")
	if code != 0 || stderr != "" || strings.Count(out, "\n") != 3 || !strings.Contains(out, `9007199254740993`) || !strings.Contains(out, `"balance":"9007199254740993"`) {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "wss" || u.Host != "web3."+a.env.Domain || u.Path != "/v1/websocket/" || u.Query().Get("EIO") != "4" || u.Query().Get("transport") != "websocket" || u.Query().Get("protocol") != "ethereum" || u.Query().Get("network") != "mainnet" || strings.Contains(endpoint, "test-product-key") {
		t.Fatal(endpoint)
	}
	if len(f.writes) != 3 || !strings.HasPrefix(string(f.writes[0]), `40/v1/websocket,`) || !strings.Contains(string(f.writes[0]), `"apiKey":"test-product-key"`) || !strings.Contains(string(f.writes[1]), `"subscription"`) || !strings.Contains(string(f.writes[1]), `ADDRESS_ACTIVITY`) || !strings.Contains(string(f.writes[1]), `nodit-cli-123000000456`) || string(f.writes[2]) != "3" {
		t.Fatalf("writes: %q", f.writes)
	}
	if !f.closed {
		t.Fatal("connection not closed")
	}
}

func TestStreamYAMLDocumentsAndRedactedError(t *testing.T) {
	a := productTestApp(t)
	f := &fakeStream{reads: [][]byte{[]byte(`0{}`), []byte(`40/v1/websocket,{}`), []byte(`42/v1/websocket,["subscription_error","bad test-product-key"]`)}, readErr: errors.New("closed")}
	a.streamDial = func(context.Context, string, http.Header) (streamConnection, *http.Response, error) {
		return f, nil, nil
	}
	code, out, stderr := run(t, a, "stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS_ACTIVITY", "-o", "yaml")
	if code != 1 || strings.Count(out, "---\n") != 1 || strings.Contains(out+stderr, "test-product-key") || !strings.Contains(stderr, "STREAM_SUBSCRIPTION_FAILED") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
}

func TestStreamValidationBeforeDial(t *testing.T) {
	a := productTestApp(t)
	dialed := false
	a.streamDial = func(context.Context, string, http.Header) (streamConnection, *http.Response, error) {
		dialed = true
		return nil, nil, errors.New("unexpected")
	}
	for _, args := range [][]string{
		{"stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS_ACTIVITY", "-o", "json"},
		{"stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS ACTIVITY", "-o", "jsonl"},
		{"stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS_ACTIVITY", "--condition", "[]", "-o", "jsonl"},
		{"stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS_ACTIVITY", "--condition", "{} trailing", "-o", "jsonl"},
		{"stream", "watch", "-n", "ethereum-mainnet", "--event-type", "ADDRESS_ACTIVITY", "--messages", "0", "-o", "jsonl"},
		{"stream", "watch", "-n", "solana-devnet", "--event-type", "ADDRESS_ACTIVITY", "-o", "jsonl"},
	} {
		if code, out, _ := run(t, a, args...); code != 2 || out != "" {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	if dialed {
		t.Fatal("invalid stream request dialed")
	}
}
