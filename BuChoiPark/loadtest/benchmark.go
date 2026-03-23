package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── 공통 타입 ────────────────────────────────────────────────────────────────

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
}

func newEndpointStat() *endpointStat {
	return &endpointStat{statusCode: make(map[int]int64)}
}

func (s *endpointStat) record(latency time.Duration, status int, payloadBytes int64, isError bool) {
	s.mu.Lock()
	s.latencies = append(s.latencies, latency)
	s.statusCode[status]++
	s.mu.Unlock()
	atomic.AddInt64(&s.count, 1)
	atomic.AddInt64(&s.bytes, payloadBytes)
	if isError {
		atomic.AddInt64(&s.errors, 1)
	}
}

func (s *endpointStat) summary() (count, errs, totalBytes int64, p95, p99 time.Duration, codes map[int]int64) {
	count = atomic.LoadInt64(&s.count)
	errs = atomic.LoadInt64(&s.errors)
	totalBytes = atomic.LoadInt64(&s.bytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	codes = make(map[int]int64, len(s.statusCode))
	for k, v := range s.statusCode {
		codes[k] = v
	}
	if len(s.latencies) == 0 {
		return
	}
	cp := append([]time.Duration(nil), s.latencies...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	p95 = pct(cp, 95)
	p99 = pct(cp, 99)
	return
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type metrics struct {
	mu        sync.Mutex
	endpoints map[string]*endpointStat
}

func newMetrics() *metrics {
	return &metrics{endpoints: make(map[string]*endpointStat)}
}

func (m *metrics) get(key string) *endpointStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.endpoints[key]; ok {
		return s
	}
	s := newEndpointStat()
	m.endpoints[key] = s
	return s
}

func (m *metrics) add(key string, latency time.Duration, status int, payloadBytes int64, isError bool) {
	m.get(key).record(latency, status, payloadBytes, isError)
}

// ─── 유틸리티 ─────────────────────────────────────────────────────────────────

func parseStages(spec string) ([]stage, error) {
	var out []stage
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tok := strings.SplitN(part, ":", 2)
		if len(tok) != 2 {
			return nil, fmt.Errorf("잘못된 stage 형식: %q", part)
		}
		c, err := strconv.Atoi(strings.TrimSpace(tok[0]))
		if err != nil || c <= 0 {
			return nil, fmt.Errorf("잘못된 동시성: %q", part)
		}
		d, err := time.ParseDuration(strings.TrimSpace(tok[1]))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("잘못된 시간: %q", part)
		}
		out = append(out, stage{c, d})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("stage가 없음")
	}
	return out, nil
}

func ratioPercent(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100.0 / float64(b)
}

func bytesToMiB(v int64) float64 { return float64(v) / (1024 * 1024) }

func isHTTPError(status int, err error) bool {
	if err != nil {
		return !errors.Is(err, context.Canceled)
	}
	return status == 0 || status >= 500
}

// ─── 결정론적 콘텐츠 (무결성 검증용) ──────────────────────────────────────────

const blockSize = 4096

func makeBlock(seed int64) []byte {
	block := make([]byte, blockSize)
	r := rand.New(rand.NewSource(seed))
	for i := range block {
		block[i] = byte(r.Intn(256))
	}
	return block
}

func checksumOfBlock(block []byte, size int64) string {
	h := sha256.New()
	remaining := size
	for remaining > 0 {
		n := int64(len(block))
		if n > remaining {
			n = remaining
		}
		h.Write(block[:n])
		remaining -= n
	}
	return hex.EncodeToString(h.Sum(nil))
}

type repeatingReader struct {
	block  []byte
	size   int64
	read   int64
	offset int
}

func newRepeatingReader(block []byte, size int64) *repeatingReader {
	return &repeatingReader{block: block, size: size}
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.read >= r.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && r.read < r.size {
		avail := int64(len(r.block) - r.offset)
		want := int64(len(p) - n)
		if want > r.size-r.read {
			want = r.size - r.read
		}
		if want > avail {
			want = avail
		}
		copy(p[n:], r.block[r.offset:r.offset+int(want)])
		n += int(want)
		r.read += want
		r.offset = (r.offset + int(want)) % len(r.block)
	}
	if r.read >= r.size {
		return n, io.EOF
	}
	return n, nil
}

// 성능 테스트용 무한 reader (항상 'a' 반환)
type endlessByteReader struct{}

