package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const HeaderTimestamp = "Rebuno-Timestamp"
const SignatureWindow = 5 * time.Minute

var signedHeaders = [...]string{HeaderTimestamp, "Rebuno-Dispatch-Id", "Rebuno-Dispatch-Attempt"}

// SignRequest binds the exact request target, delivery attempt and body to the
// agent's secret. Webhook signatures use a separate, body-only format.
func SignRequest(secret string, r *http.Request, body []byte) string {
	fields := []string{"rebuno-request-v1", r.Method, r.URL.RequestURI()}
	for _, name := range signedHeaders {
		fields = append(fields, r.Header.Get(name))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join(fields, "\n") + "\n"))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func VerifyRequest(secret string, r *http.Request, body []byte, now time.Time) bool {
	for _, name := range signedHeaders {
		if len(r.Header.Values(name)) > 1 || strings.ContainsAny(r.Header.Get(name), "\r\n") {
			return false
		}
	}
	if len(r.Header.Values("Rebuno-Signature")) != 1 {
		return false
	}
	seconds, err := strconv.ParseInt(r.Header.Get(HeaderTimestamp), 10, 64)
	if err != nil {
		return false
	}
	if seconds < now.Unix()-int64(SignatureWindow/time.Second) || seconds > now.Unix()+int64(SignatureWindow/time.Second) {
		return false
	}
	return hmac.Equal([]byte(SignRequest(secret, r, body)), []byte(r.Header.Get("Rebuno-Signature")))
}
