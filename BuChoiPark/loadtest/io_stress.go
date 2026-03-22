package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stage struct {
	Concurrency int
	Duration    time.Duration
}

type endpointStat struct {
	mu         sync.Mutex
	latencies  []time.Duration
	count      int64
	errors     int64
	bytes      int64
	statusCode map[int]int64
	zeroKinds  map[string]int64
}

func newEndpointStat() *endpointStat {
	return &endpointStat{
		statusCode: make(map[int]int64),
		zeroKinds:  make(map[string]int64),
	}
}

func (s *endpointStat) add(latency time.Duration, status int, payloadBytes int64, isError bool, zeroKind string) {
	s.mu.Lock()
	s.latencies = append(s.latencies, latency)
	s.statusCode[status]++
	if status == 0 && zeroKind != "" {
		s.zeroKinds[zeroKind]++
	}
	s.mu.Unlock()
	atomic.AddInt64(&s.count, 1)
	atomic.AddInt64(&s.bytes, payloadBytes)
	if isError {
		atomic.AddInt64(&s.errors, 1)
	}
}

func (s *endpointStat) summary() (count int64, errors int64, totalBytes int64, p95 time.Duration, p99 time.Duration, codes map[int]int64, zeroKinds map[string]int64) {
	count = atomic.LoadInt64(&s.count)
	errors = atomic.LoadInt64(&s.errors)
	totalBytes = atomic.LoadInt64(&s.bytes)

	s.mu.Lock()
	defer s.mu.Unlock()

	codes = make(map[int]int64, len(s.statusCode))
	for k, v := range s.statusCode {
		codes[k] = v
	}
	zeroKinds = make(map[string]int64, len(s.zeroKinds))
	for k, v := range s.zeroKinds {
		zeroKinds[k] = v
	}

	if len(s.latencies) == 0 {
		return count, errors, totalBytes, 0, 0, codes, zeroKinds
	}

	copied := append([]time.Duration(nil), s.latencies...)
	sort.Slice(copied, func(i, j int) bool { return copied[i] < copied[j] })
	p95 = percentile(copied, 95)
	p99 = percentile(copied, 99)
	return count, errors, totalBytes, p95, p99, codes, zeroKinds
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil((p/100.0)*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type metrics struct {
	endpoints map[string]*endpointStat

	readOps    int64
	writeOps   int64
	readBytes  int64
	writeBytes int64
}

func newMetrics(keys ...string) *metrics {
	m := &metrics{endpoints: make(map[string]*endpointStat, len(keys))}
	for _, k := range keys {
		m.endpoints[k] = newEndpointStat()
	}
	return m
}

func (m *metrics) add(endpoint string, latency time.Duration, status int, payloadBytes int64, isError bool, zeroKind string) {
	st, ok := m.endpoints[endpoint]
	if !ok {
		return
	}
	st.add(latency, status, payloadBytes, isError, zeroKind)

	if endpoint == "GET /files/{id}/download" {
		atomic.AddInt64(&m.readOps, 1)
		atomic.AddInt64(&m.readBytes, payloadBytes)
		return
	}
	if endpoint == "POST /files/upload" {
		atomic.AddInt64(&m.writeOps, 1)
		atomic.AddInt64(&m.writeBytes, payloadBytes)
	}
}

type config struct {
	baseURL   string
	stagesRaw string
	seed      int64

	userID string

	readRatio float64
	uploadMode string

	uploadMinMB int
	uploadMaxMB int

	prepareCount    int
	prepareSizeMB   int
	prepareTimeout  time.Duration
	skipPrepare     bool
	existingFileIDs string

	httpTimeout time.Duration
	topCodes    int
}

func parseFlags() config {
	cfg := config{}
	defaultBaseURL := os.Getenv("TARGET_BASE_URL")
	if defaultBaseURL == "" {
		defaultBaseURL = "http://localhost:8080"
	}

	flag.StringVar(&cfg.baseURL, "base-url", defaultBaseURL, "Target base URL")
	flag.StringVar(&cfg.stagesRaw, "stages", "4:40s", "Stage spec: concurrency:duration comma-separated")
	flag.Int64Var(&cfg.seed, "seed", time.Now().UnixNano(), "Random seed")

	flag.StringVar(&cfg.userID, "user-id", "io-stress-user", "userId used for upload/list")
	flag.Float64Var(&cfg.readRatio, "read-ratio", 0.50, "Read ratio (0~1), write ratio is 1 - read-ratio")
	flag.StringVar(&cfg.uploadMode, "upload-mode", "multipart", "Upload payload mode: multipart or raw")
	flag.IntVar(&cfg.uploadMinMB, "upload-min-mb", 100, "Minimum upload size in MB")
	flag.IntVar(&cfg.uploadMaxMB, "upload-max-mb", 200, "Maximum upload size in MB")

	flag.IntVar(&cfg.prepareCount, "prepare-count", 8, "Number of files uploaded before stress")
	flag.IntVar(&cfg.prepareSizeMB, "prepare-size-mb", 100, "Size (MB) of each prepare upload")
	flag.DurationVar(&cfg.prepareTimeout, "prepare-timeout", 10*time.Minute, "Timeout for each prepare upload")
	flag.BoolVar(&cfg.skipPrepare, "skip-prepare", false, "Skip prepare uploads and use existing file IDs")
	flag.StringVar(&cfg.existingFileIDs, "file-ids", "", "Comma-separated existing file IDs used with -skip-prepare=true")

	flag.DurationVar(&cfg.httpTimeout, "timeout", 0, "HTTP timeout for requests (0 means no timeout)")
	flag.IntVar(&cfg.topCodes, "top-codes", 8, "Max status codes shown per endpoint")
	flag.Parse()

	if cfg.readRatio < 0 || cfg.readRatio > 1 {
		panic("read-ratio must be between 0 and 1")
	}
	cfg.uploadMode = strings.ToLower(strings.TrimSpace(cfg.uploadMode))
	if cfg.uploadMode != "multipart" && cfg.uploadMode != "raw" {
		panic("upload-mode must be one of: multipart, raw")
	}
	if cfg.uploadMinMB <= 0 || cfg.uploadMaxMB < cfg.uploadMinMB {
		panic("invalid upload-min-mb/upload-max-mb")
	}
	if cfg.prepareCount <= 0 {
		panic("prepare-count must be > 0")
	}
	if cfg.prepareSizeMB <= 0 {
		panic("prepare-size-mb must be > 0")
	}
	if cfg.prepareTimeout <= 0 {
		panic("prepare-timeout must be > 0")
	}
	if cfg.topCodes <= 0 {
		panic("top-codes must be > 0")
	}
	if cfg.skipPrepare && strings.TrimSpace(cfg.existingFileIDs) == "" {
		panic("when -skip-prepare=true, provide -file-ids")
	}

	return cfg
}

func parseStages(spec string) ([]stage, error) {
	parts := strings.Split(spec, ",")
	out := make([]stage, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tokens := strings.Split(part, ":")
		if len(tokens) != 2 {
			return nil, fmt.Errorf("invalid stage token: %q", part)
		}
		c, err := strconv.Atoi(strings.TrimSpace(tokens[0]))
		if err != nil || c <= 0 {
			return nil, fmt.Errorf("invalid concurrency in %q", part)
		}
		d, err := time.ParseDuration(strings.TrimSpace(tokens[1]))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid duration in %q", part)
		}
		out = append(out, stage{Concurrency: c, Duration: d})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid stage parsed")
	}
	return out, nil
}

type uploadResponse struct {
	ID string `json:"id"`
}

type endlessByteReader struct{}

func (r endlessByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

type filePool struct {
	mu  sync.RWMutex
	ids []string
}

func newFilePool(initial []string) *filePool {
	copied := append([]string(nil), initial...)
	return &filePool{ids: copied}
}

func (p *filePool) add(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	p.ids = append(p.ids, id)
	p.mu.Unlock()
}

func (p *filePool) random(r *rand.Rand) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.ids) == 0 {
		return "", false
	}
	return p.ids[r.Intn(len(p.ids))], true
}

