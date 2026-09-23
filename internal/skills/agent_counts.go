package skills

// PublishedCounts answers, for each agent, how many skills it publishes over MCP.
//
// The number has to be the one `list_skills` would list for that agent, or a caller
// comparing the two lines reads the difference as a content change. So the count
// comes from the same two steps the catalog endpoint runs - PublishableLayers, then
// BuildCatalog - and never from a second rule about which directories count.
//
// Agreement is per read, not an invariant: this is one read and `list_skills` is
// another, so a layer written in between makes the two numbers differ. Accepted
// (2026-09-23): the reasons and the conditions that would reopen it are at this
// function's caller, internal/setup/handlers_agents.go.
//
// Cost is why the layers are scanned once per call rather than once per agent: the
// platform library is shared, so N agents cost N agent layers plus one shared layer
// instead of N of each. Scanning is not separable from hashing (ScanSkillDirs reads
// the bytes the digest covers), so a "count only" walk would be a second opinion
// about what a skill contains - the defect this package exists to prevent.
//
// The two maps are the answer's two halves, and they are kept apart because "this
// agent publishes nothing" and "we could not read this agent's skills" are different
// facts: a caller that cannot tell them apart concludes the first from the second.
// A count is reported only when every one of the agent's layers was read; a layer
// that errored (which ScanSkillDirs reports alongside partial results) puts the agent
// in the failures map and its count nowhere.
func PublishedCounts(agentIDs []string) (counts map[string]int, failures map[string]string) {
	counts = make(map[string]int, len(agentIDs))
	failures = make(map[string]string)

	type scan struct {
		found []DiscoveredSkill
		err   error
	}
	scanned := make(map[string]scan)

	for _, agentID := range agentIDs {
		layers, err := PublishableLayers(agentID)
		if err != nil {
			failures[agentID] = err.Error()
			continue
		}

		var discovered []DiscoveredSkill
		for _, layer := range layers {
			got, ok := scanned[layer.Dir]
			if !ok {
				found, scanErr := ScanSkillDirs([]string{layer.Dir})
				got = scan{found: found, err: scanErr}
				scanned[layer.Dir] = got
			}
			if got.err != nil {
				failures[agentID] = got.err.Error()
				discovered = nil
				break
			}
			// Concatenation in layer order is what carries precedence: BuildCatalog
			// decides shadowing by the order it receives, never by comparing names.
			discovered = append(discovered, got.found...)
		}
		if _, failed := failures[agentID]; failed {
			continue
		}
		counts[agentID] = len(BuildCatalog(discovered).Skills)
	}
	return counts, failures
}
