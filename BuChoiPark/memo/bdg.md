## 의견 메모

- userId를 filePath에 포함시키는게 성능상 이점으로 보입니다.
- 어차피 file을 검색할 때는 항상 userId가 필요하기 때문에, filePath에 userId를 포함시키는 것이 검색 시에 더 빠르게 접근할 수 있다고 봅니다.
- /userId/filePath 형태로 저장하면, 특정 userId에 대한 파일들을 쉽게 그룹화할 수 있고, 검색 시에도 해당 userId에 대한 파일들을 빠르게 찾을 수 있을 것입니다.


## 부하 발생 상황 가정

### 1. DB락 경합

- 테스트 케이스
    - upload를 동시 50~150로 올리고, 동시에 delete/move-folder를 주기적으로 섞음.
    - 읽기/쓰기 비율을 read 20% / write 80%로 강하게 줌.
    - 파일 크기는 작게(100KB~1MB) 해서 “디스크 대역폭”보다 “DB write 경합”이 드러나게 함.
- 발생 확인 방법
    - 앱 로그에서 database is locked, SQLITE_BUSY, transaction timeout 확인.
    - POST /files/upload, POST /files/move-folder, DELETE /files*의 p95/p99 급등 확인.
    - CPU가 여유인데 응답시간만 증가하면 lock 대기 가능성 큼.
    - SQL 실행시간 분포에서 특정 write SQL tail latency 급증 확인.
- 성공 판정 예시
    - 동시성 100에서 write API 오류율 < 1%, lock 에러 거의 없음, p95 안정.

### 2. 디스크 IO 포화
- 테스트 케이스
    - 큰 파일 업로드/다운로드 혼합: 50MB~300MB, 동시 10~40.
    - read-heavy(다운로드 80%)와 write-heavy(업로드 80%)를 각각 분리 수행.
    - 20~30분 지속 테스트로 순간 버스트가 아닌 포화 상태를 확인.
- 발생 확인 방법
    - iostat -x 1에서 %util 90~100% 지속, await 상승, avgqu-sz 증가.
    - vmstat 1에서 wa(iowait) 증가.
    - API latency가 파일 크기에 비례 이상으로 악화, timeout 증가.
    - docker stats에서 CPU보다 I/O 대기 중심으로 처리량 정체.
- 성공 판정 예시
    - 목표 처리량 구간에서 %util이 계속 100% 고정되지 않고, p95 급격한 붕괴 없음.

### 3. JVM 메모리 압박
- 테스트 케이스
    - 다중 multipart 업로드를 동시 50~120로 수행.
    - 중간 크기 파일(2MB~20MB)을 랜덤으로 섞어 힙/버퍼 압박 유도.
    - 1~2시간 soak 테스트로 누수/GC 악화를 확인.
- 발생 확인 방법
    - Actuator/JVM 메트릭: heap used, old gen 비율, GC pause time, GC 횟수.
    - docker stats에서 메모리 우상향(회수되지 않음) 확인.
    - Full GC 빈도 증가, pause 증가, 그 시점 latency spike 동시 발생 확인.
    - OOMKilled, OutOfMemoryError, Direct buffer memory 에러 로그 체크.
- 성공 판정 예시
    - 장시간 테스트에서 RSS/heap가 saw-tooth 패턴으로 안정, Full GC 드묾.


### 4. 웹서버 스레드 고갈
- 테스트 케이스
    - 느린 클라이언트 다운로드 시뮬레이션(응답을 천천히 읽는 소비자) + 동시 연결 100~300.
    - 대용량 다운로드(100MB+)로 연결 점유 시간을 길게 유지.
    - 동시에 짧은 API(/health, /files)를 소량 호출해 영향 확인.
- 발생 확인 방법
    - Tomcat 메트릭: threads.busy == max 근접/고정, queued request 증가.
    - 짧은 API까지 응답 지연되면 thread starvation 신호.
    - ss -s에서 ESTABLISHED 연결 과다, accept backlog 징후.
    - timeout/503/connection reset 증가 확인.
- 성공 판정 예시
    - 다운로드 부하 중에도 경량 API p95가 크게 악화되지 않음.

