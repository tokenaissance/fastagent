//go:build manual

// Credential-gated probe for the two questions the API schemas left open.
//
// Run with:
//
//	E2B_API_KEY=... E2B_TEMPLATE=... \
//	  go test ./internal/sandbox/ -run TestE2BEnvdTokenAcrossPauseResume -v -count=1 -tags=manual -timeout 5m
//
// What the docs already say (api-reference/sandboxes/create-sandbox.md,
// .../connect-to-sandbox.md, .../resume-sandbox.md): `envdAccessToken` is part
// of the `Sandbox` schema returned by create, connect AND resume, but it is
// "only returned when the sandbox is created with server-side `secure: true`.
// Null for non-secure sandboxes (envd endpoints work without auth)". Both
// endpoints also carry `network.allowPublicTraffic`, default `true`.
//
// What the schemas do NOT say, and this probe answers:
//  1. Does the token from CREATE still authenticate envd after a pause+resume,
//     or must the row be refreshed from the connect/resume response?
//  2. Concretely: is our current (non-secure, public) configuration really
//     token-less, so that a lease row's `envd_token` is empty by construction?

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func e2bProbeCredentials(t *testing.T) (apiKey, template string) {
	t.Helper()
	apiKey, template = os.Getenv("E2B_API_KEY"), os.Getenv("E2B_TEMPLATE")
	if apiKey == "" || template == "" {
		t.Skip("set E2B_API_KEY and E2B_TEMPLATE to run the envd-token probe")
	}
	return apiKey, template
}

// createProbeSandbox posts a create request with the given `secure` flag and
// returns the sandbox id plus whatever token the response carried.
func createProbeSandbox(t *testing.T, apiKey, template string, secure bool) (id, token string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"templateID": template,
		"timeout":    300,
		"secure":     secure,
	})
	req, err := http.NewRequest("POST", e2bBaseURL+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create sandbox: HTTP %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		SandboxID       string `json:"sandboxID"`
		EnvdAccessToken string `json:"envdAccessToken"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse create response: %v", err)
	}
	return out.SandboxID, out.EnvdAccessToken
}

// destroyProbeSandbox removes an instance this probe created and then proves
// it is gone. The probe used to leave both of its sandboxes behind — one of
// them paused on purpose — and a leftover shows up nowhere except the
// provider's sandbox list, which is to say never, for anyone reading the test
// output.
func destroyProbeSandbox(t *testing.T, apiKey, sandboxID string) {
	t.Helper()
	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/sandboxes/%s", e2bBaseURL, sandboxID), nil)
	if err != nil {
		t.Errorf("build destroy request for %s: %v", sandboxID, err)
		return
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Errorf("destroy sandbox %s: %v", sandboxID, err)
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// 404 is success here, for the same reason it is in closeSandboxByID: the
	// instance is not running, which is the entire goal.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		t.Errorf("destroy sandbox %s: HTTP %d: %s", sandboxID, resp.StatusCode, raw)
		return
	}
	// Prove it rather than assume it. Reporting success on a rejected or
	// ignored DELETE is exactly how leftovers accumulate.
	getReq, err := http.NewRequest("GET", fmt.Sprintf("%s/sandboxes/%s", e2bBaseURL, sandboxID), nil)
	if err != nil {
		t.Errorf("build verify request for %s: %v", sandboxID, err)
		return
	}
	getReq.Header.Set("X-API-Key", apiKey)
	getResp, err := (&http.Client{Timeout: 60 * time.Second}).Do(getReq)
	if err != nil {
		t.Errorf("verify sandbox %s is gone: %v", sandboxID, err)
		return
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(getResp.Body)
		t.Errorf("sandbox %s survived its own destroy: HTTP %d: %s", sandboxID, getResp.StatusCode, body)
	}
}

// trackProbeSandbox makes an instance disappear when the (sub)test that
// created it ends, t.Fatal and t.Skipf included.
func trackProbeSandbox(t *testing.T, apiKey, sandboxID string) {
	t.Helper()
	t.Cleanup(func() { destroyProbeSandbox(t, apiKey, sandboxID) })
}

// connectProbeSandbox resumes a paused sandbox (a no-op when it is running) and
// returns the token from the response — the one a resumed lease row would need.
func connectProbeSandbox(t *testing.T, apiKey, sandboxID string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"timeout": 300})
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/sandboxes/%s/connect", e2bBaseURL, sandboxID), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build connect request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("connect sandbox: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("connect sandbox: HTTP %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		EnvdAccessToken string `json:"envdAccessToken"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse connect response: %v", err)
	}
	return out.EnvdAccessToken
}

// execWith reports whether a command runs with the given token, so the caller
// can compare "the token from create" against "the token from connect".
func execWith(ctx context.Context, apiKey, id, token, template string) error {
	ex := newAdoptedE2BExecutor(apiKey, id, token, template, 5*time.Minute)
	_, err := ex.execOnce(ctx, "echo probe-ok", 30*time.Second)
	return err
}

func TestE2BEnvdTokenAcrossPauseResume(t *testing.T) {
	apiKey, template := e2bProbeCredentials(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Run("non-secure: no token at all", func(t *testing.T) {
		// This is our production configuration: no `secure` field, so the
		// schema says envdAccessToken is null and envd needs no auth. If that
		// holds, a lease row's envd_token is empty by construction and the
		// pause/resume question is moot for it.
		id, token := createProbeSandbox(t, apiKey, template, false)
		trackProbeSandbox(t, apiKey, id)
		t.Logf("created non-secure sandbox %s (token %q)", id, token)
		if token != "" {
			t.Errorf("non-secure sandbox returned a token: %q — the schema says null", token)
		}
		if err := execWith(ctx, apiKey, id, "", template); err != nil {
			t.Errorf("envd without a token failed on a non-secure sandbox: %v", err)
		}
	})

	t.Run("secure: does the create token survive a pause+resume?", func(t *testing.T) {
		id, created := createProbeSandbox(t, apiKey, template, true)
		trackProbeSandbox(t, apiKey, id)
		if created == "" {
			t.Fatal("secure sandbox returned no envdAccessToken — the schema says it must")
		}
		if err := execWith(ctx, apiKey, id, created, template); err != nil {
			t.Fatalf("envd with the create token failed before pausing: %v", err)
		}

		ex := newAdoptedE2BExecutor(apiKey, id, created, template, 5*time.Minute)
		if err := ex.Pause(ctx, id); err != nil {
			t.Fatalf("pause: %v", err)
		}
		resumed := connectProbeSandbox(t, apiKey, id)
		t.Logf("pause+resume: token changed = %v", resumed != created)

		// The answer this probe exists for. Either outcome is usable — it
		// decides whether adopting a resumed sandbox must refresh the row.
		oldErr := execWith(ctx, apiKey, id, created, template)
		newErr := execWith(ctx, apiKey, id, resumed, template)
		t.Logf("envd with the CREATE token after resume: %v", oldErr)
		t.Logf("envd with the CONNECT token after resume: %v", newErr)
		if oldErr != nil && newErr != nil {
			t.Errorf("neither token works after resume: create=%v connect=%v", oldErr, newErr)
		}
	})
}
