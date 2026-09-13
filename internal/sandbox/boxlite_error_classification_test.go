package sandbox

// boxlite's "the box is gone" decision used to be a string match over three
// codes, so any error quoting one of them triggered a rebuild. These cases pin
// the classifier to the status the provider actually sent, driven through a
// real HTTP response rather than a hand-built error.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newBoxliteAgainst stands up a boxlite executor pointing at a stub server and
// returns the error its exec start call produces for the given response.
func boxliteExecStartError(t *testing.T, status int, body string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	ex := &BoxliteExecutor{
		baseURL: srv.URL,
		prefix:  "default",
		apiKey:  "test-key",
		image:   "img",
		timeout: time.Minute,
		client:  srv.Client(),
		boxID:   "box-1",
	}
	_, err := ex.execOnce(context.Background(), "echo hi", time.Second)
	if err == nil {
		t.Fatal("execOnce against a failing stub must return an error")
	}
	return err
}

func TestIsBoxliteGoneReadsTheStatusNotTheText(t *testing.T) {
	t.Run("gone statuses", func(t *testing.T) {
		for _, status := range []int{http.StatusNotFound, http.StatusBadGateway, http.StatusGone} {
			err := boxliteExecStartError(t, status, `{"message":"box not found"}`)
			if !isBoxliteGone(err) {
				t.Fatalf("HTTP %d must classify as gone, got %v", status, err)
			}
		}
	})

	t.Run("a 500 quoting the codes is not gone", func(t *testing.T) {
		err := boxliteExecStartError(t, http.StatusInternalServerError,
			`{"message":"upstream said HTTP 404 and HTTP 410"}`)
		if isBoxliteGone(err) {
			t.Fatalf("a 500 that merely mentions the codes must not cost a rebuild: %v", err)
		}
	})

	t.Run("no provider status means no verdict", func(t *testing.T) {
		if isBoxliteGone(nil) {
			t.Fatal("nil must not classify as gone")
		}
		if isBoxliteGone(context.DeadlineExceeded) {
			t.Fatal("a transport error must not classify as gone")
		}
	})
}
