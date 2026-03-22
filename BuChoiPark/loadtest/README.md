# DB Lock Contention Load Test

This load test targets the scenario in `memo/bdg.md`:

- Upload concurrency ramp: 50 -> 100 -> 150
- Read/Write ratio target: read 20% / write 80%
- Write mix: upload + periodic delete/move-folder
- Upload file size: random 100KB ~ 1MB
- Output checks: p95/p99 per endpoint, write error rate, lock keyword hits

## Files

- `loadtest/db_lock_stress.go`: load generator + metrics report
- `loadtest/run_db_lock_stress.sh`: convenient runner script

## Quick Run (host)

```bash
cd BuChoiPark
chmod +x loadtest/run_db_lock_stress.sh
./loadtest/run_db_lock_stress.sh
```

## Quick Run (in loadtest container)

```bash
cd /work
TARGET_BASE_URL=http://server-debug:8080 bash ./loadtest/run_db_lock_stress.sh
```

## Tunable Environment Variables

- `TARGET_BASE_URL` (default: `http://localhost:8080`)
- `STAGES` (default: `50:2m,100:2m,150:2m`)
- `READ_RATIO` (default: `0.20`)
- `MIN_KB` (default: `100`)
- `MAX_KB` (default: `1024`)
- `USERS` (default: `200`)
- `TIMEOUT` (default: `20s`)
- `MIX_WORKERS` (default: `4`)
- `MIX_INTERVAL` (default: `500ms`)
- `SEED` (default: current unix timestamp)

Example:

```bash
TARGET_BASE_URL=http://localhost:8080 \
STAGES="50:3m,100:5m,150:8m" \
READ_RATIO=0.20 \
MIX_WORKERS=6 \
MIX_INTERVAL=300ms \
./loadtest/run_db_lock_stress.sh
```

## What To Watch During Test

- App logs for lock signals:
  - `database is locked`
  - `SQLITE_BUSY`
  - `transaction timeout`
- Endpoint latency spikes (especially write APIs):
  - `POST /files/upload`
  - `POST /files/move-folder`
  - `DELETE /files` and `DELETE /files/folder`
- CPU still has headroom but latency grows -> likely lock wait behavior.

## Report Interpretation

The report prints:

- Request totals and actual read/write ratio
- Write API error rate (target: `< 1%`)
- Lock keyword hit count in response/error body
- Endpoint-level p95/p99 and status distribution

Example success gate (from memo):

- Concurrency around 100 under mixed write load:
  - write API error rate `< 1%`
  - lock keyword hits near zero
  - p95 remains stable without sudden jumps


### example db_lock_stress.go output
```bash
#input
go run ./loadtest/db_lock_stress.go \
  -base-url http://server-debug:8080 \
  -stages 300:10s,400:10s \
  -read-ratio 0.05 \
  -users 30 \
  -mix-workers 16 \
  -mix-interval 80ms \
  -min-kb 100 \
  -max-kb 256

#output
stage: concurrency=300 duration=10s
stage: concurrency=400 duration=10s

================ Load Test Report ================
total requests   : 4379
write requests   : 4190 (failures=401, error rate=9.57%)
read requests    : 189 (failures=21, error rate=11.11%)
actual R/W ratio : read 4.32% / write 95.68% (target read 5.00% / write 95.00%)
lock keyword hit : 0 (body contains 'database is locked'|'SQLITE_BUSY'|'transaction timeout')

-- Endpoint p95/p99 --
DELETE /files
  count=64 errors=9 errorRate=14.06% p95=2.997605059s p99=3.373232599s
  status codes: 200:33, 404:22, 0:9
DELETE /files/folder
  count=55 errors=7 errorRate=12.73% p95=3.247136048s p99=3.372886965s
  status codes: 200:32, 404:16, 0:7
GET /files
  count=103 errors=17 errorRate=16.50% p95=2.380669654s p99=2.427901878s
  status codes: 200:86, 0:17
GET /files/folder
  count=86 errors=4 errorRate=4.65% p95=2.377450363s p99=2.448222712s
  status codes: 200:82, 0:4
POST /files/move-folder
  count=47 errors=6 errorRate=12.77% p95=2.361921218s p99=3.366552575s
  status codes: 200:41, 0:6
POST /files/upload
  count=4024 errors=379 errorRate=9.42% p95=2.390086596s p99=2.429092725s
  status codes: 200:3645, 0:379

-- Success Gate (example from memo) --
  write API error rate < 1%: false (9.57%)
  lock keyword near-zero : true (count=0)
  overall gate pass      : false
==================================================
```

### example web_server_stress.go output

```bash
# input
cd /data/26-1-mystery-x/BuChoiPark
go run web_server_stress.go \
  -base-url http://server-debug:8080 \
  -stages 100:10s,200:10s,300:10s \
  -prepare-count 6 \
  -prepare-size-mb 100 \
  -probe-workers 2 \
  -probe-interval 500ms \
  -slow-chunk-kb 64 \
  -slow-sleep 100ms
```

### example io_stress.go output

```bash
cd /work/loadtest
go run io_stress.go \
  -base-url http://server-debug:8080 \
  -stages 10:10s,20:10s,40:10s \
  -read-ratio 0.8 \
  -upload-mode multipart \
  -upload-min-mb 500 \
  -upload-max-mb 1000 \
  -prepare-count 8 \
  -prepare-size-mb 100 \
  -timeout 0

go run io_stress.go \
  -base-url http://server-debug:8080 \
  -stages 20:20s \
  -read-ratio 0.8 \
  -upload-mode raw \
  -upload-min-mb 500 \
  -upload-max-mb 1000 \
  -prepare-count 8 \
  -prepare-size-mb 100 \
  -timeout 0
```

- `-upload-mode multipart` : `multipart/form-data` 업로드 (Nginx에서는 `/_legacy/upload` 경유)
- `-upload-mode raw` : raw body + `X-User-Id/X-File-Path/X-File-Name` 헤더 업로드 (Nginx Lua 직저장 경로)

## Web Server Stress + Resource Metrics

`web_server_stress.go` 실행과 동시에 서버 자원 사용률을 수집하려면 아래 스크립트를 사용하세요.

- `loadtest/run_web_server_stress_with_metrics.sh`: 테스트 실행 + 리소스 수집 오케스트레이션
- `loadtest/collect_server_metrics.sh`: 1초 주기 docker stats + `ss -s` 수집

### Quick Run (host or docker-capable shell)

```bash
cd BuChoiPark
chmod +x loadtest/collect_server_metrics.sh loadtest/run_web_server_stress_with_metrics.sh
TARGET_BASE_URL=http://server-debug:8080 \
bash ./loadtest/run_web_server_stress_with_metrics.sh
```

### Output

결과는 `loadtest/results/<timestamp>/` 아래에 저장됩니다.

- `stress.log`: `web_server_stress.go` 원본 출력
- `docker_stats.csv`: CPU/메모리/네트워크/블록 I/O/PIDs
- `socket_established.csv`: `ss -s`에서 추출한 established 연결 수
- `socket_raw.log`: `ss -s` 원본 스냅샷
- `server.log`: 테스트 구간 서버 로그
- `error_signals.log`: timeout/connection reset/broken pipe/503 관련 라인
- `summary.txt`: 실행 메타 정보

