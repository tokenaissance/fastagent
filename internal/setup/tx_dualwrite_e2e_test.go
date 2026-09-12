package setup

// e2e for the transactional configs / configs_kv write, through the real
// HTTP handler. The Cloud dashboard reaches /api/config through the
// /api/fastagent proxy, so this is the path a failed mirror write would take
// in production: the request must fail AND neither table may keep a
// half-applied namespace — that leftovers are exactly what the readers'
// blob-authoritative + mirror-fallback rule exists to survive.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

var errConfigsKVDownE2E = errors.New("configs_kv write failed")

// configsKVFailingStore fails every configs_kv write. It re-wraps the
// transaction handle so the injected failure applies inside the handler's
// transaction.
type configsKVFailingStore struct{ *store.DBStore }

func (m *configsKVFailingStore) SetConfigValue(ctx context.Context, kind, scope, scopeID, name string, value store.ConfigValue) error {
	return errConfigsKVDownE2E
}

func (m *configsKVFailingStore) WithTx(ctx context.Context, fn func(store.Store) error) error {
	return m.DBStore.WithTx(ctx, func(tx store.Store) error {
		inner, ok := tx.(*store.DBStore)
		if !ok {
			return fn(tx)
		}
		return fn(&configsKVFailingStore{DBStore: inner})
	})
}

func newTxE2EStore(t *testing.T, name string) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUpdateConfig_RollsBackNamespaceWhenConfigsKVWriteFails(t *testing.T) {
	const uid = "u_tx_e2e"
	real := newTxE2EStore(t, "setup_tx_fail")
	s := &Server{dataStore: &configsKVFailingStore{DBStore: real}}

	rec := httptest.NewRecorder()
	body := `{"objectstore":{"s3":{"bucket":"tx-bucket"}}}`
	s.handleUpdateConfig(rec, cfgReq(t, http.MethodPost, uid, body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/config = %d (%s), want 500 when the mirror write fails",
			rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	rec2, err := real.GetConfigByName(ctx, store.KindSetting, uid, "", "objectstore")
	if err == nil && rec2 != nil {
		t.Fatalf("blob row committed although the mirror write failed: %+v", rec2.Data)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetConfigByName: %v", err)
	}
	kv, err := real.ListConfigValues(ctx, store.KindSetting, "user", uid, "objectstore.")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if len(kv) != 0 {
		t.Fatalf("partial mirror rows committed: %#v", kv)
	}

	// Control: the same request against a healthy store writes both tables,
	// so the assertion above is about atomicity, not about the request being
	// rejected for some other reason.
	healthy := &Server{dataStore: real}
	rec = httptest.NewRecorder()
	healthy.handleUpdateConfig(rec, cfgReq(t, http.MethodPost, uid, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("control POST /api/config = %d (%s)", rec.Code, rec.Body.String())
	}
	rec2, err = real.GetConfigByName(ctx, store.KindSetting, uid, "", "objectstore")
	if err != nil || rec2 == nil {
		t.Fatalf("control run did not write the blob row: %+v %v", rec2, err)
	}
	s3, ok := rec2.Data["s3"].(map[string]interface{})
	if !ok || s3["bucket"] != "tx-bucket" {
		t.Fatalf("control run blob data = %#v", rec2.Data)
	}
	kv, err = real.ListConfigValues(ctx, store.KindSetting, "user", uid, "objectstore.")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if len(kv) == 0 {
		t.Fatal("control run did not write the mirror rows")
	}
}

// The provider path goes through the same transaction, and its response must
// not claim success when the mirror write failed.
func TestCreateProvider_ReportsConfigsKVFailure(t *testing.T) {
	const uid = "u_tx_e2e_prov"
	real := newTxE2EStore(t, "setup_tx_fail_provider")
	s := &Server{dataStore: &configsKVFailingStore{DBStore: real}}

	req := httptest.NewRequest(http.MethodPost, "/api/providers?scope=user&scopeId="+uid,
		strings.NewReader(`{"name":"openai","apiKey":"sk-x"}`))
	req.Header.Set("Content-Type", "application/json")
	req = stampAuthAndUserID(req, uid)
	rec := httptest.NewRecorder()
	s.handleCreateProvider(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/providers = %d (%s), want 500", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if msg, _ := resp["error"].(string); msg == "" {
		t.Fatalf("no error surfaced to the caller: %s", rec.Body.String())
	}

	ctx := context.Background()
	row, err := real.GetConfigByName(ctx, store.KindProvider, uid, "", "openai")
	if err == nil && row != nil {
		t.Fatalf("provider row committed although the mirror write failed")
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetConfigByName: %v", err)
	}
}
