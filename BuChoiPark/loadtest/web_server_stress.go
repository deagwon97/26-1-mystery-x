package main

import (
	"errors"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
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
	statusCode map[int]int64
}

func newEndpointStat() *endpointStat {
	return &endpointStat{statusCode: make(map[int]int64)}
}

func (s *endpointStat) add(latency time.Duration, status int, isError bool) {
	s.mu.Lock()
	s.latencies = append(s.latencies, latency)
	s.statusCode[status]++
	s.mu.Unlock()
	atomic.AddInt64(&s.count, 1)
	if isError {
		atomic.AddInt64(&s.errors, 1)
	}
}

func (s *endpointStat) summary() (count int64, errors int64, p95 time.Duration, p99 time.Duration, codes map[int]int64) {
	count = atomic.LoadInt64(&s.count)
	errors = atomic.LoadInt64(&s.errors)

	s.mu.Lock()
	defer s.mu.Unlock()

	codes = make(map[int]int64, len(s.statusCode))
	for k, v := range s.statusCode {
		codes[k] = v
	}

	if len(s.latencies) == 0 {
		return count, errors, 0, 0, codes
	}

	copied := append([]time.Duration(nil), s.latencies...)
	sort.Slice(copied, func(i, j int) bool { return copied[i] < copied[j] })

	p95 = percentile(copied, 95)
	p99 = percentile(copied, 99)
	return count, errors, p95, p99, codes
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
	endpoints         map[string]*endpointStat
	downloadCanceled int64
}

func newMetrics(keys ...string) *metrics {
	m := &metrics{endpoints: make(map[string]*endpointStat, len(keys))}
	for _, k := range keys {
		m.endpoints[k] = newEndpointStat()
	}
	return m
}

func (m *metrics) add(endpoint string, latency time.Duration, status int, isError bool) {
	if st, ok := m.endpoints[endpoint]; ok {
		st.add(latency, status, isError)
	}
}

func (m *metrics) addDownloadCanceled() {
	atomic.AddInt64(&m.downloadCanceled, 1)
}

type config struct {
	baseURL string
	stages  string

	prepareUserID   string
	prepareCount    int
	prepareSizeMB   int
	prepareTimeout  time.Duration
	skipPrepare     bool
	existingFileIDs string

	downloadTimeout time.Duration
	probeTimeout    time.Duration

	slowChunkKB int
	slowSleep   time.Duration

	probeWorkers  int
	probeInterval time.Duration

	baselineDuration         time.Duration
	allowedP95Multiplier     float64
	probeErrorRateThreshold  float64
	reportTopStatusCodesSize int
	seed                     int64
}

