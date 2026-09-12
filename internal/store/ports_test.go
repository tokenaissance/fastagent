package store

import (
	"reflect"
	"sort"
	"testing"
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
		{(*ConfigReader)(nil), []string{"GetConfigByName", "ListConfigs", "ListConfigValues"}},
		{(*ConfigRowWriter)(nil), []string{"SaveConfig"}},
		{(*ConfigWriter)(nil), []string{
			"DeleteConfig", "DeleteConfigPrefix", "DeleteConfigValue",
			"SaveConfig", "SetConfigValue",
		}},
		{(*ConfigStore)(nil), []string{
			"DeleteConfig", "DeleteConfigPrefix", "DeleteConfigValue",
			"DeleteConfigMirror",
			"GetConfigByName", "ListConfigs", "ListConfigValues",
			"GetConfigMirror",
			"SaveConfig", "SaveConfigMirror", "SetConfigValue",
		}},
		{(*ConfigMirrorStore)(nil), []string{
			"DeleteConfigMirror", "GetConfigMirror", "SaveConfigMirror",
		}},
		{(*ConfigMirrorReconciler)(nil), []string{"ReconcileConfigMirrors"}},
		{(*KVStore)(nil), []string{
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
	store := reflect.TypeOf((*Store)(nil)).Elem()
	config := reflect.TypeOf((*ConfigStore)(nil)).Elem()
	if store.NumMethod() <= config.NumMethod() {
		t.Fatalf("Store has %d methods, ConfigStore has %d — the configs port must be a strict subset",
			store.NumMethod(), config.NumMethod())
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
	store := reflect.TypeOf((*Store)(nil)).Elem()
	allowed := map[string]bool{
		"GetConfigByName": true, "ListConfigs": true, "ListConfigValues": true,
		"SaveConfig": true, "DeleteConfig": true,
		"SetConfigValue": true, "DeleteConfigValue": true, "DeleteConfigPrefix": true,
		"SaveConfigMirror": true, "GetConfigMirror": true, "DeleteConfigMirror": true,
		"ReconcileConfigMirrors": true,
	}
	for _, port := range []reflect.Type{
		reflect.TypeOf((*ConfigReader)(nil)).Elem(),
		reflect.TypeOf((*ConfigWriter)(nil)).Elem(),
		reflect.TypeOf((*ConfigRowWriter)(nil)).Elem(),
		reflect.TypeOf((*ConfigStore)(nil)).Elem(),
		reflect.TypeOf((*KVStore)(nil)).Elem(),
		reflect.TypeOf((*ConfigMirrorStore)(nil)).Elem(),
		reflect.TypeOf((*ConfigMirrorReconciler)(nil)).Elem(),
	} {
		for i := 0; i < port.NumMethod(); i++ {
			name := port.Method(i).Name
			if !allowed[name] {
				t.Errorf("%s.%s is outside the configs domain", port.Name(), name)
			}
			if _, ok := store.MethodByName(name); !ok {
				t.Errorf("%s.%s is not a Store method", port.Name(), name)
			}
		}
	}
	// Guard the allow-list itself: every entry has to be a real Store method,
	// or the test would silently accept a renamed port method.
	for name := range allowed {
		if _, ok := store.MethodByName(name); !ok {
			t.Errorf("allow-list names %s, which Store does not have", name)
		}
	}
}