func (endlessByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

// ─── HTTP 헬퍼 ────────────────────────────────────────────────────────────────

type uploadResponse struct {
	ID string `json:"id"`
}

func uploadMultipart(ctx context.Context, client *http.Client, baseURL, userID, filePath string, body io.Reader, sizeBytes int64) (id string, status int, latency time.Duration, err error) {
	start := time.Now()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		defer mw.Close()
		if e := mw.WriteField("userId", userID); e != nil {
			pw.CloseWithError(e)
			return
		}
		if e := mw.WriteField("filePath", filePath); e != nil {
			pw.CloseWithError(e)
			return
		}
		part, e := mw.CreateFormFile("file", path.Base(filePath))
		if e != nil {
			pw.CloseWithError(e)
			return
		}
		if _, e = io.CopyN(part, body, sizeBytes); e != nil {
			pw.CloseWithError(e)
		}
	}()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/files/upload", pr)
	if e != nil {
		return "", 0, 0, e
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, e := client.Do(req)
	latency = time.Since(start)
	if e != nil {
		return "", 0, latency, e
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var ur uploadResponse
	_ = json.Unmarshal(b, &ur)
	return ur.ID, resp.StatusCode, latency, nil
}

func uploadRaw(ctx context.Context, client *http.Client, baseURL, userID, filePath string, body io.Reader, sizeBytes int64) (id string, status int, latency time.Duration, err error) {
	start := time.Now()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/files/upload", io.LimitReader(body, sizeBytes))
	if e != nil {
		return "", 0, 0, e
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-File-Path", filePath)
	req.Header.Set("X-File-Name", path.Base(filePath))
	resp, e := client.Do(req)
	latency = time.Since(start)
	if e != nil {
		return "", 0, latency, e
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var ur uploadResponse
	_ = json.Unmarshal(b, &ur)
	return ur.ID, resp.StatusCode, latency, nil
}

func uploadFile(ctx context.Context, client *http.Client, cfg *config, filePath string, body io.Reader, sizeBytes int64) (string, int, time.Duration, error) {
	if cfg.uploadMode == "raw" {
		return uploadRaw(ctx, client, cfg.baseURL, cfg.userID, filePath, body, sizeBytes)
	}
	return uploadMultipart(ctx, client, cfg.baseURL, cfg.userID, filePath, body, sizeBytes)
}

func downloadDiscard(ctx context.Context, client *http.Client, baseURL, id string) (status int, received int64, latency time.Duration, err error) {
	start := time.Now()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/files/"+id+"/download", nil)
	if e != nil {
		return 0, 0, 0, e
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, 0, time.Since(start), e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		return resp.StatusCode, 0, time.Since(start), nil
	}
	n, re := io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, n, time.Since(start), re
}

func downloadAndHash(ctx context.Context, client *http.Client, baseURL, id string) (status int, checksum string, received int64, latency time.Duration, err error) {
	start := time.Now()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/files/"+id+"/download", nil)
	if e != nil {
		return 0, "", 0, 0, e
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, "", 0, time.Since(start), e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		return resp.StatusCode, "", 0, time.Since(start), nil
	}
	h := sha256.New()
	n, re := io.Copy(h, resp.Body)
	return resp.StatusCode, hex.EncodeToString(h.Sum(nil)), n, time.Since(start), re
}

func downloadSlow(ctx context.Context, client *http.Client, baseURL, id string, chunkSize int, sleep time.Duration) (status int, latency time.Duration, err error) {
	start := time.Now()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/files/"+id+"/download", nil)
	if e != nil {
		return 0, 0, e
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, time.Since(start), e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		return resp.StatusCode, time.Since(start), nil
	}
	buf := make([]byte, chunkSize)
	for {
		_, re := resp.Body.Read(buf)
		if re == io.EOF {
			break
		}
		if re != nil {
			return resp.StatusCode, time.Since(start), re
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

func doSimpleRequest(ctx context.Context, client *http.Client, method, targetURL, contentType string, body []byte) (status int, respBody []byte, latency time.Duration) {
	start := time.Now()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, r)
	if err != nil {
		return 0, nil, 0
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, []byte(err.Error()), time.Since(start)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, b, time.Since(start)
}

// ─── Config ───────────────────────────────────────────────────────────────────

type config struct {
	baseURL        string
	scenarios      []string
	uploadMode     string
	userID         string
	seed           int64
	timeout        time.Duration
	prepareTimeout time.Duration

	// 시나리오 1: 작은 파일 다수
	smallStages      string
	smallMinKB       int
	smallMaxKB       int
	smallUsers       int
	smallMixWorkers  int
	smallMixInterval time.Duration

	// 시나리오 2: 큰 파일 다수
	largeStages       string
	largeMinMB        int
	largeMaxMB        int
	largeReadRatio    float64
	largePrepareCount int
	largePrepareMB    int

	// 시나리오 3: 큰 단일 파일
	singleSizeMB int

	// 시나리오 4: 무결성 검증
	integritySizesMB   string
	integrityConcurrent int

	// 시나리오 5: 스트리밍
	streamStages        string
	streamPrepareCount  int
	streamPrepareMB     int
	streamChunkKB       int
	streamSleep         time.Duration
	streamProbeWorkers  int
	streamProbeInterval time.Duration
	streamBaseline      time.Duration
	streamP95Mult       float64
	streamErrThreshold  float64

	// 시나리오 6: 동시 동일 파일 접근
	concurrentSizeMB   int
	concurrentWorkers  int
	concurrentDuration time.Duration
}

func parseFlags() *config {
	cfg := &config{}
	defaultURL := os.Getenv("TARGET_BASE_URL")
	if defaultURL == "" {
		defaultURL = "http://localhost:8080"
	}

	scenariosRaw := flag.String("scenario", "all",
		"실행할 시나리오: all 또는 콤마 구분 (small,large,single,integrity,streaming,concurrent)")
	flag.StringVar(&cfg.baseURL, "base-url", defaultURL, "대상 서버 URL")
	flag.StringVar(&cfg.uploadMode, "upload-mode", "multipart", "업로드 방식: multipart 또는 raw")
	flag.StringVar(&cfg.userID, "user-id", "bench-user", "유저 ID 접두어")
	flag.Int64Var(&cfg.seed, "seed", time.Now().UnixNano(), "랜덤 시드")
	flag.DurationVar(&cfg.timeout, "timeout", 0, "요청 HTTP 타임아웃 (0 = 제한 없음)")
	flag.DurationVar(&cfg.prepareTimeout, "prepare-timeout", 10*time.Minute, "준비 업로드 타임아웃")

	// 시나리오 1
	flag.StringVar(&cfg.smallStages, "small-stages", "50:2m,100:2m,150:2m", "[소형] 동시성 단계")
	flag.IntVar(&cfg.smallMinKB, "small-min-kb", 100, "[소형] 최소 파일 크기 KB")
	flag.IntVar(&cfg.smallMaxKB, "small-max-kb", 1024, "[소형] 최대 파일 크기 KB")
	flag.IntVar(&cfg.smallUsers, "small-users", 100, "[소형] 가상 유저 수")
	flag.IntVar(&cfg.smallMixWorkers, "small-mix-workers", 4, "[소형] 삭제/이동 mix 워커 수")
	flag.DurationVar(&cfg.smallMixInterval, "small-mix-interval", 500*time.Millisecond, "[소형] mix 워커 간격")

	// 시나리오 2
	flag.StringVar(&cfg.largeStages, "large-stages", "10:3m,20:3m,40:3m", "[대형] 동시성 단계")
	flag.IntVar(&cfg.largeMinMB, "large-min-mb", 100, "[대형] 최소 파일 크기 MB")
	flag.IntVar(&cfg.largeMaxMB, "large-max-mb", 500, "[대형] 최대 파일 크기 MB")
	flag.Float64Var(&cfg.largeReadRatio, "large-read-ratio", 0.8, "[대형] 읽기 비율 (0~1)")
	flag.IntVar(&cfg.largePrepareCount, "large-prepare-count", 8, "[대형] 사전 업로드 파일 수")
	flag.IntVar(&cfg.largePrepareMB, "large-prepare-mb", 100, "[대형] 사전 업로드 파일 크기 MB")

	// 시나리오 3
	flag.IntVar(&cfg.singleSizeMB, "single-size-mb", 1024, "[단일] 파일 크기 MB")

	// 시나리오 4
	flag.StringVar(&cfg.integritySizesMB, "integrity-sizes-mb", "1,10,100", "[무결성] 파일 크기 목록 MB (콤마 구분)")
	flag.IntVar(&cfg.integrityConcurrent, "integrity-concurrent", 10, "[무결성] 동시 다운로드 수 per 파일")

	// 시나리오 5
	flag.StringVar(&cfg.streamStages, "stream-stages", "100:2m,200:2m,300:2m", "[스트리밍] 느린 다운로드 동시성 단계")
	flag.IntVar(&cfg.streamPrepareCount, "stream-prepare-count", 6, "[스트리밍] 사전 업로드 파일 수")
	flag.IntVar(&cfg.streamPrepareMB, "stream-prepare-mb", 100, "[스트리밍] 사전 업로드 파일 크기 MB")
	flag.IntVar(&cfg.streamChunkKB, "stream-chunk-kb", 64, "[스트리밍] 느린 클라이언트 청크 크기 KB")
	flag.DurationVar(&cfg.streamSleep, "stream-sleep", 100*time.Millisecond, "[스트리밍] 청크 간 sleep")
	flag.IntVar(&cfg.streamProbeWorkers, "stream-probe-workers", 2, "[스트리밍] 프로브 워커 수")
	flag.DurationVar(&cfg.streamProbeInterval, "stream-probe-interval", 500*time.Millisecond, "[스트리밍] 프로브 간격")
	flag.DurationVar(&cfg.streamBaseline, "stream-baseline", 30*time.Second, "[스트리밍] 베이스라인 측정 시간")
	flag.Float64Var(&cfg.streamP95Mult, "stream-p95-mult", 3.0, "[스트리밍] 허용 p95 배율")
	flag.Float64Var(&cfg.streamErrThreshold, "stream-err-threshold", 5.0, "[스트리밍] 허용 프로브 오류율 %")

	// 시나리오 6
	flag.IntVar(&cfg.concurrentSizeMB, "concurrent-size-mb", 100, "[동시접근] 파일 크기 MB")
	flag.IntVar(&cfg.concurrentWorkers, "concurrent-workers", 50, "[동시접근] 동시 다운로드 수")
	flag.DurationVar(&cfg.concurrentDuration, "concurrent-duration", 30*time.Second, "[동시접근] 테스트 지속 시간")

	flag.Parse()

	s := strings.TrimSpace(*scenariosRaw)
	if s == "all" {
		cfg.scenarios = []string{"small", "large", "single", "integrity", "streaming", "concurrent"}
	} else {
		for _, part := range strings.Split(s, ",") {
			cfg.scenarios = append(cfg.scenarios, strings.TrimSpace(part))
		}
	}
	return cfg
}

// ─── 경로 저장소 (소형 파일 mix 작업용) ───────────────────────────────────────

type pathStore struct {
	mu           sync.Mutex
	byUser       map[string][]string
	folderByUser map[string][]string
}

func newPathStore() *pathStore {
	return &pathStore{
		byUser:       make(map[string][]string),
		folderByUser: make(map[string][]string),
	}
}

func (s *pathStore) add(userID, filePath string) {
	folder := folderOf(filePath)
	s.mu.Lock()
	s.byUser[userID] = append(s.byUser[userID], filePath)
	if folder != "" {
		s.folderByUser[userID] = append(s.folderByUser[userID], folder)
	}
	s.mu.Unlock()
}

func (s *pathStore) popFile(userID string, r *rand.Rand) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.byUser[userID]
	if len(list) == 0 {
		return "", false
	}
	idx := r.Intn(len(list))
	picked := list[idx]
	list[idx] = list[len(list)-1]
	s.byUser[userID] = list[:len(list)-1]
	return picked, true
}

func (s *pathStore) randomFolder(userID string, r *rand.Rand) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.folderByUser[userID]
	if len(list) == 0 {
		return "", false
	}
	return list[r.Intn(len(list))], true
}

func folderOf(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return ""
	}
	return p[:idx]
}

// ─── 시나리오 결과 ────────────────────────────────────────────────────────────

type scenarioResult struct {
	name    string
	pass    bool
	details []string
}

func (r *scenarioResult) detail(format string, args ...interface{}) {
	r.details = append(r.details, fmt.Sprintf(format, args...))
}

// ─── 단계별 워커 실행기 ───────────────────────────────────────────────────────

func runStagedWorkers(ctx context.Context, stages []stage, worker func(workerID int, wctx context.Context)) {
	var wg sync.WaitGroup
	stops := make([]context.CancelFunc, 0)
	current := 0

	spawnWorker := func(id int) {
		wctx, wcancel := context.WithCancel(ctx)
		stops = append(stops, wcancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(id, wctx)
		}()
	}

	setWorkers := func(target int) {
		for current < target {
			current++
			spawnWorker(current)
		}
		for current > target {
			stops[current-1]()
			stops = stops[:current-1]
			current--
		}
	}

loop:
	for _, st := range stages {
		setWorkers(st.Concurrency)
		fmt.Printf("  stage: 동시=%d 시간=%s\n", st.Concurrency, st.Duration)
		select {
		case <-ctx.Done():
			break loop
		case <-time.After(st.Duration):
		}
	}

	setWorkers(0)
	wg.Wait()
}

// ─── 시나리오 1: 작은 파일 다수 요청 ─────────────────────────────────────────

func runSmallFiles(cfg *config) *scenarioResult {
	res := &scenarioResult{name: "1. 작은 파일 다수 요청 성능"}
	fmt.Println("\n══════════════════════════════════════════")
	fmt.Println("[시나리오 1] 작은 파일 다수 요청 성능")
	fmt.Printf("  파일 크기: %dKB ~ %dKB | 동시성: %s | 유저: %d명\n",
		cfg.smallMinKB, cfg.smallMaxKB, cfg.smallStages, cfg.smallUsers)

	stages, err := parseStages(cfg.smallStages)
	if err != nil {
		res.detail("stage 파싱 실패: %v", err)
		return res
	}

	client := &http.Client{Timeout: cfg.timeout}
	m := newMetrics()
	store := newPathStore()

	userIDs := make([]string, cfg.smallUsers)
	for i := range userIDs {
		userIDs[i] = fmt.Sprintf("%s-sm-%03d", cfg.userID, i+1)
	}

	var pathSeq uint64
	ctx, cancel := context.WithCancel(context.Background())

	// 주기적 삭제/이동 워커
	var mixWG sync.WaitGroup
	for i := 0; i < cfg.smallMixWorkers; i++ {
		mixWG.Add(1)
		go func(id int) {
			defer mixWG.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(id+10000)*3571))
			ticker := time.NewTicker(cfg.smallMixInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					uid := userIDs[r.Intn(len(userIDs))]
					smallMixOp(ctx, client, cfg, uid, r, store, m)
				}
			}
		}(i)
	}

	// 업로드 + 조회 워커
	runStagedWorkers(ctx, stages, func(workerID int, wctx context.Context) {
		r := rand.New(rand.NewSource(cfg.seed + int64(workerID)*7919))
		for {
			select {
			case <-wctx.Done():
				return
			default:
			}
			uid := userIDs[r.Intn(len(userIDs))]

			if r.Float64() < 0.2 {
				// 읽기: 목록 조회
				v := url.Values{}
				v.Set("userId", uid)
				if r.Intn(2) == 0 {
					st, _, lat := doSimpleRequest(wctx, client, http.MethodGet, cfg.baseURL+"/files?"+v.Encode(), "", nil)
					m.add("GET /files", lat, st, 0, isHTTPError(st, nil))
				} else {
					folder := fmt.Sprintf("/small/%02d", r.Intn(30))
					v.Set("folderPath", folder)
					st, _, lat := doSimpleRequest(wctx, client, http.MethodGet, cfg.baseURL+"/files/folder?"+v.Encode(), "", nil)
					m.add("GET /files/folder", lat, st, 0, isHTTPError(st, nil))
				}
			} else {
				// 쓰기: 업로드
				sizeKB := cfg.smallMinKB
				if cfg.smallMaxKB > cfg.smallMinKB {
					sizeKB += r.Intn(cfg.smallMaxKB - cfg.smallMinKB + 1)
				}
				sizeBytes := int64(sizeKB) * 1024
				seq := atomic.AddUint64(&pathSeq, 1)
				filePath := fmt.Sprintf("/small/%02d/%03d/f-%d.bin", r.Intn(30), r.Intn(100), seq)
				id, status, latency, err := uploadFile(wctx, client, cfg, filePath, endlessByteReader{}, sizeBytes)
				m.add("POST /files/upload", latency, status, sizeBytes, isHTTPError(status, err))
				if err == nil && status >= 200 && status < 300 && id != "" {
					store.add(uid, filePath)
				}
			}
		}
	})

	cancel()
	mixWG.Wait()

	// 결과 평가
	uploadCount, uploadErr, _, uploadP95, uploadP99, _ := m.get("POST /files/upload").summary()
	getCount, getErr, _, getP95, getP99, _ := m.get("GET /files").summary()
	uploadSuccessRate := 100.0 - ratioPercent(uploadErr, uploadCount)
	getSuccessRate := 100.0 - ratioPercent(getErr, getCount)

	res.detail("업로드:   count=%d successRate=%.2f%% p95=%s p99=%s", uploadCount, uploadSuccessRate, uploadP95, uploadP99)
	res.detail("GET /files: count=%d successRate=%.2f%% p95=%s p99=%s", getCount, getSuccessRate, getP95, getP99)

	for _, key := range []string{"GET /files/folder", "DELETE /files", "DELETE /files/folder", "POST /files/move-folder"} {
		c, e, _, p95, p99, _ := m.get(key).summary()
		if c > 0 {
			res.detail("%s: count=%d successRate=%.2f%% p95=%s p99=%s", key, c, 100.0-ratioPercent(e, c), p95, p99)
		}
	}

	passUpload := uploadCount > 0 && uploadSuccessRate >= 99.0
	passGet := getCount == 0 || getSuccessRate >= 99.0
	res.pass = passUpload && passGet
	res.detail("합격: 업로드 성공률 ≥ 99%% → %v (%.2f%%)", passUpload, uploadSuccessRate)

	printMetrics(m, "시나리오 1")
	return res
}

func smallMixOp(ctx context.Context, client *http.Client, cfg *config, uid string, r *rand.Rand, store *pathStore, m *metrics) {
	choice := r.Float64()
	if choice < 0.40 {
		if filePath, ok := store.popFile(uid, r); ok {
			v := url.Values{}
			v.Set("userId", uid)
			v.Set("filePath", filePath)
			st, _, lat := doSimpleRequest(ctx, client, http.MethodDelete, cfg.baseURL+"/files?"+v.Encode(), "", nil)
			m.add("DELETE /files", lat, st, 0, isHTTPError(st, nil))
		}
		return
	}
	if choice < 0.70 {
		folder := fmt.Sprintf("/small/%02d", r.Intn(30))
		if f, ok := store.randomFolder(uid, r); ok {
			folder = f
		}
		v := url.Values{}
		v.Set("userId", uid)
		v.Set("folderPath", folder)
		st, _, lat := doSimpleRequest(ctx, client, http.MethodDelete, cfg.baseURL+"/files/folder?"+v.Encode(), "", nil)
		m.add("DELETE /files/folder", lat, st, 0, isHTTPError(st, nil))
		return
	}
	fromPath := fmt.Sprintf("/small/%02d", r.Intn(30))
	toPath := fmt.Sprintf("/small-moved/%02d", r.Intn(30))
	payload, _ := json.Marshal(map[string]string{"fromPath": fromPath, "toPath": toPath})
	st, _, lat := doSimpleRequest(ctx, client, http.MethodPost, cfg.baseURL+"/files/move-folder", "application/json", payload)
	m.add("POST /files/move-folder", lat, st, 0, isHTTPError(st, nil))
}

// ─── 시나리오 2: 큰 파일 다수 요청 ──────────────────────────────────────────

func runLargeFiles(cfg *config) *scenarioResult {
	res := &scenarioResult{name: "2. 큰 파일 다수 요청 성능"}
	fmt.Println("\n══════════════════════════════════════════")
	fmt.Println("[시나리오 2] 큰 파일 다수 요청 성능")
	fmt.Printf("  파일 크기: %dMB ~ %dMB | 읽기 비율: %.0f%% | 동시성: %s\n",
		cfg.largeMinMB, cfg.largeMaxMB, cfg.largeReadRatio*100, cfg.largeStages)

	stages, err := parseStages(cfg.largeStages)
	if err != nil {
		res.detail("stage 파싱 실패: %v", err)
		return res
	}

	client := &http.Client{Timeout: cfg.timeout}
	m := newMetrics()

	// 사전 파일 업로드
	fmt.Printf("  사전 파일 업로드: %d개 × %dMB\n", cfg.largePrepareCount, cfg.largePrepareMB)
	var poolIDs []string
	for i := 0; i < cfg.largePrepareCount; i++ {
		sizeBytes := int64(cfg.largePrepareMB) * 1024 * 1024
		filePath := fmt.Sprintf("/large/prepare/%02d/f-%d.bin", i+1, time.Now().UnixNano())
		prepCtx, prepCancel := context.WithTimeout(context.Background(), cfg.prepareTimeout)
		id, status, _, err := uploadFile(prepCtx, client, cfg, filePath, endlessByteReader{}, sizeBytes)
		prepCancel()
		if err != nil || status < 200 || status >= 300 || id == "" {
			res.detail("사전 업로드 실패 %d/%d: status=%d err=%v", i+1, cfg.largePrepareCount, status, err)
			return res
		}
		poolIDs = append(poolIDs, id)
		fmt.Printf("  prepared %d/%d id=%s\n", i+1, cfg.largePrepareCount, id)
	}

	type safePool struct {
		mu  sync.RWMutex
		ids []string
	}
	pool := &safePool{ids: poolIDs}

	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runStagedWorkers(ctx, stages, func(workerID int, wctx context.Context) {
		r := rand.New(rand.NewSource(cfg.seed + int64(workerID)*7919))
		for {
			select {
			case <-wctx.Done():
				return
			default:
			}
			if r.Float64() < cfg.largeReadRatio {
				pool.mu.RLock()
				if len(pool.ids) == 0 {
					pool.mu.RUnlock()
					continue
				}
				id := pool.ids[r.Intn(len(pool.ids))]
				pool.mu.RUnlock()
				status, received, latency, err := downloadDiscard(wctx, client, cfg.baseURL, id)
				m.add("GET /files/{id}/download", latency, status, received, isHTTPError(status, err))
			} else {
				sizeMB := cfg.largeMinMB
				if cfg.largeMaxMB > cfg.largeMinMB {
					sizeMB += r.Intn(cfg.largeMaxMB - cfg.largeMinMB + 1)
				}
				sizeBytes := int64(sizeMB) * 1024 * 1024
				filePath := fmt.Sprintf("/large/run/%02d/%d.bin", workerID, r.Int63())
				id, status, latency, err := uploadFile(wctx, client, cfg, filePath, endlessByteReader{}, sizeBytes)
				m.add("POST /files/upload", latency, status, sizeBytes, isHTTPError(status, err))
				if err == nil && status >= 200 && status < 300 && id != "" {
					pool.mu.Lock()
					pool.ids = append(pool.ids, id)
					pool.mu.Unlock()
				}
			}
		}
	})

	elapsed := time.Since(start)

	uploadCount, uploadErr, uploadBytes, uploadP95, uploadP99, _ := m.get("POST /files/upload").summary()
	dlCount, dlErr, dlBytes, dlP95, dlP99, _ := m.get("GET /files/{id}/download").summary()
	uploadSuccessRate := 100.0 - ratioPercent(uploadErr, uploadCount)
	dlSuccessRate := 100.0 - ratioPercent(dlErr, dlCount)

	uploadTP := 0.0
	dlTP := 0.0
	totalTP := 0.0
	if elapsed > 0 {
		uploadTP = bytesToMiB(uploadBytes) / elapsed.Seconds()
		dlTP = bytesToMiB(dlBytes) / elapsed.Seconds()
		totalTP = bytesToMiB(uploadBytes+dlBytes) / elapsed.Seconds()
	}

	res.detail("업로드:   count=%d successRate=%.2f%% throughput=%.2f MiB/s p95=%s p99=%s",
		uploadCount, uploadSuccessRate, uploadTP, uploadP95, uploadP99)
	res.detail("다운로드: count=%d successRate=%.2f%% throughput=%.2f MiB/s p95=%s p99=%s",
		dlCount, dlSuccessRate, dlTP, dlP95, dlP99)
	res.detail("전체 처리량: %.2f MiB/s", totalTP)

	passUpload := uploadCount == 0 || uploadSuccessRate >= 99.0
	passDL := dlCount == 0 || dlSuccessRate >= 99.0
	res.pass = passUpload && passDL
	res.detail("합격: 업로드 성공률 ≥ 99%% → %v (%.2f%%), 다운로드 성공률 ≥ 99%% → %v (%.2f%%)",
		passUpload, uploadSuccessRate, passDL, dlSuccessRate)

	printMetrics(m, "시나리오 2")
	return res
}

// ─── 시나리오 3: 큰 단일 파일 ────────────────────────────────────────────────

func runSingleLarge(cfg *config) *scenarioResult {
	res := &scenarioResult{name: "3. 큰 단일 파일 요청 성능"}
	fmt.Println("\n══════════════════════════════════════════")
	fmt.Printf("[시나리오 3] 큰 단일 파일 요청 성능 (크기: %dMB)\n", cfg.singleSizeMB)

	client := &http.Client{Timeout: cfg.timeout}
	sizeBytes := int64(cfg.singleSizeMB) * 1024 * 1024
	filePath := fmt.Sprintf("/single/f-%d.bin", time.Now().UnixNano())

	// 업로드
	fmt.Printf("  업로드 중 (%dMB)...\n", cfg.singleSizeMB)
	ctx := context.Background()
	id, status, latency, err := uploadFile(ctx, client, cfg, filePath, endlessByteReader{}, sizeBytes)
	uploadOK := err == nil && status >= 200 && status < 300 && id != ""
	passStr := map[bool]string{true: "성공", false: "실패"}
	res.detail("업로드: status=%d latency=%s err=%v → %s", status, latency, err, passStr[uploadOK])

	if !uploadOK {
		res.pass = false
		return res
	}
	fmt.Printf("  업로드 완료: id=%s latency=%s\n", id, latency)

	// 다운로드
	fmt.Printf("  다운로드 중...\n")
	dlStatus, received, dlLatency, dlErr := downloadDiscard(ctx, client, cfg.baseURL, id)
	dlOK := dlErr == nil && dlStatus == 200

	if dlStatus == 200 && received != sizeBytes {
		res.detail("경고: 수신 바이트 불일치 expected=%d got=%d", sizeBytes, received)
		dlOK = false
	}
	res.detail("다운로드: status=%d received=%dMiB/%dMiB latency=%s err=%v → %s",
		dlStatus, received/(1024*1024), cfg.singleSizeMB, dlLatency, dlErr, passStr[dlOK])

	fmt.Printf("  다운로드 완료: %dMiB 수신 latency=%s\n", received/(1024*1024), dlLatency)

	res.pass = uploadOK && dlOK
	return res
}

// ─── 시나리오 4: 파일 무결성 검증 ────────────────────────────────────────────

func runIntegrity(cfg *config) *scenarioResult {
	res := &scenarioResult{name: "4. 파일 무결성 검증"}
	fmt.Println("\n══════════════════════════════════════════")
	fmt.Printf("[시나리오 4] 파일 무결성 검증 (크기: %s | 동시 다운로드: %d개)\n",
		cfg.integritySizesMB, cfg.integrityConcurrent)

	var sizesMB []int
	for _, s := range strings.Split(cfg.integritySizesMB, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			res.detail("잘못된 크기: %q", s)
			return res
		}
		sizesMB = append(sizesMB, v)
	}
	if len(sizesMB) == 0 {
		res.detail("크기 목록이 비어 있음")
		return res
	}

	client := &http.Client{Timeout: cfg.timeout}
	ctx := context.Background()

	var totalChecks int64
	var mismatches int64

	for idx, sizeMB := range sizesMB {
		sizeBytes := int64(sizeMB) * 1024 * 1024
		block := makeBlock(cfg.seed + int64(idx)*997)
		expected := checksumOfBlock(block, sizeBytes)
		filePath := fmt.Sprintf("/integrity/%04dMB/f-%d.bin", sizeMB, time.Now().UnixNano())

		fmt.Printf("  [%dMB] 업로드 중...\n", sizeMB)
		prepCtx, prepCancel := context.WithTimeout(ctx, cfg.prepareTimeout)
		id, status, _, err := uploadFile(prepCtx, client, cfg, filePath, newRepeatingReader(block, sizeBytes), sizeBytes)
		prepCancel()

		if err != nil || status < 200 || status >= 300 || id == "" {
			res.detail("[%dMB] 업로드 실패: status=%d err=%v", sizeMB, status, err)
			atomic.AddInt64(&mismatches, int64(cfg.integrityConcurrent))
			atomic.AddInt64(&totalChecks, int64(cfg.integrityConcurrent))
			continue
		}
		fmt.Printf("  [%dMB] 업로드 완료 id=%s → 동시 다운로드 %d개\n", sizeMB, id, cfg.integrityConcurrent)

		var wg sync.WaitGroup
		for i := 0; i < cfg.integrityConcurrent; i++ {
			wg.Add(1)
			go func(workerIdx int) {
				defer wg.Done()
				atomic.AddInt64(&totalChecks, 1)
				dlStatus, got, _, _, dlErr := downloadAndHash(ctx, client, cfg.baseURL, id)
				if dlErr != nil || dlStatus != 200 {
					atomic.AddInt64(&mismatches, 1)
					res.detail("[%dMB] worker=%d 다운로드 실패: status=%d err=%v", sizeMB, workerIdx, dlStatus, dlErr)
					return
				}
				if got != expected {
					atomic.AddInt64(&mismatches, 1)
					res.detail("[%dMB] worker=%d 체크섬 불일치", sizeMB, workerIdx)
				}
			}(i)
		}
		wg.Wait()
		fmt.Printf("  [%dMB] 완료 (불일치: %d건)\n", sizeMB, atomic.LoadInt64(&mismatches))
	}

	total := atomic.LoadInt64(&totalChecks)
	mm := atomic.LoadInt64(&mismatches)
	res.detail("총 검증: %d건 | 체크섬 불일치: %d건", total, mm)
	res.pass = mm == 0 && total > 0
	res.detail("합격: 체크섬 불일치 0건 → %v", res.pass)
	return res
}