### 5. SQL Like문 부하 급증
- 테스트 케이스
    - 특정 user에 파일 10만~100만 건 데이터 준비.
    - 깊은 경로(a/b/c/...)와 넓은 경로(동일 prefix 대량) 모두 생성.
    - folderPath가 넓게 매칭되는 요청을 반복 호출:
        - DELETE /files/folder
        - POST /files/move-folder
        - GET /files/folder
- 발생 확인 방법
    - 해당 API latency만 비정상적으로 증가.
    - DB 쿼리 시간 로그에서 LIKE 'prefix%' 쿼리 tail latency 증가.
    - EXPLAIN QUERY PLAN으로 인덱스 사용 여부 확인(풀스캔 여부).
    - 요청 당 처리 row 수(영향받은 row)와 latency 상관관계 확인.
- 성공 판정 예시
    - 데이터량 증가에 따라 선형 이하로 증가하거나, 최소한 급격한 비선형 폭증이 없음.


## 2026-03-08 웹서버 고갈 대응 개선 정리

### 목적

- 느린 다운로드 클라이언트가 대량으로 붙는 상황에서 서버가 최대한 많은 요청을 안전하게 처리하도록 튜닝.
- 다운로드 트래픽 폭주 시에도 경량 API(`/health`, `/files`)가 같이 무너지는 현상을 완화.
- 비협조적인 클라이언트(즉시 재시도 포함)를 가정하고, 서버 측에서 선제적으로 과부하를 제어.

### 추가한 기능/설정

- Tomcat 수용 한도 설정 추가 (`application.yml`):
    - `server.tomcat.threads.max`
    - `server.tomcat.threads.min-spare`
    - `server.tomcat.accept-count`
    - `server.tomcat.max-connections`
- 다운로드 엔드포인트 보호 강화 (`FileController`):
    - `Semaphore.tryAcquire()` -> `tryAcquire(timeout)`으로 변경해 짧은 버스트를 흡수.
    - 클라이언트(IP) 단위 고정 윈도우 rate limit 추가.
    - 제한 초과 시 `429 Too Many Requests` + `Retry-After: 1` 반환.
- 운영 튜닝 가능한 설정 추가 (`application.yml`):
    - `app.download.acquire-timeout-ms`
    - `app.download.rate-limit.enabled`
    - `app.download.rate-limit.requests-per-second`

### 확인된 개선점

- 기존에는 다운로드 요청이 거의 모두 `503`으로 거절됨.
    - 이전 측정 예시: download `503=144,176` (혹은 `78,232`), 오류율 `~99%`대.
- 서버 방어 로직 적용 후 `503`이 크게 감소하고, 과부하는 주로 `429`로 흡수됨.
    - 적용 후 측정 예시: download `429=125,876`, `503=820`.
    - download 오류율(취소 제외) `0.41%` 수준으로 하락.
- 리소스 관점:
    - 메모리는 안정적(약 9~12%)이고, CPU는 2 vCPU 한계 근처(`~200%`)를 지속 사용.
    - 즉, 메모리 고갈보다 CPU/요청 처리 경로가 주 병목임을 재확인.

### 남은 과제

- 경량 API p95 배수(기준 대비)가 아직 큼(대략 18~20배).
- 현재 download 경로에서 rate limit 적용 시점이 DB 조회 뒤에 있어, 제한 요청도 일부 백엔드 비용이 발생.
- 다음 개선 우선순위:
    - rate limit 체크를 더 앞단으로 이동.
    - `APP_DOWNLOAD_RATE_LIMIT_RPS` / 동시성 상한 스윕으로 처리량-지연 균형점 탐색.


### 적용한 추가 수정

- `FileController.download` 경로에서 rate limit 체크를 DB 조회보다 앞단으로 이동.
- 목적: `429`로 차단될 요청이 `fileService.getDownloadFile(id)`까지 내려가지 않도록 하여 백엔드 비용 절감.

### 최신 실험 결과 요약

- 실행 조건: `stages=100:10s,200:10s,300:10s`, `slow-sleep=100ms`, `probe-workers=2`.
- 결과:
    - `GET /files/{id}/download` 상태코드 분포: `429=63,540`, `503=820`, `0=920`, `200=1`.
    - download 오류율(취소 제외): `1.79%`.
    - `GET /health` p95: `586ms` (baseline 대비 `145.58x`).
    - `GET /files` p95: `797ms` (baseline 대비 `136.60x`).