func parseFlags() config {
	cfg := config{}
	defaultBaseURL := os.Getenv("TARGET_BASE_URL")
	if defaultBaseURL == "" {
		defaultBaseURL = "http://localhost:8080"
	}

	flag.StringVar(&cfg.baseURL, "base-url", defaultBaseURL, "Target base URL")
	flag.StringVar(&cfg.stages, "stages", "100:3m,200:3m,300:3m", "Stage spec: concurrency:duration comma-separated")
	flag.StringVar(&cfg.prepareUserID, "prepare-user", "slow-client-user", "userId used for prepare/upload/list")
	flag.IntVar(&cfg.prepareCount, "prepare-count", 6, "Number of large files to upload before stress")
	flag.IntVar(&cfg.prepareSizeMB, "prepare-size-mb", 100, "Size (MB) for each prepared file")
	flag.DurationVar(&cfg.prepareTimeout, "prepare-timeout", 5*time.Minute, "Timeout for each prepare upload")
	flag.BoolVar(&cfg.skipPrepare, "skip-prepare", false, "Skip prepare uploads and use existing file IDs")
	flag.StringVar(&cfg.existingFileIDs, "file-ids", "", "Comma-separated existing file IDs used when -skip-prepare=true")

	flag.DurationVar(&cfg.downloadTimeout, "download-timeout", 0, "HTTP timeout for download requests (0 means no timeout)")
	flag.DurationVar(&cfg.probeTimeout, "probe-timeout", 10*time.Second, "HTTP timeout for /health and /files probes")
	flag.IntVar(&cfg.slowChunkKB, "slow-chunk-kb", 64, "Read chunk size in KB for slow download client")
	flag.DurationVar(&cfg.slowSleep, "slow-sleep", 100*time.Millisecond, "Sleep duration after each chunk read")
	flag.IntVar(&cfg.probeWorkers, "probe-workers", 2, "Concurrent lightweight probe workers")
	flag.DurationVar(&cfg.probeInterval, "probe-interval", 500*time.Millisecond, "Interval between probe requests per worker")

	flag.DurationVar(&cfg.baselineDuration, "baseline", 30*time.Second, "Baseline probe-only duration before download stress")
	flag.Float64Var(&cfg.allowedP95Multiplier, "p95-multiplier", 3.0, "Allowed p95 multiplier for probes under download stress")
	flag.Float64Var(&cfg.probeErrorRateThreshold, "probe-error-threshold", 5.0, "Allowed probe error rate percent")
	flag.IntVar(&cfg.reportTopStatusCodesSize, "top-codes", 8, "Max status codes printed per endpoint")
	flag.Int64Var(&cfg.seed, "seed", time.Now().UnixNano(), "Random seed")
	flag.Parse()

	if cfg.prepareCount <= 0 {
		panic("prepare-count must be > 0")
	}
	if cfg.prepareSizeMB <= 0 {
		panic("prepare-size-mb must be > 0")
	}
	if cfg.slowChunkKB <= 0 {
		panic("slow-chunk-kb must be > 0")
	}
	if cfg.slowSleep < 0 {
		panic("slow-sleep must be >= 0")
	}
	if cfg.probeWorkers < 0 {
		panic("probe-workers must be >= 0")
	}
	if cfg.probeInterval <= 0 {
		panic("probe-interval must be > 0")
	}
	if cfg.baselineDuration < 0 {
		panic("baseline must be >= 0")
	}
	if cfg.allowedP95Multiplier <= 0 {
		panic("p95-multiplier must be > 0")
	}
	if cfg.probeErrorRateThreshold < 0 {
		panic("probe-error-threshold must be >= 0")
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

func main() {
	cfg := parseFlags()
	stages, err := parseStages(cfg.stages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stage parse error: %v\n", err)
		os.Exit(2)
	}

	randSrc := rand.New(rand.NewSource(cfg.seed))
	fmt.Printf("Web server thread starvation stress started\n")
	fmt.Printf("baseURL=%s stages=%s seed=%d\n", cfg.baseURL, cfg.stages, cfg.seed)
	fmt.Printf("prepare: skip=%v count=%d sizeMB=%d userId=%s\n", cfg.skipPrepare, cfg.prepareCount, cfg.prepareSizeMB, cfg.prepareUserID)
	fmt.Printf("slow-client: chunk=%dKB sleep=%s, probe: workers=%d interval=%s baseline=%s\n",
		cfg.slowChunkKB, cfg.slowSleep, cfg.probeWorkers, cfg.probeInterval, cfg.baselineDuration)

	downloadClient := &http.Client{Timeout: cfg.downloadTimeout}
	probeClient := &http.Client{Timeout: cfg.probeTimeout}

	fileIDs := make([]string, 0)
	if cfg.skipPrepare {
		for _, token := range strings.Split(cfg.existingFileIDs, ",") {
			id := strings.TrimSpace(token)
			if id != "" {
				fileIDs = append(fileIDs, id)
			}
		}
	} else {
		fileIDs, err = prepareLargeFiles(cfg, downloadClient, randSrc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "prepare failed: %v\n", err)
			os.Exit(1)
		}
	}

	if len(fileIDs) == 0 {
		fmt.Fprintln(os.Stderr, "no file IDs available")
		os.Exit(1)
	}
	fmt.Printf("prepared file IDs: %s\n", strings.Join(fileIDs, ","))

	probeEndpoints := []string{"GET /health", "GET /files"}
	baselineMetrics := newMetrics(probeEndpoints...)
	stressMetrics := newMetrics("GET /files/{id}/download", "GET /health", "GET /files")

	if cfg.baselineDuration > 0 {
		fmt.Printf("baseline probes start: duration=%s\n", cfg.baselineDuration)
		runProbePhase(cfg.baselineDuration, cfg, probeClient, baselineMetrics)
	}

	fmt.Printf("download stress start\n")
	runStress(stages, cfg, downloadClient, probeClient, fileIDs, stressMetrics)

	printReport(cfg, baselineMetrics, stressMetrics)
}

func prepareLargeFiles(cfg config, client *http.Client, r *rand.Rand) ([]string, error) {
	out := make([]string, 0, cfg.prepareCount)
	sizeBytes := int64(cfg.prepareSizeMB) * 1024 * 1024

	for i := 0; i < cfg.prepareCount; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.prepareTimeout)
		filePath := fmt.Sprintf("/thread-test/%02d/f-%d.bin", i+1, r.Int63())
		id, status, body, err := uploadFile(ctx, client, cfg.baseURL, cfg.prepareUserID, filePath, sizeBytes)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("prepare upload failed idx=%d err=%w", i+1, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("prepare upload failed idx=%d status=%d body=%s", i+1, status, string(body))
		}
		if id == "" {
			return nil, fmt.Errorf("prepare upload returned empty id idx=%d status=%d body=%s", i+1, status, string(body))
		}
		fmt.Printf("prepared %d/%d: id=%s path=%s\n", i+1, cfg.prepareCount, id, filePath)
		out = append(out, id)
	}
	return out, nil
}

