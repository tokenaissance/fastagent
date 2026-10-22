package setup

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestConfigCommitterOrdersPersistInvalidateReloads(t *testing.T) {
	var calls []string
	c := ConfigCommitter{
		scope: func(*http.Request) (string, string) {
			return "system", ""
		},
		invalidate: func(sc, _ string) {
			calls = append(calls, "invalidate:"+sc)
		},
		reloadSandbox: func() error {
			calls = append(calls, "sandbox")
			return nil
		},
		reloadTaskQueue: func() error {
			calls = append(calls, "queue")
			return nil
		},
	}

	err := c.Commit(httptest.NewRequest(http.MethodPost, "/api/config", nil), func() error {
		calls = append(calls, "persist")
		return nil
	}, CommitOptions{ReloadSandbox: true, ReloadTaskQueue: true})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	want := []string{"persist", "invalidate:system", "sandbox", "queue"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestConfigCommitterSkipsInvalidateWhenPersistFails(t *testing.T) {
	invalidated := false
	c := ConfigCommitter{
		scope:      func(*http.Request) (string, string) { return "system", "" },
		invalidate: func(string, string) { invalidated = true },
	}

	persistErr := errors.New("store down")
	err := c.Commit(httptest.NewRequest(http.MethodPost, "/api/config", nil), func() error {
		return persistErr
	}, CommitOptions{ReloadSandbox: true})
	if !errors.Is(err, persistErr) {
		t.Fatalf("Commit error = %v, want %v", err, persistErr)
	}
	if invalidated {
		t.Fatal("a failed persist must not invalidate the cache")
	}
}

func TestConfigCommitterInvalidatesBeforeReloadFailure(t *testing.T) {
	var calls []string
	reloadErr := errors.New("sandbox reload failed")
	c := ConfigCommitter{
		scope: func(*http.Request) (string, string) { return "system", "" },
		invalidate: func(string, string) {
			calls = append(calls, "invalidate")
		},
		reloadSandbox: func() error {
			calls = append(calls, "sandbox")
			return reloadErr
		},
	}

	err := c.Commit(httptest.NewRequest(http.MethodPost, "/api/config", nil), nil, CommitOptions{ReloadSandbox: true})
	if !errors.Is(err, reloadErr) {
		t.Fatalf("Commit error = %v, want %v", err, reloadErr)
	}
	if want := []string{"invalidate", "sandbox"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v (invalidate must run before the failing reload)", calls, want)
	}
}

func TestConfigCommitterSkipsUnrequestedReloads(t *testing.T) {
	c := ConfigCommitter{
		scope:      func(*http.Request) (string, string) { return "user", "u1" },
		invalidate: func(string, string) {},
		reloadSandbox: func() error {
			t.Fatal("sandbox reload must not run")
			return nil
		},
		reloadTaskQueue: func() error {
			t.Fatal("taskqueue reload must not run")
			return nil
		},
	}

	if err := c.Commit(httptest.NewRequest(http.MethodPost, "/api/config", nil), nil, CommitOptions{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}