### 해석

- DB 보호 관점:
    - 의도한 대로 차단 트래픽의 DB 접근을 줄이는 방향으로 개선.
- 서비스 지연 관점:
    - 경량 API p95는 오히려 크게 악화.
    - 2 vCPU 환경에서 대량의 차단 응답(`429`) 처리 자체가 스레드/소켓/스케줄링 비용을 유발해 API 지연이 커진 것으로 판단.

### 다음 튜닝 제안

- 2 vCPU 기준 Tomcat 기본값을 더 보수적으로 조정해 과도한 스레드 경쟁을 완화:
    - `server.tomcat.threads.max=64`
    - `server.tomcat.threads.min-spare=8`
    - `server.tomcat.accept-count=200`
    - `server.tomcat.max-connections=512`
- 앱 내부 rate limit 외에, 가능하면 L7 앞단(Nginx/Envoy)에서 선차단(rate/conn limit) 적용 검토.


## 2026-03-08 디스크 I/O 병목 대응 작업 정리

### 이번에 실제 적용한 변경

- `loadtest/io_stress.go` 추가
    - 대용량 업로드/다운로드 혼합 I/O 부하 생성기 추가.
    - stage 기반 동시성 제어, prepare 업로드, p95/p99/상태코드/throughput 리포트 포함.
- `metric/collect_server_metrics.sh` 확장
    - 기존 수집(`docker_stats.csv`, `socket_established.csv`, `cgroup_io.csv`) 유지.
    - `host_cpu.csv` 추가: host iowait 증분/비율 수집.
    - `psi_io.csv` 추가: 컨테이너 `/proc/pressure/io` (some/full) 수집.
- `FileController.upload` 보호 로직 추가
    - 업로드 동시성 제한(`Semaphore.tryAcquire(timeout)`) 추가.
    - 업로드 in-flight bytes 상한(`InFlightByteLimiter`) 추가.
    - 초과 시 `503` + `Retry-After: 1` 반환.
- `application.yml`에 업로드 보호 설정 추가
    - `app.upload.max-concurrency`
    - `app.upload.acquire-timeout-ms`
    - `app.upload.max-inflight-bytes`
- `deploy/docker-compose.yaml` 튜닝 반영
    - upload: `APP_UPLOAD_MAX_CONCURRENCY=8`, `APP_UPLOAD_ACQUIRE_TIMEOUT_MS=100`, `APP_UPLOAD_MAX_INFLIGHT_BYTES=1073741824`
    - download: `APP_DOWNLOAD_MAX_CONCURRENCY` / `APP_DOWNLOAD_RATE_LIMIT_RPS` 스윕(32/15 -> 24/12)

### 실험 요약 (io_stress.go)

- 조건 A: `stages=10:10s,20:10s,40:10s`, `50~300MB`, read 80%
    - 결과(개선 전/중 일부):
        - download 오류율 50~61%대, p95 17~20s 구간 발생.
        - upload도 일부 구간에서 오류율 급등(최대 32%) 및 p95 10s+ 발생.
    - 해석: 2 vCPU + 단일 디스크 환경에서 부하 강도가 과함. 보호 로직이 동작해도 포화 자체는 해소되지 않음.
- 조건 B: `stages=6:10s,12:10s,24:10s`, `50~150MB`, read 80%
    - 대표 결과 1:
        - download `errorRate=21.24%`, `p95=9.06s`
        - upload `errorRate=0%`, `p95=3.21s`
        - aggregate throughput `407.18 MiB/s`
    - 대표 결과 2:
        - download `errorRate=23.53%`, `p95=9.32s`
        - upload `errorRate=0%`, `p95=5.40s`
        - aggregate throughput `378.53 MiB/s`
    - 해석: A 대비 의미 있는 개선. 다만 download `status=0`가 지속되어 완전 안정 구간은 아님.

### 현재 판단

- 개선 여부: "부분 개선"은 명확.
    - upload 안정성은 크게 개선(오류율 0% 유지 구간 확인).
    - download tail/오류율은 개선되었지만 여전히 높음(약 21~24%대).
