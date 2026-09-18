package skills

import "testing"

// The catalog's layer set and its order are the single source for two consumers,
// so they are pinned here: the platform library first, the agent's own skills
// second, and never the per-chatter layer.
func TestPublishableLayersOrderAndExclusions(t *testing.T) {
	t.Setenv("FASTAGENT_HOME", t.TempDir())

	layers, err := PublishableLayers("agt_test")
	if err != nil {
		t.Fatalf("PublishableLayers: %v", err)
	}
	if len(layers) != 2 {
		t.Fatalf("layers = %+v; want the platform layer and the agent layer", layers)
	}
	if layers[0].Layer != "managed" || layers[1].Layer != "agent" {
		t.Fatalf("order = %s,%s; want managed,agent (lowest precedence first)", layers[0].Layer, layers[1].Layer)
	}
	for _, layer := range layers {
		if layer.Layer == "personal" {
			t.Fatal("the per-chatter layer must never be published")
		}
	}
	if layers[0].Owner != GlobalSkillOwner || layers[1].Owner != "agt_test" {
		t.Fatalf("owners = %s,%s", layers[0].Owner, layers[1].Owner)
	}
}
