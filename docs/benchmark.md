# 벤치마크 가이드

## 개요

`loadtest` 는 파일 서버의 사용자 관점 성능을 평가하는 벤치마크 도구입니다.

### 테스트 시나리오

1. **Upload**: 파일 동시 업로드
2. **Download**: 동시 다운로드 + SHA-256 무결성 검증
3. **Folder List**: 폴더 목록 조회
4. **Move Folder**: 폴더 이동
5. **Delete Files**: 개별 파일 동시 삭제
6. **Delete Folder**: 폴더 단위 삭제

## 테스트 파일 구성

### 파일 케이스

| 케이스 | 크기 | 파일 수 | 동시성 | 총 크기 |
|--------|------|---------|--------|---------|
| small | 3 MB | 1000 | 1000 | 3 GB |
| medium | 30 MB | 100 | 100 | 3 GB |
| large | 100 MB | 30 | 30 | 3 GB |
| xlarge | 300 MB | 10 | 10 | 3 GB |
| huge | 500 MB | 6 | 6 | 3 GB |

**총 테스트 데이터: 약 15 GB**

### 폴더 구조

```
/bench/
├── small/
│   ├── folderA/          (250 개)
│   ├── folderB/          (250 개)
│   ├── folderC/          (250 개)
│   └── folderD/          (250 개)
├── medium/
│   ├── folderA/          (50 개)
│   └── folderB/          (50 개)
├── large/
│   ├── folderA/          (15 개)
│   └── folderB/          (15 개)
├── xlarge/
│   └── folderA/          (10 개)
└── huge/
    └── folderA/          (6 개)
```

## 빠른 시작

### 1. Docker 컨테이너 실행

```bash
cd loadtest
docker-compose up -d
docker exec -it loadtest bash
```

### 2. 빌드

```bash
bash build.sh
```

생성되는 바이너리:
- `create-test-files`: 테스트 파일 생성 도구
- `benchmark`: 벤치마크 실행 도구

### 3. 테스트 파일 생성

```bash
./create-test-files -data-dir ./bench-data -seed 42
```

**중요**: 동일한 seed 를 사용하면 동일한 파일이 생성됩니다 (재현성 보장).

### 4. 벤치마크 실행

```bash
./benchmark -host http://server-debug:8080 -user-id bench-user
```

## CLI 옵션

### benchmark

| 플래그 | 타입 | 기본값 | 설명 |
|--------|------|--------|------|
| `-host` | string | `http://localhost:8080` | 대상 서버 URL |
| `-user-id` | string | `bench-user` | 테스트용 userId |
| `-upload-mode` | string | `multipart` | 업로드 방식 (`multipart`/`raw`) |
| `-data-dir` | string | `./bench-data` | 테스트 파일 디렉토리 |
| `-seed` | int64 | `42` | 파일 생성 랜덤 시드 |
| `-timeout` | string | `5m` | 요청별 타임아웃 |

### create-test-files

| 플래그 | 타입 | 기본값 | 설명 |
|--------|------|--------|------|
| `-data-dir` | string | `./bench-data` | 출력 디렉토리 |
| `-seed` | int64 | `42` | 랜덤 시드 |

## 예시 출력

```
════════════════════════════════════════════════════════════════
                    BuChoiPark Benchmark Report
════════════════════════════════════════════════════════════════

[1/6] Upload - small (3MB x 1000 files)
  Total Time: 45.2s
  Throughput: 66.4 MB/s
  Success Rate: 100.0%
  Latency:
    Min: 120ms
    Avg: 450ms
    P50: 400ms
    P95: 800ms
    P99: 1.2s
    Max: 1.5s

[2/6] Download - small (3MB x 1000 files)
  Total Time: 30.1s
  Throughput: 99.7 MB/s
  Success Rate: 100.0%
  Hash Verification: PASSED
  Latency:
    Min: 50ms
    Avg: 200ms
    P50: 180ms
    P95: 350ms
    P99: 500ms
    Max: 600ms

...

════════════════════════════════════════════════════════════════
                         Summary
════════════════════════════════════════════════════════════════
Total Test Time: 5m 30s
Overall Success Rate: 100.0%
Average Upload Throughput: 65.2 MB/s
Average Download Throughput: 98.5 MB/s
```

