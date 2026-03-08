package main

import (
	"bytes"
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
	endpoints map[string]*endpointStat

	writeTotal     int64
	writeFailures  int64
	readTotal      int64
	readFailures   int64
	lockKeywordHit int64
}

func newMetrics() *metrics {
	return &metrics{
		endpoints: map[string]*endpointStat{
			"POST /files/upload":      newEndpointStat(),
			"POST /files/move-folder": newEndpointStat(),
			"DELETE /files":           newEndpointStat(),
			"DELETE /files/folder":    newEndpointStat(),
			"GET /files":              newEndpointStat(),
			"GET /files/folder":       newEndpointStat(),
		},
	}
}

type pathStore struct {
	mu         sync.Mutex
	byUser     map[string][]string
	byUserFold map[string][]string
}

func newPathStore() *pathStore {
	return &pathStore{
		byUser:     make(map[string][]string),
		byUserFold: make(map[string][]string),
	}
}

func (s *pathStore) add(userID, filePath string) {
	folder := folderOf(filePath)
	s.mu.Lock()
	s.byUser[userID] = append(s.byUser[userID], filePath)
	if folder != "" {
		s.byUserFold[userID] = append(s.byUserFold[userID], folder)
	}
	s.mu.Unlock()
}

func (s *pathStore) popRandomFile(userID string, r *rand.Rand) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.byUser[userID]
	if len(list) == 0 {
		return "", false
	}
	idx := r.Intn(len(list))
	picked := list[idx]
	last := len(list) - 1
	list[idx] = list[last]
	s.byUser[userID] = list[:last]
	return picked, true
}

func (s *pathStore) randomFolder(userID string, r *rand.Rand) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.byUserFold[userID]
	if len(list) == 0 {
		return "", false
	}
	return list[r.Intn(len(list))], true
}

func folderOf(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return ""
	}
	return path[:idx]
}

type config struct {
	baseURL               string
	stageSpec             string
	readRatio             float64
	minKB                 int
	maxKB                 int
	userCount             int
	requestTimeout        time.Duration
	mixWorkers            int
	mixInterval           time.Duration
	seed                  int64
	reportTopStatusCodesN int
}