func uploadFile(ctx context.Context, client *http.Client, baseURL, userID, filePath string, sizeBytes int64) (string, int, []byte, error) {
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
		return "", 0, nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	var parsed uploadResponse
	_ = json.Unmarshal(body, &parsed)
	return parsed.ID, resp.StatusCode, body, nil
}

func runProbePhase(duration time.Duration, cfg config, client *http.Client, m *metrics) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var wg sync.WaitGroup

	for i := 0; i < cfg.probeWorkers; i++ {
		wg.Add(1)
		go func(seedOffset int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(7000+seedOffset*97)))
			ticker := time.NewTicker(cfg.probeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					doProbe(ctx, client, cfg.baseURL, cfg.prepareUserID, r, m)
				}
			}
		}(i)
	}
	wg.Wait()
}

func runStress(stages []stage, cfg config, downloadClient, probeClient *http.Client, fileIDs []string, m *metrics) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var probeWG sync.WaitGroup
	for i := 0; i < cfg.probeWorkers; i++ {
		probeWG.Add(1)
		go func(seedOffset int) {
			defer probeWG.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(9000+seedOffset*131)))
			ticker := time.NewTicker(cfg.probeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					doProbe(ctx, probeClient, cfg.baseURL, cfg.prepareUserID, r, m)
				}
			}
		}(i)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	var workerWG sync.WaitGroup
	workerStops := make([]context.CancelFunc, 0)
	spawnWorker := func(id int) {
		wctx, wcancel := context.WithCancel(workerCtx)
		workerStops = append(workerStops, wcancel)
		workerWG.Add(1)
		go func(workerID int, localCtx context.Context) {
			defer workerWG.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(1000+workerID*7919)))
			chunkSize := cfg.slowChunkKB * 1024
			for {
				select {
				case <-localCtx.Done():
					return
				default:
				}
				picked := fileIDs[r.Intn(len(fileIDs))]
				status, latency, err := doSlowDownload(localCtx, downloadClient, cfg.baseURL, picked, chunkSize, cfg.slowSleep)
				isErr, wasCanceled := classifyDownloadResult(status, err)
				if wasCanceled {
					m.addDownloadCanceled()
				}
				m.add("GET /files/{id}/download", latency, status, isErr)
			}
		}(id, wctx)
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
		fmt.Printf("stage: slow-download concurrency=%d duration=%s\n", st.Concurrency, st.Duration)
		select {
		case <-ctx.Done():
			break
		case <-time.After(st.Duration):
		}
	}

	workerCancel()
	setWorkers(0)
	workerWG.Wait()
	cancel()
	probeWG.Wait()
}

func doProbe(ctx context.Context, client *http.Client, baseURL, userID string, r *rand.Rand, m *metrics) {
	if r.Intn(2) == 0 {
		endpoint := "GET /health"
		status, _, latency := doRequest(ctx, client, http.MethodGet, baseURL+"/health", "", nil, 8192)
		isErr := status == 0 || status >= 500
		m.add(endpoint, latency, status, isErr)
		return
	}

	v := url.Values{}
	v.Set("userId", userID)
	endpoint := "GET /files"
	status, _, latency := doRequest(ctx, client, http.MethodGet, baseURL+"/files?"+v.Encode(), "", nil, 64*1024)
	isErr := status == 0 || status >= 500
	m.add(endpoint, latency, status, isErr)
}

func doSlowDownload(ctx context.Context, client *http.Client, baseURL, id string, chunkSize int, sleep time.Duration) (int, time.Duration, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/files/"+id+"/download", nil)
	if err != nil {
		return 0, 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		return resp.StatusCode, time.Since(start), nil
	}

	buf := make([]byte, chunkSize)
	for {
		_, readErr := resp.Body.Read(buf)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return resp.StatusCode, time.Since(start), readErr
		}
		if sleep > 0 {
			select {
			case <-ctx.Done():
				return resp.StatusCode, time.Since(start), ctx.Err()
			case <-time.After(sleep):
			}
		}
	}
	return resp.StatusCode, time.Since(start), nil
}

