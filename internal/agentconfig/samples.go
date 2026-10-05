package agentconfig

/**
 * [INPUT]: durations, one per observed check or rebuild.
 * [OUTPUT]: sampleRing — a fixed-size window that answers p50 and p99.
 * [POS]: The read cache reports its own behaviour (docs/fastagent/design/
 *        15-agent-config-consistency.md §10: check p99, rebuild rate, version
 *        lag). A window rather than a histogram because the acceptance line is
 *        about the current shape, not about every sample since boot, and
 *        because a fixed slice cannot grow without bound on a busy pod.
 * [PROTOCOL]: On change, update this header, then check agentconfig.Stats and
 *        the ops surface that reads it.
 */

import (
	"sort"
	"sync"
	"time"
)

// sampleWindow is how many recent durations the ring keeps. 512 samples are
// enough to place a p99 with reasonable resolution, and the cost is one copy of
// 512 float64 per read of the stats.
const sampleWindow = 512

type sampleRing struct {
	mu      sync.Mutex
	samples [sampleWindow]float64
	next    int
	count   int
}

func (r *sampleRing) observe(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples[r.next] = d.Seconds()
	r.next = (r.next + 1) % sampleWindow
	if r.count < sampleWindow {
		r.count++
	}
}

// percentiles returns p50 and p99 in seconds, over the samples in the window.
// It reports zeros before any sample arrives.
func (r *sampleRing) percentiles() (p50, p99 float64, count int) {
	r.mu.Lock()
	values := make([]float64, r.count)
	copy(values, r.samples[:r.count])
	count = r.count
	r.mu.Unlock()
	if count == 0 {
		return 0, 0, 0
	}
	sort.Float64s(values)
	p50 = values[percentileIndex(len(values), 0.50)]
	p99 = values[percentileIndex(len(values), 0.99)]
	return p50, p99, count
}

// percentileIndex maps a fraction to an index in a sorted slice. It picks the
// smallest value that is at or above the fraction of the samples, which is the
// usual definition for a "p99 is at most this" statement.
func percentileIndex(n int, p float64) int {
	if n <= 1 {
		return 0
	}
	idx := int(p * float64(n))
	if idx >= n {
		idx = n - 1
	}
	return idx
}
