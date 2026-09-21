package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/noditlabs/nodit-cli/internal/buildinfo"
)

const maxAPIResponseBytes = 16 << 20

// Product API requests never use OAuth credentials.
func (a *app) apiPost(ctx context.Context, endpoint, key string, body any) (any, error) {
	result, _, err := a.apiRequest(ctx, http.MethodPost, endpoint, key, body)
	return result, err
}

func (a *app) apiRequest(ctx context.Context, method, endpoint, key string, body any) (any, http.Header, error) {
	return a.jsonRequest(ctx, method, endpoint, "X-API-KEY", key, body)
}

// These causes call for different responses: a refused or unresolved host may work on the next try,
// while a rejected certificate never will until the environment changes. Only errors the standard
// library types unambiguously are named; anything else is left to the caller's general code so a
// wrong cause is never reported. The wording is ours because the underlying text can carry a proxy
// address or an internal host name.
func transportCause(err error, host string) (string, string) {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "TLS_FAILED", "The " + host + " host's certificate was rejected."
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS_FAILED", "Cannot resolve the " + host + " host."
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "CONNECTION_REFUSED", "Cannot reach the " + host + " host."
	}
	return "", ""
}

// Without this the requests go out as the Go default, which does not say which client or version
// made them.
func userAgent() string {
	return "nodit-cli/" + buildinfo.ReportedVersion()
}

func (a *app) jsonRequest(ctx context.Context, method, endpoint, authHeader, credential string, body any) (any, http.Header, error) {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, nil, invalid("Cannot encode the request body.")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, failure("INVALID_ENDPOINT", "Cannot create API request.")
	}
	req.Header.Set(authHeader, credential)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *a.httpClient
	client.Timeout = time.Duration(a.timeoutMS) * time.Millisecond
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		code, message := "API_REQUEST_FAILED", "API request failed."
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			code, message = "TIMEOUT", "API request timed out. Raise the limit with --timeout."
		} else if named, text := transportCause(err, "API"); named != "" {
			code, message = named, text
		}
		return nil, nil, failure(code, message)
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		e := failure("API_RESPONSE_FAILED", "Cannot read the API response.")
		e.HTTPStatus = resp.StatusCode
		return nil, nil, e
	}
	if len(content) > maxAPIResponseBytes {
		e := failure("RESPONSE_TOO_LARGE", "API response exceeds the 16 MiB limit.")
		e.HTTPStatus = resp.StatusCode
		return nil, nil, e
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(content))
	d.UseNumber()
	decodeErr := d.Decode(&value)
	if decodeErr == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			decodeErr = errors.New("trailing JSON content")
		}
	}
	value = redactAPIValue(value, strings.TrimPrefix(credential, "Bearer "))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, apiFailure(resp.StatusCode, value, resp.Header)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		decodeErr = nil
	}
	if decodeErr != nil {
		e := failure("INVALID_API_RESPONSE", "API returned an invalid JSON response.")
		e.HTTPStatus = resp.StatusCode
		return nil, nil, e
	}
	return value, safeResponseHeaders(resp.Header, credential), nil
}

func safeResponseHeaders(headers http.Header, credential string) http.Header {
	result := make(http.Header)
	for name, values := range headers {
		lower := strings.ToLower(name)
		if lower == "content-type" || lower == "retry-after" || lower == "link" || lower == "x-request-id" ||
			strings.HasPrefix(lower, "x-aptos-") || strings.HasPrefix(lower, "x-cosmos-") ||
			strings.HasPrefix(lower, "x-ratelimit-") || lower == "x-cursor" {
			for _, value := range values {
				result.Add(name, redactAPIValue(value, strings.TrimPrefix(credential, "Bearer ")).(string))
			}
		}
	}
	return result
}

// Rate limit responses carry the wait time in headers, so an error built from the
// body alone cannot tell the caller when to try again.
func retryDetails(headers http.Header) any {
	details := map[string]any{}
	for name, field := range map[string]string{
		"Retry-After":           "retryAfter",
		"X-RateLimit-Limit":     "rateLimitLimit",
		"X-RateLimit-Remaining": "rateLimitRemaining",
		"X-RateLimit-Reset":     "rateLimitReset",
	} {
		value := headers.Get(name)
		if value == "" {
			continue
		}
		// Retry-After also allows an HTTP date, which stays a string.
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			details[field] = seconds
		} else {
			details[field] = value
		}
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func apiFailure(status int, value any, headers http.Header) error {
	e := failure("API_ERROR", "API rejected the request.")
	e.HTTPStatus = status
	// The server answers a rejected credential with its own wording, which says nothing about which
	// credential this command used or how to change it. The hint is appended after the body is read
	// so it survives the server message, and it names the same routes the local checks name.
	hint := ""
	switch status {
	case 401:
		e.Code = "AUTHENTICATION_FAILED"
		hint = "See which credential this used with nodit auth status. Set a working one with " +
			"NODIT_API_KEY, or run nodit auth login and nodit project select <project-id> to link one."
	case 403:
		e.Code = "PERMISSION_DENIED"
		hint = "See which credential this used with nodit auth status."
	case 429:
		// The gateway sends the rate limit headers on every response, so they are
		// only worth reporting on the status where the caller has to act on them.
		e.Code = "RATE_LIMITED"
		e.Details = retryDetails(headers)
	}
	if problem, ok := value.(map[string]any); ok {
		if nested, ok := problem["error"].(map[string]any); ok {
			problem = nested
		}
		switch code := problem["code"].(type) {
		case string:
			e.APICode = code
		case json.Number:
			e.APICode = code
		}
		if message, ok := problem["message"].(string); ok && message != "" {
			e.Message = message
		}
		// Already filled from the rate limit headers on 429, which is the more specific source there.
		if details, ok := problem["details"].(map[string]any); ok && e.Details == nil && len(details) > 0 {
			e.Details = details
		}
	}
	if hint != "" {
		e.Message = strings.TrimSuffix(e.Message, " ") + " " + hint
	}
	return e
}

func redactAPIValue(v any, key string) any {
	switch x := v.(type) {
	case string:
		if key != "" {
			return strings.ReplaceAll(x, key, "[REDACTED]")
		}
		return x
	case []any:
		for i, item := range x {
			x[i] = redactAPIValue(item, key)
		}
	case map[string]any:
		for name, item := range x {
			normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(name))
			switch normalized {
			case "apikey", "xapikey", "authorization", "accesstoken", "refreshtoken", "clientsecret":
				x[name] = "[REDACTED]"
			default:
				x[name] = redactAPIValue(item, key)
			}
		}
	}
	return v
}