func doRequest(ctx context.Context, client *http.Client, method, targetURL, contentType string, body io.Reader, readLimit int64) (int, []byte, time.Duration) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return 0, []byte(err.Error()), 0
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, []byte(err.Error()), time.Since(start)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	return resp.StatusCode, respBody, time.Since(start)
}

func classifyDownloadResult(status int, err error) (isError bool, wasCanceled bool) {
	if err == nil {
		return status == 0 || status >= 500, false
	}

	if errors.Is(err, context.Canceled) {
		return false, true
	}

	return true, false
}

func ratioPercent(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100.0 / float64(b)
}

func summaryOf(m *metrics, key string) (count int64, errors int64, p95 time.Duration, p99 time.Duration, codes map[int]int64) {
	st, ok := m.endpoints[key]
	if !ok {
		return 0, 0, 0, 0, map[int]int64{}
	}
	return st.summary()
}

func printReport(cfg config, baseline, stress *metrics) {
	fmt.Println("\n================ Web Server Stress Report ================")
	fmt.Println("focus: slow download clients + lightweight API probe impact")

	keys := make([]string, 0, len(stress.endpoints))
	for k := range stress.endpoints {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("\n-- Stress Endpoint p95/p99 --")
	for _, k := range keys {
		count, errors, p95, p99, codes := summaryOf(stress, k)
		fmt.Printf("%s\n", k)
		fmt.Printf("  count=%d errors=%d errorRate=%.2f%% p95=%s p99=%s\n", count, errors, ratioPercent(errors, count), p95, p99)
		printStatusCodes(codes, cfg.reportTopStatusCodesSize)
	}

	fmt.Println("\n-- Baseline vs Stress Probe p95 --")
	probeKeys := []string{"GET /health", "GET /files"}
	degradePass := true
	errorPass := true
	for _, k := range probeKeys {
		bCount, _, bP95, _, _ := summaryOf(baseline, k)
		sCount, sErr, sP95, _, _ := summaryOf(stress, k)

		multiplier := math.Inf(1)
		if bP95 > 0 {
			multiplier = float64(sP95) / float64(bP95)
		}
		if bCount > 0 && !math.IsInf(multiplier, 1) && multiplier > cfg.allowedP95Multiplier {
			degradePass = false
		}

		errRate := ratioPercent(sErr, sCount)
		if sCount > 0 && errRate > cfg.probeErrorRateThreshold {
			errorPass = false
		}

		fmt.Printf("%s\n", k)
		fmt.Printf("  baseline count=%d p95=%s\n", bCount, bP95)
		fmt.Printf("  stress   count=%d p95=%s multiplier=", sCount, sP95)
		if math.IsInf(multiplier, 1) {
			fmt.Printf("INF")
		} else {
			fmt.Printf("%.2fx", multiplier)
		}
		fmt.Printf(" errorRate=%.2f%%\n", errRate)
	}

	dCount, dErrRaw, _, _, dCodes := summaryOf(stress, "GET /files/{id}/download")
	dCanceled := atomic.LoadInt64(&stress.downloadCanceled)
	dErr := dErrRaw
	if dErrRaw >= dCanceled {
		dErr = dErrRaw - dCanceled
	}
	d503 := dCodes[503]
	dConnResetLike := dCodes[0]

	fmt.Println("\n-- Thread Starvation Signals (from API outcomes) --")
	fmt.Printf("download requests=%d errors=%d errorRate=%.2f%%\n", dCount, dErr, ratioPercent(dErr, dCount))
	fmt.Printf("download canceled(context canceled)=%d (excluded from download errors)\n", dCanceled)
	fmt.Printf("download status503=%d transportErrors(status=0)=%d\n", d503, dConnResetLike)

	fmt.Println("\n-- Success Gate (memo intent) --")
	fmt.Printf("probe p95 multiplier <= %.2fx: %v\n", cfg.allowedP95Multiplier, degradePass)
	fmt.Printf("probe error rate <= %.2f%%  : %v\n", cfg.probeErrorRateThreshold, errorPass)
	fmt.Printf("overall gate pass          : %v\n", degradePass && errorPass)

	fmt.Println("\n-- External Checks To Run In Parallel --")
	fmt.Println("Tomcat metrics: threads.busy near max, queued requests increase")
	fmt.Println("OS socket view : ss -s (ESTABLISHED surge / backlog symptoms)")
	fmt.Println("App errors     : timeout / 503 / connection reset")
	fmt.Println("============================================================")
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
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}

	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, fmt.Sprintf("%d:%d", items[i].k, items[i].v))
	}
	fmt.Printf("  status codes: %s\n", strings.Join(parts, ", "))
}
