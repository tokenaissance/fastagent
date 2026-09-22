package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The one fact this file exists for: our bucket answers 412 to every If-Match, so
// an overwrite of an existing path has to land anyway.
//
// A fake cannot witness that, and the claim that it was verified came from a
// fake that emulated the header handling correctly — which is exactly why the
// refusals reached production-shaped code (2026-09-22, dev session
// MImz6pYfMoZLJabEHJRI4p: three edits of an existing file refused as "another
// writer changed it", then the tools were disabled for the turn).
//
// Run it against a real bucket:
//
//	FASTAGENT_S3_LIVE=1 \
//	FASTAGENT_OBJECT_STORE_ENDPOINT=nyc3.digitaloceanspaces.com \
//	FASTAGENT_OBJECT_STORE_BUCKET=fastagent-nyc3 \
//	FASTAGENT_OBJECT_STORE_PREFIX=dev \
//	FASTAGENT_OBJECT_STORE_REGION=nyc3 \
//	FASTAGENT_OBJECT_STORE_USESSL=true \
//	FASTAGENT_OBJECT_STORE_ACCESSKEY=… FASTAGENT_OBJECT_STORE_SECRETKEY=… \
//	go test ./internal/workspace/ -run TestS3LiveOverwriteOfAnExistingPath -v -count=1
//
// It writes one scratch object under the configured prefix and deletes it again.
func TestS3LiveOverwriteOfAnExistingPath(t *testing.T) {
	if os.Getenv("FASTAGENT_S3_LIVE") != "1" {
		t.Skip("live S3: set FASTAGENT_S3_LIVE=1 with FASTAGENT_OBJECT_STORE_* to run")
	}
	store, err := NewS3(S3Config{
		Endpoint:  os.Getenv("FASTAGENT_OBJECT_STORE_ENDPOINT"),
		Region:    os.Getenv("FASTAGENT_OBJECT_STORE_REGION"),
		Bucket:    os.Getenv("FASTAGENT_OBJECT_STORE_BUCKET"),
		Prefix:    os.Getenv("FASTAGENT_OBJECT_STORE_PREFIX"),
		AccessKey: os.Getenv("FASTAGENT_OBJECT_STORE_ACCESSKEY"),
		SecretKey: os.Getenv("FASTAGENT_OBJECT_STORE_SECRETKEY"),
		UseSSL:    os.Getenv("FASTAGENT_OBJECT_STORE_USESSL") != "false",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const agent = "probe-overwrite"
	name := fmt.Sprintf("probe-%d.md", time.Now().UnixNano())
	defer func() { _ = store.Delete(ctx, agent, "", "", name) }()

	if err := store.PutIfVersion(ctx, agent, "", "", name, strings.NewReader("v1"), 2, "text/markdown", VersionAbsent); err != nil {
		t.Fatalf("create-only on a free key: %v", err)
	}
	info, err := store.Stat(ctx, agent, "", "", name)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The write the 09-22 session could never make.
	if err := store.PutIfVersion(ctx, agent, "", "", name, strings.NewReader("v2"), 2, "text/markdown", info.Version); err != nil {
		t.Fatalf("overwrite with the live version = %v; want the write to land", err)
	}
	rc, err := store.Get(ctx, agent, "", "", name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "v2" {
		t.Fatalf("stored body = %q; want v2", body)
	}
	// And a superseded expectation still loses, on the real backend too.
	err = store.PutIfVersion(ctx, agent, "", "", name, strings.NewReader("v3"), 2, "text/markdown", info.Version)
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("write with a superseded version = %v; want ErrVersionConflict", err)
	}
}
