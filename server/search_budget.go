package server

import (
	"log/slog"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
)

const MiB int64 = 1024 * 1024

// ConfigureMemory runs once at process startup. Explicit GOMEMLIMIT wins.
// Leave room for the OS, SQLite and native allocations outside Go's soft limit.
func ConfigureMemory() {
	if _, err := memlimit.Set(memlimit.WithRatio(.75), memlimit.WithProvider(memlimit.ApplyFallback(memlimit.FromCgroup, memlimit.FromSystem))); err != nil {
		slog.Warn("automatic memory budget unavailable; using conservative search defaults", "error", err)
	}
}

type SearchBudget struct {
	CPUs          int           `json:"cpu_parallelism"`
	Memory        int64         `json:"go_memory_budget_bytes"`
	Workers       int           `json:"workers"`
	QueueBytes    int64         `json:"queue_byte_capacity"`
	Queue         int           `json:"queue_capacity"`
	QueueWait     time.Duration `json:"-"`
	QueueWaitMS   int64         `json:"queue_wait_ms"`
	Execution     time.Duration `json:"-"`
	DBConnections int           `json:"database_connections"`
	Ingress       int           `json:"ingress_capacity"`
	MetadataBytes int64         `json:"metadata_cache_bytes"`
}

func baseSearchBudget(cpus int, memory int64) SearchBudget {
	// Unknown/unlimited memory must not imply an unlimited allocation.
	if memory <= 0 || memory > 1<<50 {
		memory = 384 * MiB
	}
	cpus = max(1, cpus)
	// Heuristics, not a promise of fixed per-query memory: half the Go budget
	// remains available for the rest of the process; SQLite/cache sizes are bounded.
	workers := max(1, min(cpus*2, int(memory/(64*MiB)), 32))
	queue := max(1, min(workers*256, int(memory/8/(32*1024)), 4096))
	return SearchBudget{CPUs: cpus, Memory: memory, Workers: workers, Queue: queue, QueueBytes: memory / 8,
		QueueWait: 10 * time.Second, QueueWaitMS: 10000, Execution: 10 * time.Second,
		DBConnections: workers + 8, Ingress: max(32, workers*8), MetadataBytes: min(memory/16, 64*MiB)}
}
func CurrentSearchBudget() SearchBudget {
	b := baseSearchBudget(runtime.GOMAXPROCS(0), debug.SetMemoryLimit(-1))
	b.DBConnections = max(5, min(setting("MAX_DATABASE_CONNECTIONS", b.DBConnections), 256))
	b.Workers = min(setting("MAX_ACTIVE_SEARCHES", b.Workers), max(1, b.DBConnections-4), 256)
	b.Queue = min(setting("MAX_QUEUED_SEARCHES", b.Queue), 16384)
	b.QueueWait = time.Duration(min(setting("SEARCH_QUEUE_WAIT_MS", 10000), 15000)) * time.Millisecond
	b.QueueWaitMS = b.QueueWait.Milliseconds()
	b.Execution = time.Duration(min(setting("SEARCH_EXECUTION_MS", 10000), 15000)) * time.Millisecond
	b.Ingress = min(setting("MAX_IN_FLIGHT_REQUESTS", b.Ingress), 4096)
	return b
}