func main() {
	cfg := parseFlags()
	stages, err := parseStages(cfg.stagesRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stage parse error: %v\n", err)
		os.Exit(2)
	}

	rng := rand.New(rand.NewSource(cfg.seed))
	client := &http.Client{Timeout: cfg.httpTimeout}

	fmt.Println("I/O stress started")
	fmt.Printf("baseURL=%s stages=%s seed=%d\n", cfg.baseURL, cfg.stagesRaw, cfg.seed)
	fmt.Printf("mode: read-ratio=%.2f write-ratio=%.2f upload-size=%d~%dMB upload-mode=%s\n", cfg.readRatio, 1.0-cfg.readRatio, cfg.uploadMinMB, cfg.uploadMaxMB, cfg.uploadMode)
	fmt.Printf("prepare: skip=%v count=%d sizeMB=%d userId=%s\n", cfg.skipPrepare, cfg.prepareCount, cfg.prepareSizeMB, cfg.userID)

	ids := make([]string, 0)
	if cfg.skipPrepare {
		for _, token := range strings.Split(cfg.existingFileIDs, ",") {
			id := strings.TrimSpace(token)
			if id != "" {
				ids = append(ids, id)
			}
		}
	} else {
		ids, err = prepareFiles(cfg, client, rng)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prepare failed: %v\n", err)
			os.Exit(1)
		}
	}

	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "no file IDs available")
		os.Exit(1)
	}

	fmt.Printf("prepared file IDs: %s\n", strings.Join(ids, ","))

	pool := newFilePool(ids)
	m := newMetrics("POST /files/upload", "GET /files/{id}/download")

	start := time.Now()
	runStress(stages, cfg, client, pool, m)
	elapsed := time.Since(start)
	printReport(cfg, m, elapsed)
}