// ─── 시나리오 5: 대용량 파일 스트리밍 성능 ────────────────────────────────────

func runStreaming(cfg *config) *scenarioResult {
	res := &scenarioResult{name: "5. 대용량 파일 스트리밍 성능"}
	fmt.Println("\n══════════════════════════════════════════")
	fmt.Println("[시나리오 5] 대용량 파일 스트리밍 성능")
	fmt.Printf("  파일: %d개×%dMB | 느린클라이언트: 청크=%dKB sleep=%s | 동시성: %s\n",
		cfg.streamPrepareCount, cfg.streamPrepareMB, cfg.streamChunkKB, cfg.streamSleep, cfg.streamStages)

	stages, err := parseStages(cfg.streamStages)
	if err != nil {
		res.detail("stage 파싱 실패: %v", err)
		return res
	}

	client := &http.Client{Timeout: cfg.timeout}

	// 사전 파일 업로드
	fmt.Printf("  사전 파일 업로드: %d개 × %dMB\n", cfg.streamPrepareCount, cfg.streamPrepareMB)
	var fileIDs []string
	for i := 0; i < cfg.streamPrepareCount; i++ {
		sizeBytes := int64(cfg.streamPrepareMB) * 1024 * 1024
		filePath := fmt.Sprintf("/streaming/prepare/%02d/f-%d.bin", i+1, time.Now().UnixNano())
		prepCtx, prepCancel := context.WithTimeout(context.Background(), cfg.prepareTimeout)
		id, status, _, err := uploadFile(prepCtx, client, cfg, filePath, endlessByteReader{}, sizeBytes)
		prepCancel()
		if err != nil || status < 200 || status >= 300 || id == "" {
			res.detail("사전 업로드 실패 %d/%d: status=%d err=%v", i+1, cfg.streamPrepareCount, status, err)
			return res
		}
		fileIDs = append(fileIDs, id)
		fmt.Printf("  prepared %d/%d id=%s\n", i+1, cfg.streamPrepareCount, id)
	}

	probeKeys := []string{"GET /health", "GET /files"}
	baselineM := newMetrics()
	stressM := newMetrics()

	// 베이스라인 프로브
	if cfg.streamBaseline > 0 {
		fmt.Printf("  베이스라인 프로브 시작 (%s)...\n", cfg.streamBaseline)
		runProbePhase(cfg.streamBaseline, cfg, client, baselineM)
	}

	// 스트리밍 부하 + 동시 프로브
	fmt.Printf("  스트리밍 부하 시작...\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var probeWG sync.WaitGroup
	for i := 0; i < cfg.streamProbeWorkers; i++ {
		probeWG.Add(1)
		go func(id int) {
			defer probeWG.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(id+9000)*131))
			ticker := time.NewTicker(cfg.streamProbeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					doProbe(ctx, client, cfg, r, stressM)
				}
			}
		}(i)
	}

	runStagedWorkers(ctx, stages, func(workerID int, wctx context.Context) {
		r := rand.New(rand.NewSource(cfg.seed + int64(workerID)*7919))
		for {
			select {
			case <-wctx.Done():
				return
			default:
			}
			id := fileIDs[r.Intn(len(fileIDs))]
			status, latency, err := downloadSlow(wctx, client, cfg.baseURL, id,
				cfg.streamChunkKB*1024, cfg.streamSleep)
			isErr := isHTTPError(status, err) && !errors.Is(err, context.Canceled)
			stressM.add("GET /files/{id}/download (slow)", latency, status, 0, isErr)
		}
	})

	cancel()
	probeWG.Wait()

	// 평가
	degradePass := true
	errPass := true
	for _, k := range probeKeys {
		bCount, _, _, bP95, _, _ := baselineM.get(k).summary()
		sCount, sErr, _, sP95, _, _ := stressM.get(k).summary()

		mult := math.Inf(1)
		if bP95 > 0 {
			mult = float64(sP95) / float64(bP95)
		}
		if bCount > 0 && !math.IsInf(mult, 1) && mult > cfg.streamP95Mult {
			degradePass = false
		}
		errRate := ratioPercent(sErr, sCount)
		if sCount > 0 && errRate > cfg.streamErrThreshold {
			errPass = false
		}
		multStr := fmt.Sprintf("%.2fx", mult)
		if math.IsInf(mult, 1) {
			multStr = "INF(베이스라인 없음)"
		}
		res.detail("%s: baseline p95=%s | stress p95=%s 배율=%s | 오류율=%.2f%%",
			k, bP95, sP95, multStr, errRate)
	}

	dlCount, dlErr, _, _, _, _ := stressM.get("GET /files/{id}/download (slow)").summary()
	res.detail("느린 다운로드: count=%d errorRate=%.2f%%", dlCount, ratioPercent(dlErr, dlCount))
	res.pass = degradePass && errPass
	res.detail("합격: p95 배율 ≤ %.1fx → %v | 프로브 오류율 ≤ %.1f%% → %v",
		cfg.streamP95Mult, degradePass, cfg.streamErrThreshold, errPass)

	printMetrics(stressM, "시나리오 5 (stress)")
	return res
}

