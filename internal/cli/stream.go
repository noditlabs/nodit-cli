package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

const streamNamespace = "/v1/websocket"

type streamConnection interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(int, []byte) error
	Close() error
}

type streamDialFunc func(context.Context, string, http.Header) (streamConnection, *http.Response, error)

func defaultStreamDial(ctx context.Context, endpoint string, headers http.Header) (streamConnection, *http.Response, error) {
	return websocket.DefaultDialer.DialContext(ctx, endpoint, headers)
}

func (a *app) streamCommand() *cobra.Command {
	root := asGroup(&cobra.Command{Use: "stream", Short: "Watch Nodit Stream subscriptions"})
	var flags productFlags
	var eventType, condition string
	var messages int
	cmd := &cobra.Command{
		Use: "watch", Short: "Watch one Nodit Stream subscription over Socket.IO", Args: cobra.NoArgs,
		Long:    "Connect to Nodit Stream, register one Classic event type, and print control and delivery events.\nOnly YAML and JSONL can represent the unbounded output. --messages limits delivered\nsubscription_event records; omitted means watch until interrupted. The HTTP timeout applies\nto connection establishment only. The CLI does not reconnect or replay missed events.",
		Example: "  nodit stream watch --network ethereum-mainnet --event-type ADDRESS_ACTIVITY --condition '{\"addresses\":[\"0x000000000000000000000000000000000000dEaD\"]}' --output jsonl",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.format != "yaml" && a.format != "jsonl" {
				return invalid("Stream output must be yaml or jsonl.")
			}
			if strings.TrimSpace(eventType) == "" || strings.IndexFunc(eventType, func(r rune) bool { return r <= ' ' || r >= 127 }) >= 0 {
				return invalid("--event-type is required and cannot contain whitespace.")
			}
			if cmd.Flags().Changed("messages") && messages <= 0 {
				return invalid("--messages must be positive when specified.")
			}
			var conditionValue map[string]any
			d := json.NewDecoder(strings.NewReader(condition))
			d.UseNumber()
			if d.Decode(&conditionValue) != nil || conditionValue == nil {
				return invalid("--condition must be a JSON object.")
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				return invalid("--condition must contain exactly one JSON object.")
			}
			n, err := a.productNetwork(flags.network, "stream")
			if err != nil {
				return err
			}
			key, err := a.apiKey(flags.apiKey)
			if err != nil {
				return err
			}
			return a.watchStream(cmd.Context(), n, key, eventType, conditionValue, messages)
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&eventType, "event-type", "", "Classic event type, such as ADDRESS_ACTIVITY")
	cmd.Flags().StringVar(&condition, "condition", "{}", "Event condition as a JSON object")
	cmd.Flags().IntVar(&messages, "messages", 0, "Stop after this many subscription_event deliveries")
	_ = cmd.MarkFlagRequired("event-type")
	root.AddCommand(cmd)
	return root
}