## 결과 해석

### 성공적인 테스트 기준

1. **성공률**: 100% (모든 요청 성공)
2. **해시 검증**: PASSED (다운로드 파일 무결성 확인)
3. **처리량**: 서버 사양에 적합한 수준
4. **에러 없음**: 타임아웃, 연결 실패 없음

### 성능 지표

| 지표 | 설명 | 목표 |
|------|------|------|
| 동시성 | 동시 처리 요청 수 | 설정값 유지 |
| 처리량 | 초당 처리 데이터량 | 최대 500MB/s |
| P95 latency | 95% 요청이 완료된 시간 | < 1s |
| P99 latency | 99% 요청이 완료된 시간 | < 2s |

## 서버 사양별 기대 성능

### 4 Core / 8 GB

| 시나리오 | 기대 처리량 | 기대 시간 |
|----------|-------------|-----------|
| Upload (small) | 50-80 MB/s | ~60s |
| Download (small) | 100-150 MB/s | ~30s |
| 전체 테스트 | - | 10-15 분 |

### 8 Core / 16 GB

| 시나리오 | 기대 처리량 | 기대 시간 |
|----------|-------------|-----------|
| Upload (small) | 100-150 MB/s | ~30s |
| Download (small) | 200-300 MB/s | ~15s |
| 전체 테스트 | - | 5-10 분 |

## 문제 해결

### 1. 파일 생성 실패

```
Error: Failed to create test file
```

**해결**:
- 디스크 공간 확인 (`df -h`)
- 디렉토리 권한 확인 (`ls -la`)

### 2. 연결 실패

```
Error: Connection refused
```

**해결**:
- 서버 상태 확인 (`docker ps`)
- 헬스 체크 확인 (`curl http://server:8080/health`)

### 3. 타임아웃

```
Error: Request timeout after 5m
```

**해결**:
- `-timeout` 옵션 증가
- 서버 리소스 확인
- 동시성 설정 감소

### 4. 해시 검증 실패

```
Hash Verification: FAILED
```

**해결**:
- 네트워크 안정성 확인
- 저장소 무결성 확인
- `seed` 를 변경하여 테스트 파일 재생성

## 커스텀 테스트

### 특정 시나리오만 실행

```bash
# Upload 만 테스트
./benchmark -host http://server:8080 -user-id test -scenarios upload

# Download 만 테스트
./benchmark -host http://server:8080 -user-id test -scenarios download
```

### 동시성 조정

```bash
# 동시성 낮게 (저사양 서버용)
GOMAXPROCS=2 ./benchmark -host http://server:8080 -user-id test
```

### 업로드 방식 변경

```bash
# Raw 바디 방식 사용 (대용량 파일 최적화)
./benchmark -host http://server:8080 -user-id test -upload-mode raw
```

## 결과 저장

```bash
# JSON 결과 저장
./benchmark -host http://server:8080 -user-id test -output results.json

# CSV 결과 저장
./benchmark -host http://server:8080 -user-id test -output results.csv
```

## 비교 테스트

### 멀티파트 vs Raw 업로드

```bash
# 멀티파트 방식
./benchmark -host http://server:8080 -user-id test1 -upload-mode multipart

# Raw 방식
./benchmark -host http://server:8080 -user-id test2 -upload-mode raw
```

### 서버 설정 비교

```bash
# 설정 A 로 테스트
./benchmark -host http://server-a:8080 -user-id test -output results-a.json

# 설정 B 로 테스트
./benchmark -host http://server-b:8080 -user-id test -output results-b.json
```
