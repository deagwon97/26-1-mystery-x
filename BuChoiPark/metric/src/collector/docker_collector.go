package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type DockerCollector struct {
	httpClient *http.Client
	baseURL    string
}

func NewDockerCollector() (*DockerCollector, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", "/var/run/docker.sock")
		},
	}

	httpClient := &http.Client{Transport: transport}
	collector := &DockerCollector{
		httpClient: httpClient,
		baseURL:    "http://docker",
	}

	apiVersion, err := collector.fetchAPIVersion(context.Background())
	if err == nil && apiVersion != "" {
		collector.baseURL = collector.baseURL + "/v" + apiVersion
	}

	return collector, nil
}

func (d *DockerCollector) Close() error {
	if transport, ok := d.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

func (d *DockerCollector) ResolveContainer(ctx context.Context, target string) (string, string, error) {
	var containers []containerSummary
	err := d.getJSON(ctx, "/containers/json?all=1", &containers)
	if err != nil {
		return "", "", fmt.Errorf("list containers: %w", err)
	}

	for _, c := range containers {
		if c.ID == target || strings.HasPrefix(c.ID, target) {
			return c.ID, trimContainerName(c.Names), nil
		}
		for _, name := range c.Names {
			if strings.TrimPrefix(name, "/") == target {
				return c.ID, strings.TrimPrefix(name, "/"), nil
			}
		}
	}

	return "", "", fmt.Errorf("container not found: %s", target)
}

func (d *DockerCollector) CollectOnce(ctx context.Context, containerID string, containerName string) (MetricSample, error) {
	var stats containerStats
	path := fmt.Sprintf("/containers/%s/stats?stream=false", url.PathEscape(containerID))
	if err := d.getJSON(ctx, path, &stats); err != nil {
		return MetricSample{}, fmt.Errorf("container stats: %w", err)
	}

	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemCPUUsage - stats.PreCPUStats.SystemCPUUsage)
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}

	cpuPercent := 0.0
	if cpuDelta > 0 && systemDelta > 0 && onlineCPUs > 0 {
		cpuPercent = (cpuDelta / systemDelta) * onlineCPUs * 100.0
	}

	memoryUsage := calculateMemoryUsage(stats.MemoryStats)
	memoryLimit := stats.MemoryStats.Limit
	memoryPercent := 0.0
	if memoryLimit > 0 {
		memoryPercent = (float64(memoryUsage) / float64(memoryLimit)) * 100.0
	}

	memPgPgIn := stats.MemoryStats.Stats["pgpgin"]
	memPgPgOut := stats.MemoryStats.Stats["pgpgout"]

	blockReadBytes := uint64(0)
	blockWriteBytes := uint64(0)
	for _, entry := range stats.BlkioStats.IOServiceBytesRecursive {
		op := strings.ToLower(entry.Op)
		switch op {
		case "read":
			blockReadBytes += entry.Value
		case "write":
			blockWriteBytes += entry.Value
		}
	}

	netRxBytes := uint64(0)
	netTxBytes := uint64(0)
	for _, n := range stats.Networks {
		netRxBytes += n.RxBytes
		netTxBytes += n.TxBytes
	}

	return MetricSample{
		Timestamp:       stats.Read.Time,
		ContainerID:     containerID,
		ContainerName:   containerName,
		CPUPercent:      cpuPercent,
		MemoryUsage:     memoryUsage,
		MemoryLimit:     memoryLimit,
		MemoryPercent:   memoryPercent,
		MemPgPgIn:       memPgPgIn,
		MemPgPgOut:      memPgPgOut,
		BlockReadBytes:  blockReadBytes,
		BlockWriteBytes: blockWriteBytes,
		NetRxBytes:      netRxBytes,
		NetTxBytes:      netTxBytes,
		Pids:            stats.PidsStats.Current,
	}, nil
}

func trimContainerName(names []string) string {
	if len(names) == 0 {
		return "unknown"
	}
	return strings.TrimPrefix(names[0], "/")
}

func calculateMemoryUsage(stats memoryStats) uint64 {
	usage := stats.Usage
	cache, ok := memoryCache(stats.Stats, usage)
	if !ok {
		return usage
	}
	return usage - cache
}

func memoryCache(values map[string]uint64, usage uint64) (uint64, bool) {
	if values == nil {
		return 0, false
	}

	// Match Docker CLI behavior:
	// cgroup v1 prefers total_inactive_file, cgroup v2 uses inactive_file.
	if totalInactiveFile, ok := values["total_inactive_file"]; ok && totalInactiveFile < usage {
		return totalInactiveFile, true
	}
	if inactiveFile, ok := values["inactive_file"]; ok && inactiveFile < usage {
		return inactiveFile, true
	}
	if cache, ok := values["cache"]; ok && cache < usage {
		return cache, true
	}
	return 0, false
}

func (d *DockerCollector) fetchAPIVersion(ctx context.Context) (string, error) {
	var version dockerVersion
	if err := d.getJSONRaw(ctx, d.baseURL+"/version", &version); err != nil {
		return "", err
	}
	return version.APIVersion, nil
}

func (d *DockerCollector) getJSON(ctx context.Context, path string, out any) error {
	return d.getJSONRaw(ctx, d.baseURL+path, out)
}

func (d *DockerCollector) getJSONRaw(ctx context.Context, requestURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

type dockerVersion struct {
	APIVersion string `json:"ApiVersion"`
}

type containerSummary struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
}

type containerStats struct {
	Read        jsonTime               `json:"read"`
	CPUStats    cpuStats               `json:"cpu_stats"`
	PreCPUStats cpuStats               `json:"precpu_stats"`
	MemoryStats memoryStats            `json:"memory_stats"`
	BlkioStats  blkioStats             `json:"blkio_stats"`
	Networks    map[string]networkStat `json:"networks"`
	PidsStats   pidsStats              `json:"pids_stats"`
}

type cpuStats struct {
	CPUUsage       cpuUsage `json:"cpu_usage"`
	SystemCPUUsage uint64   `json:"system_cpu_usage"`
	OnlineCPUs     uint32   `json:"online_cpus"`
}

type cpuUsage struct {
	TotalUsage  uint64   `json:"total_usage"`
	PercpuUsage []uint64 `json:"percpu_usage"`
}

type memoryStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

type blkioStats struct {
	IOServiceBytesRecursive []blkioEntry `json:"io_service_bytes_recursive"`
}

type blkioEntry struct {
	Op    string `json:"op"`
	Value uint64 `json:"value"`
}

type networkStat struct {
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

type pidsStats struct {
	Current uint64 `json:"current"`
}

type jsonTime struct {
	time.Time
}

func (t *jsonTime) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}