- 병목 성격:
    - 앱 내부 백프레셔 + 동시성 제한으로 완화는 가능.
    - 근본적으로는 디스크 I/O 및 스트리밍 연결 유지 비용이 지배적인 구간 존재.

### 현재 권장 실험 프로파일 (재현 기준)

- 서버 환경변수(현재 후보)
    - `APP_UPLOAD_MAX_CONCURRENCY=8`
    - `APP_UPLOAD_ACQUIRE_TIMEOUT_MS=100`
    - `APP_UPLOAD_MAX_INFLIGHT_BYTES=1073741824`
    - `APP_DOWNLOAD_MAX_CONCURRENCY=24`
    - `APP_DOWNLOAD_RATE_LIMIT_RPS=12`
- 부하 실행 조건(권장)
    - `stages=6:10s,12:10s,24:10s`
    - `read-ratio=0.8`
    - `upload-min-mb=50`, `upload-max-mb=150`

### 다음 액션

- 동일 조건 3회 반복 후 median 기준으로 비교(단일 run 편차 제거).
- `io_stress.go`에서 `status=0`을 원인별(`context canceled`, transport error)로 분리 집계.
- `host_cpu.csv`(iowait) + `psi_io.csv`(some/full)와 API 지표를 같은 타임라인으로 붙여 상관관계 확인.


## 2026-03-08 큐잉 적용 + 측정 해석 보정 추가 정리

### 코드/설정 변경 (추가)

- `FileController` 다운로드 경로를 큐 기반으로 전환
    - 기존: 실행 슬롯 부족 시 즉시 `503`.
    - 변경: 연결 유지 후 큐에 적재, 선행 작업 완료 시 FIFO 순차 처리.
    - 핵심 설정:
        - `app.download.max-queue-bytes` (기본 1GiB)
        - `app.download.max-queue-requests`
        - `app.download.queue-timeout-ms`
- `FileController` 업로드 경로도 동일 큐 패턴으로 확장
    - 기존 `upload` 보호(`max-concurrency`, `max-inflight-bytes`)는 유지.
    - 실행 슬롯/바이트 여유가 없으면 큐 적재 후 순차 처리.
    - 큐 한도 초과/큐 대기 timeout 시 `503 + Retry-After: 1`.
    - 핵심 설정:
        - `app.upload.max-queue-bytes` (기본 1GiB)
        - `app.upload.max-queue-requests`
        - `app.upload.queue-timeout-ms`
- `deploy/docker-compose.yaml` 튜닝 반영
    - download:
        - `APP_DOWNLOAD_MAX_QUEUE_BYTES=1073741824`
        - `APP_DOWNLOAD_MAX_QUEUE_REQUESTS=200`
        - `APP_DOWNLOAD_QUEUE_TIMEOUT_MS=20000`
        - `APP_DOWNLOAD_RATE_LIMIT_RPS=40`
    - upload:
        - `APP_UPLOAD_MAX_QUEUE_BYTES=1073741824`
        - `APP_UPLOAD_MAX_QUEUE_REQUESTS=200`
        - `APP_UPLOAD_QUEUE_TIMEOUT_MS=20000`

### 부하 도구 측정 보정

- `loadtest/io_stress.go`에서 `status=0` 분리 집계 추가
    - `context_canceled`
    - `deadline_exceeded`
    - `transport_error`
    - `status0_unknown`
- 에러율 계산 시 `context_canceled`는 실패에서 제외.
    - stage 종료 시 취소 노이즈를 실제 장애와 분리해 해석 가능하도록 수정.

### 최신 실행 결과 해석 (6/12/24, 50~150MB, read 80%)

- 결과 요약:
    - `GET /files/{id}/download`
        - `count=105`, `errors=0`, `errorRate=0.00%`
        - `status codes: 200:83, 0:22`
        - `status=0 breakdown: context_canceled:22`
        - `p95/p99`가 `~10s` 근처로 표시
    - `POST /files/upload`
        - `count=28`, `errors=0`, `errorRate=0.00%`
        - `status codes: 200:27, 0:1`
        - `status=0 breakdown: context_canceled:1`
