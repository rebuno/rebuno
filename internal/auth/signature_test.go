package auth

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signedRequest() *http.Request {
	r := httptest.NewRequest("POST", "http://kernel/v0/executions/e/steps?q=a%20b", nil)
	r.Header.Set("Rebuno-Agent-Id", "agent")
	r.Header.Set(HeaderTimestamp, "1700000000")
	r.Header.Set("Rebuno-Dispatch-Id", "dispatch")
	r.Header.Set("Rebuno-Dispatch-Attempt", "3")
	r.Header.Set("Rebuno-Signature", SignRequest("secret", r, []byte(`{"args":{}}`)))
	return r
}

func TestRequestSignatureVectors(t *testing.T) {
	vectors := []struct {
		name, secret, method, target, body, timestamp, dispatchID, attempt, signature string
	}{
		{
			name:      "empty read",
			secret:    "test-secret",
			method:    "GET",
			target:    "/v0/executions/alpha",
			timestamp: "1700000000",
			signature: "v1=d0f262ef4226bcdf886ec35a7ce4cf77286055b563078faca8b307caa8ca5839",
		},
		{
			name:       "escaped target and unicode body",
			secret:     "test-secret",
			method:     "POST",
			target:     "/prefix/v0/executions/alpha%2Fbeta/steps?x=a%20b&x=%2F",
			body:       `{"text":"héllo € 🙂\n"}`,
			timestamp:  "1700000000",
			dispatchID: "01920000-0000-7000-8000-000000000001",
			attempt:    "3",
			signature:  "v1=08e2b66ec89c95a677798a9aa048949c38668f770f28a2beede0591e2860820b",
		},
		{
			name:       "heartbeat",
			secret:     "test-secret",
			method:     "POST",
			target:     "/v0/executions/alpha/heartbeat",
			timestamp:  "1700000000",
			dispatchID: "01920000-0000-7000-8000-000000000001",
			attempt:    "3",
			signature:  "v1=63a3e161395bd65728afa785799ddb9e52c01f8e0a61d1d6a4089f582d334b0e",
		},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			r := httptest.NewRequest(v.method, "http://kernel"+v.target, nil)
			r.Header.Set("Rebuno-Agent-Id", "test-agent")
			r.Header.Set(HeaderTimestamp, v.timestamp)
			r.Header.Set("Rebuno-Dispatch-Id", v.dispatchID)
			r.Header.Set("Rebuno-Dispatch-Attempt", v.attempt)
			if got := SignRequest(v.secret, r, []byte(v.body)); got != v.signature {
				t.Fatalf("got %s want %s", got, v.signature)
			}
			r.Header.Set("Rebuno-Signature", v.signature)
			if !VerifyRequest(v.secret, r, []byte(v.body), time.Unix(1700000000, 0)) {
				t.Fatal("valid signature rejected")
			}
		})
	}
}

func TestRequestSignatureBindsAllComponents(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"method":        func(r *http.Request) { r.Method = "GET" },
		"path":          func(r *http.Request) { r.URL.Path = "/v0/executions/other/steps" },
		"query":         func(r *http.Request) { r.URL.RawQuery = "q=a+b" },
		"timestamp":     func(r *http.Request) { r.Header.Set(HeaderTimestamp, "1700000001") },
		"dispatch":      func(r *http.Request) { r.Header.Set("Rebuno-Dispatch-Id", "other") },
		"attempt":       func(r *http.Request) { r.Header.Set("Rebuno-Dispatch-Attempt", "4") },
		"missing lease": func(r *http.Request) { r.Header.Del("Rebuno-Dispatch-Id") },
		"legacy format": func(r *http.Request) {
			r.Header.Set("Rebuno-Signature", "sha256="+strings.TrimPrefix(r.Header.Get("Rebuno-Signature"), "v1="))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := signedRequest()
			mutate(r)
			if VerifyRequest("secret", r, []byte(`{"args":{}}`), time.Unix(1700000000, 0)) {
				t.Fatal("altered request accepted")
			}
		})
	}
	if VerifyRequest("secret", signedRequest(), []byte(`{"args":{"changed":true}}`), time.Unix(1700000000, 0)) {
		t.Fatal("altered body accepted")
	}
	for _, name := range append(signedHeaders[:], "Rebuno-Signature") {
		t.Run("duplicate "+name, func(t *testing.T) {
			r := signedRequest()
			r.Header.Add(name, r.Header.Get(name))
			if VerifyRequest("secret", r, []byte(`{"args":{}}`), time.Unix(1700000000, 0)) {
				t.Fatal("duplicate signed header accepted")
			}
		})
	}
}

func TestRequestSignatureTimestampWindow(t *testing.T) {
	for _, offset := range []int64{-301, -300, 0, 300, 301} {
		t.Run(strconv.FormatInt(offset, 10), func(t *testing.T) {
			r := signedRequest()
			r.Header.Set(HeaderTimestamp, strconv.FormatInt(1700000000+offset, 10))
			r.Header.Set("Rebuno-Signature", SignRequest("secret", r, nil))
			got := VerifyRequest("secret", r, nil, time.Unix(1700000000, 0))
			if got != (offset >= -300 && offset <= 300) {
				t.Fatalf("offset %d accepted=%v", offset, got)
			}
		})
	}
	for _, timestamp := range []string{"", "1700000000.0"} {
		r := signedRequest()
		r.Header.Set(HeaderTimestamp, timestamp)
		r.Header.Set("Rebuno-Signature", SignRequest("secret", r, nil))
		if VerifyRequest("secret", r, nil, time.Unix(1700000000, 0)) {
			t.Fatalf("invalid timestamp accepted: %q", timestamp)
		}
	}
}
