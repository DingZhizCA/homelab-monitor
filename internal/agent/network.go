package agent

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type networkCounter struct{ rx, tx uint64 }

func collectNetworks(previous map[string]networkCounter, elapsed time.Duration) ([]metrics.NetworkMetrics, map[string]networkCounter) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, previous
	}
	defer f.Close()
	current := make(map[string]networkCounter)
	result := []metrics.NetworkMetrics{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.SplitN(s.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" || name == "" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rx, e1 := strconv.ParseUint(fields[0], 10, 64)
		tx, e2 := strconv.ParseUint(fields[8], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		current[name] = networkCounter{rx, tx}
		item := metrics.NetworkMetrics{Interface: name, RXBytes: rx, TXBytes: tx}
		if old, ok := previous[name]; ok && elapsed > 0 && rx >= old.rx && tx >= old.tx {
			seconds := elapsed.Seconds()
			item.RXBytesPerSecond = float64(rx-old.rx) / seconds
			item.TXBytesPerSecond = float64(tx-old.tx) / seconds
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Interface < result[j].Interface })
	return result, current
}
