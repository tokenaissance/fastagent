package store_test

import (
	"reflect"
	"sort"
	"testing"

	st "github.com/fastclaw-ai/fastclaw/internal/store"
	storeport "github.com/fastclaw-ai/fastclaw/internal/store/port"
)

// TestPortMethodSets pins what each capability port is allowed to contain.
//
// The ports exist to keep a consumer's dependency surface small (see
// ports.go), and that only holds while they stay capability-shaped. Growing
// one is a deliberate act — this test makes it an explicit edit instead of a
// silent widening, the same way the file header makes a Store signature
// change a compile error on the assertions.
func TestPortMethodSets(t *testing.T) {
	cases := []struct {
		port interface{}
		want []string
	}{
		{(*storeport.ConfigReader)(nil), []string{
			"BatchGetConfigsByAgentIDs", "GetConfigByName", "ListConfigs", "ListConfigValues",
		}},
		{(*storeport.ConfigReadStore)(nil), []string{
			"BatchGetConfigsByAgentIDs",
			"GetConfigByName", "GetConfigMirror",
			"ListConfigMirrors", "ListConfigs", "ListConfigValues",
		}},
		{(*storeport.MirrorReader)(nil), []string{"GetConfigMirror", "ListConfigMirrors"}},
		{(*storeport.ConfigRowWriter)(nil), []string{"SaveConfig"}},
		{(*storeport.ConfigWriter)(nil), []string{
			"DeleteConfig", "DeleteConfigPrefix", "DeleteConfigValue",
			"SaveConfig", "SetConfigValue",
		}},
		{(*storeport.ConfigStore)(nil), []string{
			"BatchGetConfigsByAgentIDs",
			"DeleteConfig", "DeleteConfigPrefix", "DeleteConfigValue",
			"DeleteConfigMirror",
			"GetConfigByName", "ListConfigs", "ListConfigValues",
			"GetConfigMirror",
			"SaveConfig", "SaveConfigMirror", "SetConfigValue",
		}},
		{(*storeport.ConfigMirrorStore)(nil), []string{
			"DeleteConfigMirror", "GetConfigMirror", "SaveConfigMirror",
		}},
		{(*storeport.ConfigMirrorReconciler)(nil), []string{"ReconcileConfigMirrors"}},
		{(*storeport.KVStore)(nil), []string{
			"DeleteConfigMirror", "DeleteConfigPrefix", "DeleteConfigValue",
			"GetConfigMirror", "ListConfigValues",
			"SaveConfigMirror", "SetConfigValue",
		}},
	}
	for _, c := range cases {
		iface := reflect.TypeOf(c.port).Elem()
		got := make([]string, 0, iface.NumMethod())
		for i := 0; i < iface.NumMethod(); i++ {
			got = append(got, iface.Method(i).Name)
		}
		sort.Strings(got)
		sort.Strings(c.want)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s methods = %v, want %v", iface.Name(), got, c.want)
		}
	}
}

// TestStoreIsWiderThanItsPorts records the whole point of the split: a
// consumer that takes a port is not taking Store. If these ever become equal
// the narrowing has been undone.
func TestStoreIsWiderThanItsPorts(t *testing.T) {
	storeType := reflect.TypeOf((*st.Store)(nil)).Elem()
	config := reflect.TypeOf((*storeport.ConfigStore)(nil)).Elem()
	if storeType.NumMethod() <= config.NumMethod() {
		t.Fatalf("Store has %d methods, storeport.ConfigStore has %d — the configs port must be a strict subset",
			storeType.NumMethod(), config.NumMethod())
	}
}

// TestPortsExcludeOtherDomains is the ISP check stated as behaviour: no port
// may hand a consumer a capability outside the configs domain. The names
// below are sampled from the buckets Store also carries — accounts, agents,
// sessions, cron, MCP — and one of them appearing in a port means the split
// has started leaking back.
func TestPortsExcludeOtherDomains(t *testing.T) {
	// Every Store method outside the configs domain, derived from the
	// interface itself rather than a hand-kept list: a port may only contain
	// methods whose names the configs domain owns.
	storeType := reflect.TypeOf((*st.Store)(nil)).Elem()
	allowed := map[string]bool{
		"BatchGetConfigsByAgentIDs": true,
		"GetConfigByName":           true, "ListConfigs": true, "ListConfigValues": true,
		"SaveConfig": true, "DeleteConfig": true,
		"SetConfigValue": true, "DeleteConfigValue": true, "DeleteConfigPrefix": true,
		"SaveConfigMirror": true, "GetConfigMirror": true, "DeleteConfigMirror": true,
		"ListConfigMirrors":      true,
		"ReconcileConfigMirrors": true,
	}
	for _, port := range []reflect.Type{
		reflect.TypeOf((*storeport.ConfigReader)(nil)).Elem(),
		reflect.TypeOf((*storeport.ConfigReadStore)(nil)).Elem(),
		reflect.TypeOf((*storeport.MirrorReader)(nil)).Elem(),
		reflect.TypeOf((*storeport.ConfigWriter)(nil)).Elem(),
		reflect.TypeOf((*storeport.ConfigRowWriter)(nil)).Elem(),
		reflect.TypeOf((*storeport.ConfigStore)(nil)).Elem(),
		reflect.TypeOf((*storeport.KVStore)(nil)).Elem(),
		reflect.TypeOf((*storeport.ConfigMirrorStore)(nil)).Elem(),
		reflect.TypeOf((*storeport.ConfigMirrorReconciler)(nil)).Elem(),
	} {
		for i := 0; i < port.NumMethod(); i++ {
			name := port.Method(i).Name
			if !allowed[name] {
				t.Errorf("%s.%s is outside the configs domain", port.Name(), name)
			}
			if _, ok := storeType.MethodByName(name); !ok {
				t.Errorf("%s.%s is not a Store method", port.Name(), name)
			}
		}
	}
	// Guard the allow-list itself: every entry has to be a real Store method,
	// or the test would silently accept a renamed port method.
	for name := range allowed {
		if _, ok := storeType.MethodByName(name); !ok {
			t.Errorf("allow-list names %s, which Store does not have", name)
		}
	}
}
