package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
		f.putHeaders = append(f.putHeaders, r.Header.Clone())
		// S3 semantics: If-None-Match: * means "must not exist", If-Match means
		// "must still be this version". Both answer 412 on failure.
		if r.Header.Get("If-None-Match") == "*" && exists {
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