func (a *app) watchStream(ctx context.Context, n network, key, eventType string, condition map[string]any, messages int) error {
	u := url.URL{Scheme: "wss", Host: "web3." + a.env.Domain, Path: "/v1/websocket/"}
	q := u.Query()
	q.Set("EIO", "4")
	q.Set("transport", "websocket")
	q.Set("protocol", n.Chain)
	q.Set("network", n.Network)
	u.RawQuery = q.Encode()
	dial := a.streamDial
	if dial == nil {
		dial = defaultStreamDial
	}
	connectContext, cancel := context.WithTimeout(ctx, time.Duration(a.timeoutMS)*time.Millisecond)
	conn, response, err := dial(connectContext, u.String(), http.Header{"User-Agent": {userAgent()}})
	cancel()
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The connect deadline is this command's own, so it never reaches ctx above. The dialer
		// reports a timeout during the TCP connect as a net.Error rather than the context error,
		// so both phases are covered only by asking the error itself.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return failure("TIMEOUT", "Connecting to Nodit Stream timed out. Raise the limit with --timeout.")
		}
		if code, message := transportCause(err, "Stream"); code != "" {
			return failure(code, message)
		}
		return failure("STREAM_CONNECT_FAILED", "Cannot connect to Nodit Stream.")
	}
	defer conn.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	connected, subscribed, delivered := false, false, 0
	for {
		messageType, content, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return failure("STREAM_CLOSED", "Nodit Stream closed unexpectedly.")
		}
		if messageType != websocket.TextMessage {
			continue
		}
		for _, packet := range strings.Split(string(content), "\x1e") {
			switch {
			case packet == "2":
				if err := conn.WriteMessage(websocket.TextMessage, []byte("3")); err != nil {
					return streamWriteError()
				}
			case strings.HasPrefix(packet, "0"):
				connect, _ := json.Marshal(map[string]string{"apiKey": key})
				if err := conn.WriteMessage(websocket.TextMessage, []byte("40"+streamNamespace+","+string(connect))); err != nil {
					return streamWriteError()
				}
			case strings.HasPrefix(packet, "40"+streamNamespace):
				connected = true
				if !subscribed {
					params, _ := json.Marshal(map[string]any{"description": "nodit CLI", "condition": condition})
					payload, _ := json.Marshal([]any{"subscription", fmt.Sprintf("nodit-cli-%d", a.now().UnixNano()), eventType, string(params)})
					if err := conn.WriteMessage(websocket.TextMessage, []byte("42"+streamNamespace+","+string(payload))); err != nil {
						return streamWriteError()
					}
					subscribed = true
				}
			case strings.HasPrefix(packet, "44"+streamNamespace):
				e := failure("STREAM_CONNECT_FAILED", "Nodit Stream rejected the Socket.IO connection.")
				// The server states the reason in the packet, the same way it does for a
				// rejected subscription. Without it the error says only that something failed.
				if reason := streamRejectionReason(strings.TrimPrefix(packet, "44"+streamNamespace), key); reason != nil {
					e.Details = reason
				}
				return e
			case strings.HasPrefix(packet, "42"+streamNamespace+","):
				if !connected {
					return failure("INVALID_STREAM_RESPONSE", "Stream emitted an event before connecting.")
				}
				name, payload, err := parseStreamEvent(strings.TrimPrefix(packet, "42"+streamNamespace+","), key)
				if err != nil {
					return err
				}
				if err = a.writeStreamRecord(map[string]any{"event": name, "payload": payload}); err != nil {
					return err
				}
				if name == "subscription_error" {
					e := failure("STREAM_SUBSCRIPTION_FAILED", "Nodit Stream rejected the subscription.")
					// The server states the reason in the payload; without it the error says
					// only that something was rejected.
					e.Details = payload
					return e
				}
				if name == "subscription_event" {
					delivered++
					if messages > 0 && delivered >= messages {
						return nil
					}
				}
			}
		}
	}
}

// A connect error packet carries the reason after the namespace, or nothing at all.
func streamRejectionReason(rest, key string) any {
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ","))
	if rest == "" {
		return nil
	}
	var value any
	d := json.NewDecoder(strings.NewReader(rest))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil
	}
	return redactAPIValue(value, key)
}

func parseStreamEvent(raw, key string) (string, []any, error) {
	var values []any
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if d.Decode(&values) != nil || len(values) == 0 {
		return "", nil, failure("INVALID_STREAM_RESPONSE", "Nodit Stream returned an invalid event.")
	}
	name, ok := values[0].(string)
	if !ok || name == "" {
		return "", nil, failure("INVALID_STREAM_RESPONSE", "Nodit Stream returned an unnamed event.")
	}
	payload, _ := redactAPIValue(values[1:], key).([]any)
	return name, payload, nil
}

func (a *app) writeStreamRecord(value any) error {
	if a.format == "yaml" {
		if _, err := fmt.Fprintln(a.stdout, "---"); err != nil {
			return failure("OUTPUT_FAILED", "Cannot write command output.")
		}
	}
	return a.success(value)
}

func streamWriteError() error {
	return failure("STREAM_WRITE_FAILED", "Cannot send the Stream subscription.")
}
