package sandbox

// A sandbox file whose owner is not `user` is readable but not writable: envd
// opens it as `user` and the kernel refuses, so write_file / edit_file /
// apply_patch all fail with the same provider string. Production, 2026-09-17
// 05:36:47Z (docs/sandbox-file-writes.md):
//
//	apply_patch: write /tmp/board_core.js: e2b upload HTTP 500:
//	  {"code":500,"message":"error opening file: open /tmp/board_core.js: permission denied"}
//
// Nothing in that told the model what to do, and the next call — edit_file, same
// file — came back with the identical bytes. These pin the two halves of the
// fix: the verdict gets a next step, and nothing else gets dressed up as one.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const permissionDeniedBody = `{"code":500,"message":"error opening file: open /tmp/board_core.js: permission denied"}`

// providerMessage pulls `message` out of an envd error body so the assertions
// can say "the provider's own words survived" without hardcoding the wire shape.
func providerMessage(body string) string {
	_, rest, ok := strings.Cut(body, `"message":"`)
	if !ok {
		return body
	}
	msg, _, _ := strings.Cut(rest, `"`)
	return msg
}

func TestWriteFileNamesTheFileAndTheWayOut(t *testing.T) {
	envd := &fakeEnvdTransport{
		brokenSandboxIDs: []string{"sb-1"},
		brokenStatus:     http.StatusInternalServerError,
		brokenBody:       permissionDeniedBody,
	}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}

	_, err := ex.WriteFile(context.Background(), "/tmp/board_core.js", "const x = 1;\n")
	if err == nil {
		t.Fatal("an unwritable path must fail the write")
	}
	// The sentence the model can act on, and the path it applies to: the
	// provider's text named neither the cause nor a way out.
	if !strings.Contains(err.Error(), "这个路径对沙箱用户不可写（属主/权限不匹配）：改写到 /workspace") {
		t.Fatalf("error must carry the next step, got %q", err)
	}
	if !strings.Contains(err.Error(), "/tmp/board_core.js") {
		t.Fatalf("error must name the path it refused, got %q", err)
	}
	// Forensics stay: status + provider message are what an operator greps for
	// when the hint turns out to be the wrong explanation.
	if !strings.Contains(err.Error(), providerMessage(permissionDeniedBody)) {
		t.Fatalf("the provider's message must survive, got %q", err)
	}
	var httpErr *sandboxHTTPError
	if !errors.As(err, &httpErr) || httpErr.status != http.StatusInternalServerError {
		t.Fatalf("error must still be a 500 verdict, got %q", err)
	}
	// And it stays a verdict about the request: a write refusal must not cost
	// the sandbox, or the next call destroys a healthy instance over a file
	// nobody can write (§7.4's classifier is deliberately narrow).
	if sandboxGone(err) || sandboxUnusable(err) {
		t.Fatalf("a write refusal was classified as an instance failure: %v", err)
	}
}

// Hydrate's bundle upload takes the same path (uploadBytesOn), so the hint has
// to reach it too — otherwise a bundle that cannot land reports the raw 500
// while every other writer gets a next step.
func TestUploadBytesGetsTheSameWayOut(t *testing.T) {
	envd := &fakeEnvdTransport{
		brokenSandboxIDs: []string{"sb-1"},
		brokenStatus:     http.StatusInternalServerError,
		brokenBody:       permissionDeniedBody,
	}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}

	err := ex.uploadBytes(context.Background(), "/tmp/fc-hydrate.tar.gz", []byte("tar"))
	if err == nil {
		t.Fatal("an unwritable upload target must fail")
	}
	if !strings.Contains(err.Error(), "改写到 /workspace") {
		t.Fatalf("upload must carry the same hint, got %q", err)
	}
	if !strings.Contains(err.Error(), "/tmp/fc-hydrate.tar.gz") {
		t.Fatalf("upload must name the path it refused, got %q", err)
	}
}

// Only the permission verdict is translated. Everything else keeps the
// provider's words: inventing "the owner is wrong" for a full disk would send
// the model off rewriting paths that are perfectly fine.
func TestWriteFileLeavesOtherProviderFailuresAlone(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"a full disk", http.StatusInternalServerError, `{"code":500,"message":"error opening file: no space left on device"}`},
		{"a missing directory", http.StatusInternalServerError, `{"code":500,"message":"error opening file: no such file or directory"}`},
		{"a stale token", http.StatusUnauthorized, `{"code":401,"message":"access token is invalid"}`},
		{"the instance is gone", http.StatusNotFound, `{"code":404,"message":"sandbox not found"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envd := &fakeEnvdTransport{
				brokenSandboxIDs: []string{"sb-1"},
				brokenStatus:     tc.status,
				brokenBody:       tc.body,
			}
			ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
			ex.client = &http.Client{Transport: envd}

			// writeFileOnce, not WriteFile: a 404 through the wrapper takes the
			// rebuild path, which is a different test's subject.
			_, err := ex.writeFileOnce(context.Background(), "/tmp/board_core.js", "x")
			if err == nil {
				t.Fatal("a failed write must return an error")
			}
			if strings.Contains(err.Error(), "改写到 /workspace") {
				t.Fatalf("%s was reported as a permission problem: %v", tc.name, err)
			}
			if !strings.Contains(err.Error(), providerMessage(tc.body)) {
				t.Fatalf("the provider's message must pass through unchanged, got %q", err)
			}
			var httpErr *sandboxHTTPError
			if !errors.As(err, &httpErr) || httpErr.status != tc.status {
				t.Fatalf("status %d must survive, got %q", tc.status, err)
			}
		})
	}
}

// A refusal is not a reason to retry inside the write: the same open() fails
// for the same identity next time, which is what made the prod call look broken
// (apply_patch, then edit_file, identical error). One attempt per call.
func TestWriteFileDoesNotRetryARefusedWrite(t *testing.T) {
	envd := &countingEnvdTransport{fakeEnvdTransport: fakeEnvdTransport{
		brokenSandboxIDs: []string{"sb-1"},
		brokenStatus:     http.StatusInternalServerError,
		brokenBody:       permissionDeniedBody,
	}}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}

	if _, err := ex.WriteFile(context.Background(), "/tmp/board_core.js", "x"); err == nil {
		t.Fatal("a refused write must fail")
	}
	if got := envd.filePosts(); got != 1 {
		t.Fatalf("upload attempts = %d, want 1: a refusal is not retried with the same identity", got)
	}
}

// countingEnvdTransport counts /files POSTs on top of the shared fake.
type countingEnvdTransport struct {
	fakeEnvdTransport
	files int
}

func (f *countingEnvdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/files") {
		f.files++
	}
	return f.fakeEnvdTransport.RoundTrip(req)
}

func (f *countingEnvdTransport) filePosts() int { return f.files }