- 해석:
    - 이전에 오류처럼 보이던 `status=0` 다수가 실제 서버 실패가 아니라 stage 종료 취소임을 확인.
    - 현재 프로파일에서는 실질 오류율(취소 제외) 관점에서 upload/download 모두 안정 구간 진입.
    - download `p95/p99 ~10s`는 stage 길이(10s) 종료 영향이 커서, 실제 서비스 tail과 1:1 대응 해석은 주의 필요.

### 후속 권장

- stage 길이를 늘린 재측정 (`6:30s,12:30s,24:30s`)으로 취소 노이즈를 추가 완화.
- 필요 시 리포트에 "취소 제외 latency percentile"를 병행 표기해 비교 정확도 향상.


## 후속

### 큐잉 로직 재검증 결과

- 검증 결론: 의도한 흐름(`요청 -> 연결 유지 -> 큐 등록 -> 선행 완료 후 pop 처리`)이 구현되어 있음.
- 연결 유지:
    - download: `DeferredResult<ResponseEntity<StreamingResponseBody>>` 사용.
    - upload: `DeferredResult<ResponseEntity<FileUploadResponse>>` 사용.
- 큐 등록:
    - 실행 슬롯/바이트 여유 없으면 큐 적재.
    - download는 `ConcurrentLinkedQueue`, upload는 `ArrayDeque(+lock)` 사용.
- pop + 순차 처리:
    - `drainDownloadQueue()` / `drainUploadQueue()`에서 큐에서 꺼내 다음 요청 처리.
    - 처리 완료 시 `finally`에서 permit/bytes 반납 후 drain 재호출.
- 연결 종료 시 큐 정리:
    - `onTimeout`, `onError`에서 `cancelQueued...()` 호출로 대기열 항목 제거.

### 튜닝 인자 정리 (CPU/MEM/연결수 기준)

1) 연결 수용 한도 계층

- `server.tomcat.max-connections`
- `server.tomcat.accept-count`
- `server.tomcat.threads.max`
- `server.tomcat.threads.min-spare`
- `app.download.async.request-timeout-ms`

2) 실제 실행 슬롯 계층 (우선 튜닝)

- `app.download.max-concurrency`
- `app.upload.max-concurrency`
- `app.upload.max-inflight-bytes`

3) 큐 용량 계층

- `app.download.max-queue-requests`
- `app.download.max-queue-bytes`
- `app.download.queue-timeout-ms`
- `app.upload.max-queue-requests`
- `app.upload.max-queue-bytes`
- `app.upload.queue-timeout-ms`

4) 유입 제어 계층

- `app.download.rate-limit.enabled`
- `app.download.rate-limit.requests-per-second`

### 실무 튜닝 순서 (권장)

1. 먼저 `max-concurrency`를 CPU/디스크에 맞게 조정.
2. 다음으로 `max-queue-requests`를 조정해 조기 503 vs tail latency 균형점 탐색.
3. `queue-timeout-ms` 조정(짧으면 503 증가, 길면 p95/p99 증가).
4. 마지막으로 `rate-limit RPS` 조정.

### 지표 기반 빠른 의사결정 가이드

- CPU 포화(`>85%` 지속): `max-concurrency` 하향.
- 503 증가 + CPU 여유: `max-queue-requests`/`queue-timeout-ms` 상향 검토.
- 오류율 낮고 p95/p99만 큼: 큐 과대 가능성, `max-queue-requests`/`queue-timeout-ms` 하향 검토.
- 429 다수: `rate-limit RPS` 상향 또는 앞단(L7) 선차단 정책 재조정.

### 현재 적용값(메모)

- download
    - `APP_DOWNLOAD_MAX_CONCURRENCY=24`
    - `APP_DOWNLOAD_MAX_QUEUE_BYTES=1073741824`
    - `APP_DOWNLOAD_MAX_QUEUE_REQUESTS=200`
    - `APP_DOWNLOAD_QUEUE_TIMEOUT_MS=20000`
    - `APP_DOWNLOAD_RATE_LIMIT_RPS=40`