func prepareFiles(cfg config, client *http.Client, r *rand.Rand) ([]string, error) {
	out := make([]string, 0, cfg.prepareCount)
	sizeBytes := int64(cfg.prepareSizeMB) * 1024 * 1024

	for i := 0; i < cfg.prepareCount; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.prepareTimeout)
		filePath := fmt.Sprintf("/io-stress/prepare/%02d/f-%d.bin", i+1, r.Int63())
		id, status, _, _, err := uploadFile(ctx, client, cfg, filePath, sizeBytes)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("prepare upload failed idx=%d err=%w", i+1, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("prepare upload failed idx=%d status=%d", i+1, status)
		}
		if id == "" {
			return nil, fmt.Errorf("prepare upload returned empty id idx=%d", i+1)
		}
		fmt.Printf("prepared %d/%d id=%s path=%s\n", i+1, cfg.prepareCount, id, filePath)
		out = append(out, id)
	}
	return out, nil
}

func runStress(stages []stage, cfg config, client *http.Client, pool *filePool, m *metrics) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	var workerWG sync.WaitGroup
	workerStops := make([]context.CancelFunc, 0)

	spawnWorker := func(id int) {
		ctx, cancel := context.WithCancel(rootCtx)
		workerStops = append(workerStops, cancel)
		workerWG.Add(1)

		go func(workerID int, wctx context.Context) {
			defer workerWG.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(1000+workerID*7919)))

			for {
				select {
				case <-wctx.Done():
					return
				default:
				}

				shouldRead := r.Float64() < cfg.readRatio
				if shouldRead {
					id, ok := pool.random(r)
					if ok {
						status, bytes, latency, err := downloadFile(wctx, client, cfg.baseURL, id)
						zeroKind, isErr := classifyRequestError(err, status)
						m.add("GET /files/{id}/download", latency, status, bytes, isErr, zeroKind)
						continue
					}
				}

				sizeMB := cfg.uploadMinMB
				if cfg.uploadMaxMB > cfg.uploadMinMB {
					sizeMB = cfg.uploadMinMB + r.Intn(cfg.uploadMaxMB-cfg.uploadMinMB+1)
				}
				sizeBytes := int64(sizeMB) * 1024 * 1024
				filePath := fmt.Sprintf("/io-stress/run/%02d/%d.bin", workerID, r.Int63())
				uploadedID, status, latency, _, err := uploadFile(wctx, client, cfg, filePath, sizeBytes)
				zeroKind, isErr := classifyRequestError(err, status)
				m.add("POST /files/upload", latency, status, sizeBytes, isErr, zeroKind)
				if err == nil && status >= 200 && status < 300 {
					pool.add(uploadedID)
				}
			}
		}(id, ctx)
	}

	currentWorkers := 0
	setWorkers := func(target int) {
		if target == currentWorkers {
			return
		}
		if target > currentWorkers {
			for i := currentWorkers; i < target; i++ {
				spawnWorker(i + 1)
			}
			currentWorkers = target
			return
		}

		for i := currentWorkers - 1; i >= target; i-- {
			workerStops[i]()
		}
		workerStops = workerStops[:target]
		currentWorkers = target
	}

	for _, st := range stages {
		setWorkers(st.Concurrency)
		fmt.Printf("stage: concurrency=%d duration=%s\n", st.Concurrency, st.Duration)
		select {
		case <-rootCtx.Done():
			break
		case <-time.After(st.Duration):
		}
	}

	setWorkers(0)
	workerWG.Wait()
}

