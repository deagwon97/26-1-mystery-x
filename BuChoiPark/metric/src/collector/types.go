package collector

import "time"

// MetricSample is a single polling result for a target container.
type MetricSample struct {
	Timestamp      time.Time
	ContainerID    string
	ContainerName  string
	CPUPercent     float64
	MemoryUsage    uint64
	MemoryLimit    uint64
	MemoryPercent  float64
	MemPgPgIn      uint64
	MemPgPgOut     uint64
	BlockReadBytes uint64
	BlockWriteBytes uint64
	NetRxBytes     uint64
	NetTxBytes     uint64
	Pids           uint64
}