- upload
    - `APP_UPLOAD_MAX_CONCURRENCY=8`
    - `APP_UPLOAD_MAX_INFLIGHT_BYTES=1073741824`
    - `APP_UPLOAD_MAX_QUEUE_BYTES=1073741824`
    - `APP_UPLOAD_MAX_QUEUE_REQUESTS=200`
    - `APP_UPLOAD_QUEUE_TIMEOUT_MS=20000`

### 주의점

- upload는 큐 대기 중 `MultipartFile` 요청이 살아 있어 임시 디스크/메모리 점유가 커질 수 있음.
- 따라서 upload 큐(`max-queue-requests`, `queue-timeout-ms`)는 download보다 보수적으로 유지하는 편이 안전.

## 2026-03-08 Nginx 이관 이슈 정리

### 목표

- 파일 업로드/다운로드의 바이트 처리 경로를 Kotlin에서 Nginx(OpenResty)로 이관.
- Kotlin은 메타데이터 저장/조회 및 훅 수신만 담당.

### 반영한 구조

- `deploy/docker-compose.yaml`
    - `nginx` 서비스 추가 및 `../data/uploads:/app/data/uploads` 공유 마운트 적용.
    - 외부 포트 매핑 제거, 내부 노출만 유지: `expose: ["8080"]`.
- `nginx/conf.d/default.conf`
    - `listen 8080`.
    - `resolver 127.0.0.11 ipv6=off` 추가(Docker DNS for Lua cosocket).
    - `/files/upload`, `/files/{id}/download`를 Lua 핸들러로 처리.
    - 내부 라우트:
        - `/_meta/upload` -> `server-debug:8080/internal/files/upload-metadata`
        - `/_meta/download/{id}` -> `server-debug:8080/internal/files/{id}/download-metadata`
        - `/_legacy/upload` -> `server-debug:8080/files/upload` (multipart 호환)
- `nginx/lua`
    - `upload.lua`, `download.lua`, `on_request.lua`, `before_response.lua`, `meta_http.lua` 추가/수정.
    - `on_request.lua`, `before_response.lua`에서 `package.path`에 `/etc/nginx/lua`를 prepend 후 `require("meta_http")`.

### 발생한 문제와 원인

1. `module 'meta_http' not found`
- 원인: OpenResty 기본 `package.path`에 `/etc/nginx/lua`가 없음.
- 조치: `on_request.lua`, `before_response.lua`에서 `package.path` 보강.

2. `no resolver defined to resolve "server-debug"`
- 원인: `ngx.timer` 내부 cosocket DNS 해석기 미설정.
- 조치: Nginx `server` 블록에 `resolver 127.0.0.11 ipv6=off` 추가.

3. `BASE_URL=nginx:8080 ./test.sh` 실행 시 업로드 전부 실패
- 증상: `X-User-Id, X-File-Path, X-File-Name headers are required`.
- 원인: Nginx `upload.lua`는 raw+header 기반, E2E는 `multipart/form-data`(`curl -F`) 사용.
- 조치: `upload.lua`에서 multipart 감지 시 `ngx.exec("/_legacy/upload")`로 Kotlin 기존 업로드 API 내부 전달.

4. 같은 스크립트가 한 번은 성공, 한 번은 실패
- 핵심 차이: `./test.sh` 기본값(`http://localhost:8080`)은 Kotlin 직통, `BASE_URL=nginx:8080`은 Nginx 경유.
- 조치: Nginx 경유에서도 Kotlin 직통과 동일한 multipart 의미를 갖도록 호환 라우팅 적용.

### E2E 실행 규칙

- 서버 컨테이너에서 Nginx 경유 테스트 시 권장:
    - `BASE_URL=http://nginx:8080 ./test.sh`
- 스킴(`http://`)을 붙여 URL 파싱 혼선을 방지.

### 현재 상태

- Nginx는 내부 `8080/tcp`로 정상 기동.
- `server-debug` 컨테이너에서 `http://nginx:8080` 접근 가능.
- 최근 수정 후 `meta_http not found`, `no resolver defined` 에러 재발 없음.
- 업로드 경로는 다음 2가지 모두 지원:
    - raw body + `X-User-Id/X-File-Path/X-File-Name`
    - multipart/form-data (`curl -F`, 기존 E2E 방식)