func runProbePhase(duration time.Duration, cfg *config, client *http.Client, m *metrics) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < cfg.streamProbeWorkers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(cfg.seed + int64(id)*97))
			ticker := time.NewTicker(cfg.streamProbeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					doProbe(ctx, client, cfg, r, m)
				}
			}
		}(i)
	}
	wg.Wait()
}

func doProbe(ctx context.Context, client *http.Client, cfg *config, r *rand.Rand, m *metrics) {
	if r.Intn(2) == 0 {
		st, _, lat := doSimpleRequest(ctx, client, http.MethodGet, cfg.baseURL+"/health", "", nil)
		m.add("GET /health", lat, st, 0, isHTTPError(st, nil))
		return
	}
	v := url.Values{}
	v.Set("userId", cfg.userID)
	st, _, lat := doSimpleRequest(ctx, client, http.MethodGet, cfg.baseURL+"/files?"+v.Encode(), "", nil)
	m.add("GET /files", lat, st, 0, isHTTPError(st, nil))
}

// ─── 시나리오 6: 동시 동일 파일 접근 ─────────────────────────────────────────

func runConcurrentSame(cfg *config) *scenarioResult {
	res := &scenarioResult{name: "6. 동시 동일 파일 접근"}
	fmt.Println("\n══════════════════════════════════════════")
	fmt.Printf("[시나리오 6] 동시 동일 파일 접근 (크기: %dMB | 동시 다운로드: %d개 | 시간: %s)\n",
		cfg.concurrentSizeMB, cfg.concurrentWorkers, cfg.concurrentDuration)

	client := &http.Client{Timeout: cfg.timeout}
	sizeBytes := int64(cfg.concurrentSizeMB) * 1024 * 1024
	block := makeBlock(cfg.seed + 6000)
	expected := checksumOfBlock(block, sizeBytes)
	filePath := fmt.Sprintf("/concurrent/f-%d.bin", time.Now().UnixNano())

	// 단일 파일 업로드
	fmt.Printf("  파일 업로드 중 (%dMB)...\n", cfg.concurrentSizeMB)
	prepCtx, prepCancel := context.WithTimeout(context.Background(), cfg.prepareTimeout)
	id, status, _, err := uploadFile(prepCtx, client, cfg, filePath, newRepeatingReader(block, sizeBytes), sizeBytes)
	prepCancel()
	if err != nil || status < 200 || status >= 300 || id == "" {
		res.detail("업로드 실패: status=%d err=%v", status, err)
		res.pass = false
		return res
	}
	fmt.Printf("  업로드 완료 id=%s → %d개 동시 다운로드 시작...\n", id, cfg.concurrentWorkers)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.concurrentDuration)
	defer cancel()

	var totalOps, successOps, mismatchOps int64

	var wg sync.WaitGroup
	for i := 0; i < cfg.concurrentWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				dlStatus, got, _, _, dlErr := downloadAndHash(ctx, client, cfg.baseURL, id)
				if errors.Is(dlErr, context.DeadlineExceeded) || errors.Is(dlErr, context.Canceled) {
					return
				}
				atomic.AddInt64(&totalOps, 1)
				if dlErr != nil || dlStatus != 200 {
					continue
				}
				atomic.AddInt64(&successOps, 1)
				if got != expected {
					atomic.AddInt64(&mismatchOps, 1)
					res.detail("체크섬 불일치 감지: got=%s expected=%s", got[:8]+"...", expected[:8]+"...")
				}
			}
		}()
	}
	wg.Wait()

	total := atomic.LoadInt64(&totalOps)
	success := atomic.LoadInt64(&successOps)
	mismatch := atomic.LoadInt64(&mismatchOps)
	successRate := ratioPercent(success, total)

	res.detail("총 요청: %d | 성공: %d (%.2f%%) | 체크섬 불일치: %d건", total, success, successRate, mismatch)

	passSuccess := total > 0 && successRate >= 99.0
	passMismatch := mismatch == 0
	res.pass = passSuccess && passMismatch
	res.detail("합격: 성공률 ≥ 99%% → %v (%.2f%%) | 체크섬 불일치 0건 → %v (%d건)",
		passSuccess, successRate, passMismatch, mismatch)

	return res
}