func parseFlags() config {
	cfg := config{}
	defaultBaseURL := os.Getenv("TARGET_BASE_URL")
	if defaultBaseURL == "" {
		defaultBaseURL = "http://localhost:8080"
	}

	flag.StringVar(&cfg.baseURL, "base-url", defaultBaseURL, "Target base URL")
	flag.StringVar(&cfg.stageSpec, "stages", "50:2m,100:2m,150:2m", "Stage spec: concurrency:duration comma-separated")
	flag.Float64Var(&cfg.readRatio, "read-ratio", 0.20, "Read ratio (0~1). Write ratio is 1-read-ratio")
	flag.IntVar(&cfg.minKB, "min-kb", 100, "Minimum upload payload size in KB")
	flag.IntVar(&cfg.maxKB, "max-kb", 1024, "Maximum upload payload size in KB")
	flag.IntVar(&cfg.userCount, "users", 200, "Number of users to distribute requests across")
	flag.DurationVar(&cfg.requestTimeout, "timeout", 20*time.Second, "Per-request timeout")
	flag.IntVar(&cfg.mixWorkers, "mix-workers", 4, "Periodic writer workers for delete/move-folder mix")
	flag.DurationVar(&cfg.mixInterval, "mix-interval", 500*time.Millisecond, "Interval between periodic mix operations")
	flag.Int64Var(&cfg.seed, "seed", time.Now().UnixNano(), "Random seed")
	flag.IntVar(&cfg.reportTopStatusCodesN, "top-codes", 8, "Max status codes printed per endpoint")
	flag.Parse()

	if cfg.readRatio < 0 || cfg.readRatio > 1 {
		panic("read-ratio must be between 0 and 1")
	}
	if cfg.minKB <= 0 || cfg.maxKB < cfg.minKB {
		panic("invalid min-kb/max-kb")
	}
	if cfg.userCount <= 0 {
		panic("users must be > 0")
	}
	if cfg.mixWorkers < 0 {
		panic("mix-workers must be >= 0")
	}
	if cfg.mixInterval <= 0 {
		panic("mix-interval must be > 0")
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

func main() {
	cfg := parseFlags()
	stages, err := parseStages(cfg.stageSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stage parse error: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("DB lock stress test started\n")
	fmt.Printf("baseURL=%s stages=%s readRatio=%.2f writeRatio=%.2f users=%d payload=%dKB~%dKB seed=%d\n",
		cfg.baseURL, cfg.stageSpec, cfg.readRatio, 1-cfg.readRatio, cfg.userCount, cfg.minKB, cfg.maxKB, cfg.seed)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newMetrics()
	store := newPathStore()
	client := &http.Client{Timeout: cfg.requestTimeout}

	userIDs := make([]string, cfg.userCount)
	for i := 0; i < cfg.userCount; i++ {
		userIDs[i] = fmt.Sprintf("load-user-%03d", i+1)
	}

	var pathCounter uint64
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
			localRand := rand.New(rand.NewSource(cfg.seed + int64(workerID)*7919))
			for {
				select {
				case <-localCtx.Done():
					return
				default:
				}

				uid := userIDs[localRand.Intn(len(userIDs))]
				if localRand.Float64() < cfg.readRatio {
					doReadRequest(localCtx, client, cfg.baseURL, uid, localRand, m)
				} else {
					seq := atomic.AddUint64(&pathCounter, 1)
					filePath := makeFilePath(uid, seq, localRand)
					sizeKB := cfg.minKB + localRand.Intn(cfg.maxKB-cfg.minKB+1)
					doUpload(localCtx, client, cfg.baseURL, uid, filePath, sizeKB*1024, m)
					store.add(uid, filePath)
				}
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

	var mixWG sync.WaitGroup
	for i := 0; i < cfg.mixWorkers; i++ {
		mixWG.Add(1)
		go func(workerID int) {
			defer mixWG.Done()
			localRand := rand.New(rand.NewSource(cfg.seed + int64(workerID+10000)*3571))
			ticker := time.NewTicker(cfg.mixInterval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return
				case <-ticker.C:
					uid := userIDs[localRand.Intn(len(userIDs))]
					doPeriodicWrite(workerCtx, client, cfg.baseURL, uid, localRand, store, m)
				}
			}
		}(i + 1)
	}

	for _, st := range stages {
		setWorkers(st.Concurrency)
		fmt.Printf("stage: concurrency=%d duration=%s\n", st.Concurrency, st.Duration)
		select {
		case <-ctx.Done():
			break
		case <-time.After(st.Duration):
		}
	}

	workerCancel()
	setWorkers(0)
	workerWG.Wait()
	mixWG.Wait()

	printReport(m, cfg)
}

func makeFilePath(userID string, seq uint64, r *rand.Rand) string {
	folderA := r.Intn(30)
	folderB := r.Intn(100)
	name := fmt.Sprintf("f-%s-%d-%d.bin", strings.ReplaceAll(userID, "-", ""), seq, r.Intn(1000000))
	return fmt.Sprintf("/lt/%02d/%03d/%s", folderA, folderB, name)
}

func doReadRequest(ctx context.Context, client *http.Client, baseURL, userID string, r *rand.Rand, m *metrics) {
	if r.Intn(2) == 0 {
		v := url.Values{}
		v.Set("userId", userID)
		endpoint := "GET /files"
		status, body, latency := doRequest(ctx, client, http.MethodGet, baseURL+"/files?"+v.Encode(), "", nil)
		isErr := status >= 500 || status == 0
		record(endpoint, status, body, latency, isErr, false, m)
		return
	}

	folder := fmt.Sprintf("/lt/%02d", r.Intn(30))
	v := url.Values{}
	v.Set("userId", userID)
	v.Set("folderPath", folder)
	endpoint := "GET /files/folder"
	status, body, latency := doRequest(ctx, client, http.MethodGet, baseURL+"/files/folder?"+v.Encode(), "", nil)
	isErr := status >= 500 || status == 0
	record(endpoint, status, body, latency, isErr, false, m)
}

func doPeriodicWrite(ctx context.Context, client *http.Client, baseURL, userID string, r *rand.Rand, store *pathStore, m *metrics) {
	choice := r.Float64()
	if choice < 0.40 {
		if filePath, ok := store.popRandomFile(userID, r); ok {
			v := url.Values{}
			v.Set("userId", userID)
			v.Set("filePath", filePath)
			endpoint := "DELETE /files"
			status, body, latency := doRequest(ctx, client, http.MethodDelete, baseURL+"/files?"+v.Encode(), "", nil)
			isErr := (status >= 500 || status == 0)
			record(endpoint, status, body, latency, isErr, true, m)
			return
		}
	}

	if choice < 0.70 {
		folder := fmt.Sprintf("/lt/%02d", r.Intn(30))
		if picked, ok := store.randomFolder(userID, r); ok && r.Intn(2) == 0 {
			folder = picked
		}
		v := url.Values{}
		v.Set("userId", userID)
		v.Set("folderPath", folder)
		endpoint := "DELETE /files/folder"
		status, body, latency := doRequest(ctx, client, http.MethodDelete, baseURL+"/files/folder?"+v.Encode(), "", nil)
		isErr := status >= 500 || status == 0
		record(endpoint, status, body, latency, isErr, true, m)
		return
	}

	fromPath := fmt.Sprintf("/lt/%02d", r.Intn(30))
	toPath := fmt.Sprintf("/lt-moved/%02d", r.Intn(30))
	if fromPath == toPath {
		toPath += "-x"
	}
	payload := map[string]string{"fromPath": fromPath, "toPath": toPath}
	body, _ := json.Marshal(payload)
	endpoint := "POST /files/move-folder"
	status, respBody, latency := doRequest(ctx, client, http.MethodPost, baseURL+"/files/move-folder", "application/json", body)
	isErr := status >= 500 || status == 0
	record(endpoint, status, respBody, latency, isErr, true, m)
}

func doUpload(ctx context.Context, client *http.Client, baseURL, userID, filePath string, sizeBytes int, m *metrics) {
	endpoint := "POST /files/upload"

	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	_ = writer.WriteField("userId", userID)
	_ = writer.WriteField("filePath", filePath)
	part, err := writer.CreateFormFile("file", fileNameFromPath(filePath))
	if err != nil {
		record(endpoint, 0, []byte(err.Error()), 0, true, true, m)
		return
	}

	payload := bytes.Repeat([]byte{'a'}, sizeBytes)
	if _, err = part.Write(payload); err != nil {
		record(endpoint, 0, []byte(err.Error()), 0, true, true, m)
		return
	}
	_ = writer.Close()

	status, body, latency := doRequest(ctx, client, http.MethodPost, baseURL+"/files/upload", writer.FormDataContentType(), b.Bytes())
	isErr := status >= 500 || status == 0
	record(endpoint, status, body, latency, isErr, true, m)
}

func fileNameFromPath(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 || idx == len(path)-1 {
		return "payload.bin"
	}
	return path[idx+1:]
}

func doRequest(ctx context.Context, client *http.Client, method, targetURL, contentType string, body []byte) (int, []byte, time.Duration) {
	start := time.Now()
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reqBody)
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

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, respBody, time.Since(start)
}

func record(endpoint string, status int, body []byte, latency time.Duration, isError bool, isWrite bool, m *metrics) {
	if st, ok := m.endpoints[endpoint]; ok {
		st.add(latency, status, isError)
	}
	bodyLower := strings.ToLower(string(body))
	if strings.Contains(bodyLower, "database is locked") || strings.Contains(bodyLower, "sqlite_busy") || strings.Contains(bodyLower, "transaction timeout") {
		atomic.AddInt64(&m.lockKeywordHit, 1)
	}

	if isWrite {
		atomic.AddInt64(&m.writeTotal, 1)
		if isError {
			atomic.AddInt64(&m.writeFailures, 1)
		}
	} else {
		atomic.AddInt64(&m.readTotal, 1)
		if isError {
			atomic.AddInt64(&m.readFailures, 1)
		}
	}
}

func printReport(m *metrics, cfg config) {
	fmt.Println("\n================ Load Test Report ================")
	writeTotal := atomic.LoadInt64(&m.writeTotal)
	writeFailures := atomic.LoadInt64(&m.writeFailures)
	readTotal := atomic.LoadInt64(&m.readTotal)
	readFailures := atomic.LoadInt64(&m.readFailures)
	lockHits := atomic.LoadInt64(&m.lockKeywordHit)

	total := writeTotal + readTotal
	actualReadRatio := 0.0
	if total > 0 {
		actualReadRatio = float64(readTotal) / float64(total)
	}
	actualWriteRatio := 1.0 - actualReadRatio

	fmt.Printf("total requests   : %d\n", total)
	fmt.Printf("write requests   : %d (failures=%d, error rate=%.2f%%)\n", writeTotal, writeFailures, ratioPercent(writeFailures, writeTotal))
	fmt.Printf("read requests    : %d (failures=%d, error rate=%.2f%%)\n", readTotal, readFailures, ratioPercent(readFailures, readTotal))
	fmt.Printf("actual R/W ratio : read %.2f%% / write %.2f%% (target read %.2f%% / write %.2f%%)\n",
		actualReadRatio*100, actualWriteRatio*100, cfg.readRatio*100, (1-cfg.readRatio)*100)
	fmt.Printf("lock keyword hit : %d (body contains 'database is locked'|'SQLITE_BUSY'|'transaction timeout')\n", lockHits)

	keys := make([]string, 0, len(m.endpoints))
	for k := range m.endpoints {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("\n-- Endpoint p95/p99 --")
	for _, k := range keys {
		count, errors, p95, p99, codes := m.endpoints[k].summary()
		fmt.Printf("%s\n", k)
		fmt.Printf("  count=%d errors=%d errorRate=%.2f%% p95=%s p99=%s\n", count, errors, ratioPercent(errors, count), p95, p99)
		printStatusCodes(codes, cfg.reportTopStatusCodesN)
	}

	fmt.Println("\n-- Success Gate (example from memo) --")
	if writeTotal == 0 {
		fmt.Println("  write API count is 0; cannot evaluate")
	} else {
		wr := ratioPercent(writeFailures, writeTotal)
		pass := wr < 1.0 && lockHits == 0
		fmt.Printf("  write API error rate < 1%%: %v (%.2f%%)\n", wr < 1.0, wr)
		fmt.Printf("  lock keyword near-zero : %v (count=%d)\n", lockHits == 0, lockHits)
		fmt.Printf("  overall gate pass      : %v\n", pass)
	}
	fmt.Println("==================================================")
}

func ratioPercent(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100.0 / float64(b)
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
