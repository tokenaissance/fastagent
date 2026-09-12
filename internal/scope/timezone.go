package scope

import (
	"context"
	"errors"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// PrefsNamespace is the kind=setting namespace for per-chatter
// preferences (timezone today; language etc. later). Rows live in
// configs like every other setting, but the RESOLUTION ORDER differs
// from scope.Setting: a chatter's own preference must beat the agent's
// default, so the walk is chatter-first (see Timezone below) instead of
// the usual outer→inner merge where the agent layer would shadow the
// user layer.
const PrefsNamespace = "prefs"

// prefsTimezoneKey is the JSON key inside the prefs namespace holding
// an IANA timezone name ("Asia/Shanghai").
const prefsTimezoneKey = "timezone"

// Timezone resolves the effective IANA timezone name for a chatter
// talking to an agent. Most-specific wins:
//
//	(chatter, agent) → (chatter, '') → ('', agent) → ('', '')
//
// i.e. a per-(chatter, agent) override, then the chatter's personal
// setting (follows them across agents), then the agent's default, then
// the system default. Returns "" when nothing is set — callers fall
// back to server-local time.
//
// Each layer is resolved through settingLayerAt, the one per-layer rule every
// settings reader shares, so this reader answers from a marker-certified mirror
// under configsKVFirst and from the blob under blobFirst — it is not a second
// read path with its own table choice. What is special here is only the *layer
// order* (most specific first, first hit wins) and that a disabled row at a
// layer vetoes the less specific layers below it instead of being skipped past.
//
// The walk deliberately does not merge or project: it asks one layer at a time
// whether it has a timezone. That keeps a non-authoritative row from changing a
// user-visible time — a prefs row that only exists in an uncertified mirror
// falls back to the blob like every other read, and a KV-only prefs writer (none
// exists today; SaveUserTimezone → SaveSetting dual-writes) would owe the blob
// row rather than get served by widening this loop.
func Timezone(ctx context.Context, st store.ConfigReadStore, chatterUID, agentID string) string {
	if st == nil {
		return ""
	}
	type layer struct{ uid, aid string }
	layers := []layer{}
	if chatterUID != "" && agentID != "" {
		layers = append(layers, layer{chatterUID, agentID})
	}
	if chatterUID != "" {
		layers = append(layers, layer{chatterUID, ""})
	}
	if agentID != "" {
		layers = append(layers, layer{"", agentID})
	}
	layers = append(layers, layer{"", ""})
	for _, l := range layers {
		data, enabled, present, err := settingLayerAt(ctx, st, PrefsNamespace, l.uid, l.aid)
		if err != nil || !present {
			continue
		}
		// Same rule as every other reader: a disabled row is this layer's
		// decision that the name is off, so the less specific layers below
		// it must not answer instead.
		if !enabled {
			return ""
		}
		if tz, ok := data[prefsTimezoneKey].(string); ok && tz != "" {
			return tz
		}
	}
	return ""
}

// LoadLocationOrLocal resolves an IANA timezone name to a
// *time.Location, falling back to server-local time when the name is
// empty or unknown. Never returns nil.
func LoadLocationOrLocal(name string) *time.Location {
	if name == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}

// SaveUserTimezone records tz as userID's personal timezone at user
// scope (agent_id='') so it follows the chatter across every agent
// they talk to. Other keys already present in the user's prefs row are
// preserved. tz must be a valid IANA name — validate with
// time.LoadLocation before calling.
func SaveUserTimezone(ctx context.Context, st store.ConfigStore, userID, tz string) error {
	if st == nil {
		return errors.New("scope.SaveUserTimezone: store is required")
	}
	if userID == "" {
		return errors.New("scope.SaveUserTimezone: userID is required")
	}
	data := map[string]interface{}{}
	if rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, "", PrefsNamespace); err == nil && rec != nil {
		for k, v := range rec.Data {
			data[k] = v
		}
	}
	data[prefsTimezoneKey] = tz
	return SaveSetting(ctx, st, userID, "", PrefsNamespace, data)
}