// ─── 리포트 출력 ──────────────────────────────────────────────────────────────

func printMetrics(m *metrics, label string) {
	fmt.Printf("\n  ── %s 엔드포인트 통계 ──\n", label)
	m.mu.Lock()
	keys := make([]string, 0, len(m.endpoints))
	for k := range m.endpoints {
		keys = append(keys, k)
	}
	m.mu.Unlock()
	sort.Strings(keys)

	for _, k := range keys {
		count, errs, totalBytes, p95, p99, codes := m.get(k).summary()
		if count == 0 {
			continue
		}
		fmt.Printf("  %s\n", k)
		fmt.Printf("    count=%d errors=%d (%.2f%%) bytes=%.2fMiB p95=%s p99=%s\n",
			count, errs, ratioPercent(errs, count), bytesToMiB(totalBytes), p95, p99)
		printCodes(codes, 8)
	}
}

func printCodes(codes map[int]int64, limit int) {
	type kv struct {
		k int
		v int64
	}
	items := make([]kv, 0, len(codes))
	for k, v := range codes {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v == items[j].v {
			return items[i].k < items[j].k
		}
		return items[i].v > items[j].v
	})
	if len(items) == 0 {
		return
	}
	if limit > len(items) {
		limit = len(items)
	}
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, fmt.Sprintf("%d:%d", items[i].k, items[i].v))
	}
	fmt.Printf("    status: %s\n", strings.Join(parts, ", "))
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	cfg := parseFlags()

	fmt.Println("══════════════════════════════════════════")
	fmt.Println("  파일 서버 성능 벤치마크")
	fmt.Println("══════════════════════════════════════════")
	fmt.Printf("base-url     : %s\n", cfg.baseURL)
	fmt.Printf("upload-mode  : %s\n", cfg.uploadMode)
	fmt.Printf("scenarios    : %s\n", strings.Join(cfg.scenarios, ", "))
	fmt.Printf("seed         : %d\n", cfg.seed)
	fmt.Printf("timeout      : %v\n", cfg.timeout)

	scenarioSet := make(map[string]bool)
	for _, s := range cfg.scenarios {
		scenarioSet[s] = true
	}

	var results []*scenarioResult
	if scenarioSet["small"] {
		results = append(results, runSmallFiles(cfg))
	}
	if scenarioSet["large"] {
		results = append(results, runLargeFiles(cfg))
	}
	if scenarioSet["single"] {
		results = append(results, runSingleLarge(cfg))
	}
	if scenarioSet["integrity"] {
		results = append(results, runIntegrity(cfg))
	}
	if scenarioSet["streaming"] {
		results = append(results, runStreaming(cfg))
	}
	if scenarioSet["concurrent"] {
		results = append(results, runConcurrentSame(cfg))
	}

	fmt.Println("\n\n══════════════════════════════════════════")
	fmt.Println("  최종 결과 요약")
	fmt.Println("══════════════════════════════════════════")
	allPass := true
	for _, r := range results {
		status := "✓ PASS"
		if !r.pass {
			status = "✗ FAIL"
			allPass = false
		}
		fmt.Printf("[%s] %s\n", status, r.name)
		for _, d := range r.details {
			fmt.Printf("         %s\n", d)
		}
		fmt.Println()
	}
	fmt.Println("──────────────────────────────────────────")
	if allPass {
		fmt.Println("전체 결과: PASS")
	} else {
		fmt.Println("전체 결과: FAIL")
		os.Exit(1)
	}
}