func uploadFile(ctx context.Context, client *http.Client, cfg config, filePath string, sizeBytes int64) (string, int, time.Duration, []byte, error) {
	if cfg.uploadMode == "raw" {
		return uploadFileRaw(ctx, client, cfg.baseURL, cfg.userID, filePath, sizeBytes)
	}
	return uploadFileMultipart(ctx, client, cfg.baseURL, cfg.userID, filePath, sizeBytes)
}

func uploadFileMultipart(ctx context.Context, client *http.Client, baseURL, userID, filePath string, sizeBytes int64) (string, int, time.Duration, []byte, error) {
	start := time.Now()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer mw.Close()

		if err := mw.WriteField("userId", userID); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := mw.WriteField("filePath", filePath); err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		part, err := mw.CreateFormFile("file", "payload.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}

		if _, err = io.CopyN(part, endlessByteReader{}, sizeBytes); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/files/upload", pr)
	if err != nil {
		return "", 0, 0, nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, time.Since(start), nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	var parsed uploadResponse
	_ = json.Unmarshal(body, &parsed)
	return parsed.ID, resp.StatusCode, time.Since(start), body, nil
}

func uploadFileRaw(ctx context.Context, client *http.Client, baseURL, userID, filePath string, sizeBytes int64) (string, int, time.Duration, []byte, error) {
	start := time.Now()
	body := io.LimitReader(endlessByteReader{}, sizeBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/files/upload", body)
	if err != nil {
		return "", 0, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-File-Path", filePath)
	req.Header.Set("X-File-Name", path.Base(filePath))

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, time.Since(start), nil, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	var parsed uploadResponse
	_ = json.Unmarshal(bodyBytes, &parsed)
	return parsed.ID, resp.StatusCode, time.Since(start), bodyBytes, nil
}

func downloadFile(ctx context.Context, client *http.Client, baseURL, id string) (int, int64, time.Duration, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/files/"+id+"/download", nil)
	if err != nil {
		return 0, 0, 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, time.Since(start), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		return resp.StatusCode, 0, time.Since(start), nil
	}

	n, readErr := io.Copy(io.Discard, resp.Body)
	if readErr != nil {
		return resp.StatusCode, n, time.Since(start), readErr
	}
	return resp.StatusCode, n, time.Since(start), nil
}

func ratioPercent(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100.0 / float64(b)
}

func bytesToMiB(v int64) float64 {
	return float64(v) / (1024.0 * 1024.0)
}

func classifyRequestError(err error, status int) (zeroKind string, isError bool) {
	if status >= 500 {
		return "", true
	}
	if err == nil {
		if status == 0 {
			return "status0_unknown", true
		}
		return "", false
	}

	if errors.Is(err, context.Canceled) {
		if status == 0 {
			return "context_canceled", false
		}
		return "", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if status == 0 {
			return "deadline_exceeded", true
		}
		return "", true
	}

	if status == 0 {
		return "transport_error", true
	}
	return "", true
}

func summaryOf(m *metrics, key string) (count int64, errors int64, totalBytes int64, p95 time.Duration, p99 time.Duration, codes map[int]int64, zeroKinds map[string]int64) {
	st, ok := m.endpoints[key]
	if !ok {
		return 0, 0, 0, 0, 0, map[int]int64{}, map[string]int64{}
	}
	return st.summary()
}

func printReport(cfg config, m *metrics, elapsed time.Duration) {
	fmt.Println("\n================ I/O Stress Report ================")
	fmt.Printf("elapsed: %s\n", elapsed)
	fmt.Printf("target mix: read %.2f%% / write %.2f%%\n", cfg.readRatio*100.0, (1.0-cfg.readRatio)*100.0)

	keys := make([]string, 0, len(m.endpoints))
	for k := range m.endpoints {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("\n-- Endpoint p95/p99 + throughput --")
	for _, k := range keys {
		count, errors, totalBytes, p95, p99, codes, zeroKinds := summaryOf(m, k)
		throughput := 0.0
		if elapsed > 0 {
			throughput = bytesToMiB(totalBytes) / elapsed.Seconds()
		}
		fmt.Printf("%s\n", k)
		fmt.Printf("  count=%d errors=%d errorRate=%.2f%% bytes=%.2f MiB throughput=%.2f MiB/s p95=%s p99=%s\n",
			count, errors, ratioPercent(errors, count), bytesToMiB(totalBytes), throughput, p95, p99)
		printStatusCodes(codes, cfg.topCodes)
		printZeroStatusBreakdown(zeroKinds)
	}

	readOps := atomic.LoadInt64(&m.readOps)
	writeOps := atomic.LoadInt64(&m.writeOps)
	readBytes := atomic.LoadInt64(&m.readBytes)
	writeBytes := atomic.LoadInt64(&m.writeBytes)
	totalOps := readOps + writeOps
	totalBytes := readBytes + writeBytes

	fmt.Println("\n-- Overall Mix --")
	fmt.Printf("ops: read=%d (%.2f%%), write=%d (%.2f%%), total=%d\n",
		readOps, ratioPercent(readOps, totalOps), writeOps, ratioPercent(writeOps, totalOps), totalOps)
	fmt.Printf("bytes: read=%.2f MiB, write=%.2f MiB, total=%.2f MiB\n",
		bytesToMiB(readBytes), bytesToMiB(writeBytes), bytesToMiB(totalBytes))
	if elapsed > 0 {
		fmt.Printf("aggregate throughput: %.2f MiB/s\n", bytesToMiB(totalBytes)/elapsed.Seconds())
	}

	fmt.Println("\n-- External Checks To Run In Parallel --")
	fmt.Println("iostat -x 1 : check %util, await, avgqu-sz")
	fmt.Println("vmstat 1    : check wa(iowait) trend")
	fmt.Println("docker stats: check Block I/O growth + CPU wait symptoms")
	fmt.Println("===================================================")
}

func printStatusCodes(codes map[int]int64, limit int) {
	type kv struct {
		k int
		v int64
	}
	items := make([]kv, 0, len(codes))
	for k, v := range codes {
		items = append(items, kv{k: k, v: v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v == items[j].v {
			return items[i].k < items[j].k
		}
		return items[i].v > items[j].v
	})

	if len(items) == 0 {
		fmt.Println("  status codes: (none)")
		return
	}
	if limit > len(items) {
		limit = len(items)
	}

	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, fmt.Sprintf("%d:%d", items[i].k, items[i].v))
	}
	fmt.Printf("  status codes: %s\n", strings.Join(parts, ", "))
}

func printZeroStatusBreakdown(zeroKinds map[string]int64) {
	total := int64(0)
	for _, v := range zeroKinds {
		total += v
	}
	if total == 0 {
		return
	}

	type kv struct {
		k string
		v int64
	}
	items := make([]kv, 0, len(zeroKinds))
	for k, v := range zeroKinds {
		items = append(items, kv{k: k, v: v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v == items[j].v {
			return items[i].k < items[j].k
		}
		return items[i].v > items[j].v
	})

	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s:%d", item.k, item.v))
	}
	fmt.Printf("  status=0 breakdown: %s\n", strings.Join(parts, ", "))
}
