package mcpclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
)

// unauthorized means "the last POST was answered 401": any other answer, or
// none, clears it, so a resend that got 503 is not reported as auth_required.
func TestUnauthorizedTracksLastAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second int // 0: the second round trip fails without an answer
		want   string
	}{
		{"503", http.StatusServiceUnavailable, ""},
		{"502", http.StatusBadGateway, ""},
		{"404", http.StatusNotFound, ""},
		{"403", http.StatusForbidden, ""},
		{"200", http.StatusOK, ""},
		{"no answer", 0, ""},
		{"401", http.StatusUnauthorized, "auth_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statuses := []int{http.StatusUnauthorized, tc.second}
			var hs *httptest.Server
			hs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := statuses[0]
				statuses = statuses[1:]
				if status == 0 {
					hs.CloseClientConnections()
					return
				}
				w.WriteHeader(status)
			}))
			defer hs.Close()
			origin, _ := url.Parse(hs.URL)
			rt := &headerTransport{base: &http.Transport{}, origin: origin, oauth: true}
			post := func() {
				req, _ := http.NewRequest(http.MethodPost, hs.URL, strings.NewReader(`{}`))
				if resp, err := rt.RoundTrip(req); err == nil {
					_ = resp.Body.Close()
				}
			}
			post()
			if failure := rt.status(); failure == nil || failure.Error() == "" {
				t.Fatal("401 not recorded")
			}
			post()
			got := ""
			var e *output.Error
			if failure := rt.status(); errors.As(failure, &e) && e != nil {
				got = e.Code
			}
			if got != tc.want {
				t.Fatalf("status after %s: %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
