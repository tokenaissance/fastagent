package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// S3 is the production backend for every multi-replica install, and B3 — "the
// expectation rides the request as If-Match / If-None-Match, so the check and
// the write share one round trip" — is the only reason the refusal is exact
// rather than best-effort. It had no witness at all: the port's test covers
// LocalFS, and everything above it (the three file tools) was verified against
// LocalFS too.
//
// This is the smallest S3 the minio client will talk to — HEAD answers with the
// object's ETag, PUT honours the two conditional headers the way S3 does (412 +
// PreconditionFailed) — so the witness is about the REQUEST the client builds
// and the mapping of the answer, not about an emulator's behaviour.
//
// Falsification (run for real): drop the `opts.SetMatchETag(...)` branch from
// S3.PutIfVersion and the stale-expectation case below stops being refused —
// the write lands and the test fails on "the stale writer's bytes must not
// land", which is exactly the 09-18 overwrite.

type fakeS3Object struct {
	etag string
	body []byte
}

type fakeS3 struct {
	mu      sync.Mutex
	bucket  string
	objects map[string]fakeS3Object
	// Distinctive, sequential ETags: the point of the witness is that the string
	// the client reads back from Stat IS the object's ETag, and that it is the
	// same string it then sends as the precondition.
	writes int
	// Every conditional PUT's headers, so a test can read what the client sent
	// rather than infer it from a status code.
	putHeaders []http.Header
	// The byte count of every PUT body, so a retry that "succeeds" with an empty
	// payload cannot pass as a landed write.
	putBodies []int
	// ifMatchUnsupported reproduces the store we actually run on: Ceph RGW
	// (DigitalOcean Spaces) implements the create-only form and nothing else —
	// every If-Match answers 412, even when the object still carries exactly the
	// version the client just read. Measured 2026-09-22 against nyc3 Spaces.
	ifMatchUnsupported bool
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
	obj, exists := f.objects[key]

	switch r.Method {
	case http.MethodGet:
		// minio-go asks for the bucket's region before its first write
		// (`GET /<bucket>/?location=`); S3 answers with a location constraint.
		if _, ok := r.URL.Query()["location"]; ok {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		if !exists {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", `"`+obj.etag+`"`)
		w.Write(obj.body)
	case http.MethodHead:
		if !exists {
			s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", `"`+obj.etag+`"`)
		w.Header().Set("Content-Length", fmt.Sprint(len(obj.body)))
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			s3Error(w, http.StatusBadRequest, "InvalidRequest")
			return
		}
		// The client signs a stream, so the payload arrives in aws-chunked
		// framing and Go's server hands it over exactly as sent. Real S3 decodes
		// it; the fake has to as well, or "the bytes landed" is not assertable.
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
			decoded, derr := decodeAWSChunked(body)
			if derr != nil {
				s3Error(w, http.StatusBadRequest, "InvalidChunk")
				return
			}
			body = decoded
		}
		f.putHeaders = append(f.putHeaders, r.Header.Clone())
		f.putBodies = append(f.putBodies, len(body))
		// S3 semantics: If-None-Match: * means "must not exist", If-Match means
		// "must still be this version". Both answer 412 on failure.
		if r.Header.Get("If-None-Match") == "*" && exists {
			s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if f.ifMatchUnsupported && r.Header.Get("If-Match") != "" {
			s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if want := strings.Trim(r.Header.Get("If-Match"), `"`); want != "" {
			if !exists || obj.etag != want {
				s3Error(w, http.StatusPreconditionFailed, "PreconditionFailed")
				return
			}
		}
		f.writes++
		etag := fmt.Sprintf("etag-%d", f.writes)
		f.objects[key] = fakeS3Object{etag: etag, body: body}
		w.Header().Set("ETag", `"`+etag+`"`)
		w.WriteHeader(http.StatusOK)
	default:
		s3Error(w, http.StatusMethodNotAllowed, r.Method+" "+r.URL.String())
	}
}

// decodeAWSChunked unwraps SigV4 streaming framing:
//
//	<hex size>;chunk-signature=<sig>\r\n<bytes>\r\n … 0;chunk-signature=…\r\n\r\n
func decodeAWSChunked(b []byte) ([]byte, error) {
	var out []byte
	for {
		i := bytes.Index(b, []byte("\r\n"))
		if i < 0 {
			return nil, fmt.Errorf("chunk header: no CRLF")
		}
		head := string(b[:i])
		b = b[i+2:]
		sizeField, _, _ := strings.Cut(head, ";")
		n, err := strconv.ParseInt(strings.TrimSpace(sizeField), 16, 64)
		if err != nil {
			return nil, fmt.Errorf("chunk size %q: %w", sizeField, err)
		}
		if n == 0 {
			return out, nil
		}
		if int64(len(b)) < n {
			return nil, fmt.Errorf("chunk of %d bytes, %d left", n, len(b))
		}
		out = append(out, b[:n]...)
		b = b[n:]
		if len(b) >= 2 {
			b = b[2:] // the CRLF that closes the chunk
		}
	}
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

func newFakeS3Store(t *testing.T) (*S3, *fakeS3) {
	t.Helper()
	fake := &fakeS3{bucket: "workspace", objects: map[string]fakeS3Object{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	store, err := NewS3(S3Config{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		Bucket:    "workspace",
		AccessKey: "key",
		SecretKey: "secret",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return store, fake
}

func TestS3VersionIsTheETagAndTheConditionRidesTheRequest(t *testing.T) {
	ctx := context.Background()
	store, fake := newFakeS3Store(t)

	if err := store.Put(ctx, "agt", "", "", "notes.md", strings.NewReader("v1"), 2, "text/markdown"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info, err := store.Stat(ctx, "agt", "", "", "notes.md")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The version IS the ETag — that is what makes the precondition exact
	// where LocalFS can only be best-effort.
	if string(info.Version) != "etag-1" {
		t.Fatalf("Version = %q; want the object's ETag", info.Version)
	}

	// The holder of the live version may write…
	if err := store.PutIfVersion(ctx, "agt", "", "", "notes.md", strings.NewReader("v2"), 2, "text/markdown", info.Version); err != nil {
		t.Fatalf("write with the live ETag: %v", err)
	}
	last := fake.putHeaders[len(fake.putHeaders)-1]
	if got := strings.Trim(last.Get("If-Match"), `"`); got != string(info.Version) {
		t.Fatalf("the conditional PUT carried If-Match %q; want %q", got, info.Version)
	}

	// …and the version it just superseded may not: 412 becomes ErrVersionConflict,
	// and the bytes provably do not land.
	err = store.PutIfVersion(ctx, "agt", "", "", "notes.md", strings.NewReader("v3"), 2, "text/markdown", info.Version)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("write with a stale ETag = %v; want ErrVersionConflict", err)
	}
	// The refused write left the object exactly as the successful one did: same
	// ETag. (The object's identity is compared, not its bytes: what the client
	// frames on the wire is not this test's business, and asserting on it would
	// make the witness depend on the SDK's encoding.)
	if got := fake.objects["agt/notes.md"].etag; got != "etag-2" {
		t.Fatalf("the object's ETag after the refused write = %q; want etag-2 (the stale write must not land)", got)
	}
}

// The store we actually run on answers 412 to every If-Match (see
// fakeS3.ifMatchUnsupported). On that backend a refused precondition is not a
// competing writer — the object still carries exactly the version we sent — so
// an overwrite has to land anyway. Without this, every edit of an existing file
// fails while every brand-new file succeeds, which is exactly what the 09-22 dev
// session looked like (todo.md created ✓, the 25 KB document created ✓, then
// edit_file / apply_patch refused three times in a row as "another writer
// changed it").
//
// Falsification: drop the fallback from S3.PutIfVersion and the first
// PutIfVersion below fails with ErrVersionConflict.
func TestS3OverwriteLandsOnABackendWithoutIfMatch(t *testing.T) {
	ctx := context.Background()
	store, fake := newFakeS3Store(t)
	fake.ifMatchUnsupported = true

	if err := store.Put(ctx, "agt", "", "", "notes.md", strings.NewReader("v1"), 2, "text/markdown"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	info, err := store.Stat(ctx, "agt", "", "", "notes.md")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := store.PutIfVersion(ctx, "agt", "", "", "notes.md", strings.NewReader("v2"), 2, "text/markdown", info.Version); err != nil {
		t.Fatalf("overwrite with the live version = %v; want the write to land", err)
	}
	// The landing write is the unconditional one: retrying the conditional form
	// would only buy another 412.
	if got := fake.putHeaders[len(fake.putHeaders)-1].Get("If-Match"); got != "" {
		t.Fatalf("the write that landed carried If-Match %q; want an unconditional write", got)
	}
	// The payload really landed (decoded: the client signs a stream, so what the
	// bucket stores is only visible after unwrapping the framing), and the retry
	// carried it — an "unconditional write of nothing" would pass a looser test.
	if got := fake.objects["agt/notes.md"]; string(got.body) != "v2" {
		t.Fatalf("stored body = %q; want v2 (the overwrite must reach the store)", got.body)
	}
	if got := fake.putBodies[len(fake.putBodies)-1]; got != 2 {
		t.Fatalf("the landing PUT carried %d bytes; want 2", got)
	}

	// Create-only is still exact on this backend — that form works there, and
	// nothing in this fix may weaken it.
	if err := store.PutIfVersion(ctx, "agt", "", "", "fresh.md", strings.NewReader("v1"), 2, "text/markdown", VersionAbsent); err != nil {
		t.Fatalf("create-only on a free key: %v", err)
	}
	err = store.PutIfVersion(ctx, "agt", "", "", "fresh.md", strings.NewReader("v2"), 2, "text/markdown", VersionAbsent)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("create-only on a taken key = %v; want ErrVersionConflict", err)
	}

	// And a genuinely superseded expectation is still refused: the fallback
	// re-checks the object, so "someone else got there first" keeps its answer.
	live, err := store.Stat(ctx, "agt", "", "", "notes.md")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := store.Put(ctx, "agt", "", "", "notes.md", strings.NewReader("v3"), 2, "text/markdown"); err != nil {
		t.Fatalf("the other writer: %v", err)
	}
	err = store.PutIfVersion(ctx, "agt", "", "", "notes.md", strings.NewReader("v4"), 2, "text/markdown", live.Version)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("write with a superseded version = %v; want ErrVersionConflict", err)
	}
	got := fake.objects["agt/notes.md"]
	if string(got.body) != "v3" {
		t.Fatalf("object body after the refused write = %q; want v3 (the stale write must not land)", got.body)
	}
}

// The degradation notice is not an incident report. The store this deployment
// runs on answers 412 to every If-Match as a property of the store, so an
// operator reading that line has to be told which store it is normal on — the
// alternative is somebody chasing a bucket that is behaving as designed.
//
// Falsification: drop the "normal on DigitalOcean Spaces … not an incident"
// attribute from the WARN in S3.PutIfVersion and the first assertion fails. The
// line count is pinned in the same breath: the notice belongs to the bucket, so
// the second degraded write must not add another.
func TestS3NoIfMatchWarningSaysItIsNormalOnSpaces(t *testing.T) {
	logs := captureWorkspaceWarnings(t)
	ctx := context.Background()
	store, fake := newFakeS3Store(t)
	fake.ifMatchUnsupported = true

	// Two degraded overwrites: one to produce the notice, one to prove it is
	// not repeated.
	for i, seed := range []string{"v1", "v2"} {
		if err := store.Put(ctx, "agt", "", "", "notes.md", strings.NewReader(seed), 2, "text/markdown"); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		info, err := store.Stat(ctx, "agt", "", "", "notes.md")
		if err != nil {
			t.Fatalf("stat %d: %v", i, err)
		}
		if err := store.PutIfVersion(ctx, "agt", "", "", "notes.md", strings.NewReader("v9"), 2, "text/markdown", info.Version); err != nil {
			t.Fatalf("degraded overwrite %d: %v", i, err)
		}
	}

	notice := logs.String()
	if !strings.Contains(notice, "normal on DigitalOcean Spaces") {
		t.Fatalf("the degradation notice does not say where it is normal:\n%s", notice)
	}
	if !strings.Contains(notice, "not an incident") {
		t.Fatalf("the degradation notice does not separate a designed store from a broken one:\n%s", notice)
	}
	if extra := strings.Count(strings.TrimRight(notice, "\n"), "\n"); extra != 0 {
		t.Fatalf("the notice fired %d times; want one line per process:\n%s", extra+1, notice)
	}
}

// captureWorkspaceWarnings redirects slog to a buffer for the duration of one
// test — the same shape internal/agent uses for its own notices.
func captureWorkspaceWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestS3CreateOnlyUsesIfNoneMatch(t *testing.T) {
	ctx := context.Background()
	store, fake := newFakeS3Store(t)

	// A free key: create-only succeeds, and it really is a conditional request
	// (the header, not a hopeful plain PUT).
	if err := store.PutIfVersion(ctx, "agt", "", "", "fresh.md", strings.NewReader("v1"), 2, "text/markdown", VersionAbsent); err != nil {
		t.Fatalf("create-only on a free key: %v", err)
	}
	if got := fake.putHeaders[len(fake.putHeaders)-1].Get("If-None-Match"); got != "*" {
		t.Fatalf("create-only PUT carried If-None-Match %q; want \"*\"", got)
	}
	// A taken key: refused. The object is untouched (same ETag), which is the
	// promise the create-only form exists for — an upload must not replace a
	// workspace file it has never seen.
	before := fake.objects["agt/fresh.md"].etag
	err := store.PutIfVersion(ctx, "agt", "", "", "fresh.md", strings.NewReader("v2"), 2, "text/markdown", VersionAbsent)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("create-only on a taken key = %v; want ErrVersionConflict", err)
	}
	if after := fake.objects["agt/fresh.md"].etag; after != before {
		t.Fatalf("the object's ETag changed (%q → %q) on a refused create-only write", before, after)
	}
}
