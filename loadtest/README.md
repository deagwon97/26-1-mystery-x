# loadtest — 파일 서버 벤치마크

파일 서버의 사용자 관점 성능을 6개 시나리오로 평가하는 벤치마크 도구입니다.

## 시나리오

| 순서 | 시나리오 | 설명 |
|------|----------|------|
| 1 | Upload | 파일 동시 업로드 (multipart / raw) |
| 2 | Download | 동시 다운로드 + SHA-256 해시 검증 |
| 3 | Folder List | 폴더 목록 조회 + 파일 수 검증 |
| 4 | Move Folder | 폴더 이동 + 원본/대상 경로 검증 |
| 5 | Delete Files | 개별 파일 동시 삭제 + 빈 폴더 검증 |
| 6 | Delete Folder | 폴더 단위 삭제 + 하위 전체 삭제 검증 |

## 테스트 파일 케이스 (총 ~15GB)

| 케이스 | 크기 | 파일 수 | 동시성 |
|--------|------|---------|--------|
| small | 3 MB | 1000 | 1000 |
| medium | 30 MB | 100 | 100 |
| large | 100 MB | 30 | 30 |
| xlarge | 300 MB | 10 | 10 |
| huge | 500 MB | 6 | 6 |

## 빠른 시작

### 1. 컨테이너 실행

```bash
cd loadtest
docker compose up -d
docker exec -it loadtest bash
```

### 2. 빌드

```bash
bash build.sh
```

두 개의 바이너리가 생성됩니다:
- `create-test-files` — 테스트 파일 생성 도구
- `benchmark` — 벤치마크 실행 도구

### 3. 테스트 파일 생성

```bash
./create-test-files -data-dir ./bench-data -seed 42
```

동일한 seed를 사용하면 동일한 파일이 생성됩니다 (바이트 단위 재현).
이미 존재하는 파일은 건너뜁니다.

### 4. 벤치마크 실행

```bash
./benchmark -host http://server-debug:8080 -user-id bench-user
```

## CLI 플래그

### benchmark

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `-host` | `http://localhost:8080` | 대상 서버 URL |
| `-user-id` | `bench-user` | 테스트용 userId |
| `-upload-mode` | `multipart` | 업로드 방식 (`multipart` / `raw`) |
| `-data-dir` | `./bench-data` | 테스트 파일 디렉토리 |
| `-seed` | `42` | 파일 생성 랜덤 시드 |
| `-timeout` | `5m` | 요청별 타임아웃 |

### create-test-files

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `-data-dir` | `./bench-data` | 출력 디렉토리 |
| `-seed` | `42` | 랜덤 시드 (benchmark와 동일하게 맞출 것) |

## 배포 환경에서 실행

`BuChoiPark/deploy/docker-compose.yaml`에 loadtest 서비스가 포함되어 있습니다.
서버와 같은 네트워크에서 실행됩니다.

```bash
cd BuChoiPark/deploy
docker compose up -d
docker exec -it buchoipark-loadtest bash
cd /work/loadtest/src
bash build.sh
./create-test-files
./benchmark -host http://nginx:8080
```


### Example Output

```
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SCENARIO: Upload (multipart)
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Case       Files  Conc  Total(s)  Avg(ms)  P50(ms)  P95(ms)  P99(ms)  Min(ms)  Max(ms)     MB/s    OK  Fail
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  small       1000  1000     12.63   7959.6   6186.0  12212.0  12515.1    272.0  12629.0     52.5   221   779
  medium       100   100     28.84  23867.9  24880.5  28712.1  28830.1   4792.0  28836.0     49.9    48    52
  large         30    30     31.07  27227.6  26744.5  30734.8  31066.9  17797.0  31071.0     57.9    18    12
  xlarge        10    10     35.61  29845.6  30840.5  35595.6  35610.3  18492.0  35614.0     75.8     9     1
  huge           6     6     37.00  30999.2  33516.5  36984.5  36998.5  18488.0  37002.0     81.1     6     0
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════

=== Scenario: Download ===
  [small] downloading 221 files (concurrency=221)...
  [medium] downloading 48 files (concurrency=48)...
  [large] downloading 18 files (concurrency=18)...
  [xlarge] downloading 9 files (concurrency=9)...
  [huge] downloading 6 files (concurrency=6)...
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SCENARIO: Download
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Case       Files  Conc  Total(s)  Avg(ms)  P50(ms)  P95(ms)  P99(ms)  Min(ms)  Max(ms)     MB/s    OK  Fail Hash✗
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  small        221   221      2.58   1519.2   1544.0   2560.0   2574.0    105.0   2577.0    257.1   221     0     0
  medium        48    48      7.05   4259.2   4829.5   6999.0   7034.8    106.0   7047.0    204.3    48     0     0
  large         18    18      7.46   5338.5   6482.5   7453.4   7459.5    321.0   7461.0    241.2    18     0     0
  xlarge         9     9     11.36   8156.7  10418.0  11350.2  11358.8   2020.0  11361.0    237.7     9     0     0
  huge           6     6      8.40   8174.8   8183.5   8380.0   8393.6   7896.0   8397.0    357.2     6     0     0
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════

=== Scenario: Folder List ===
    VERIFY FAIL /bench/medium/folderB: expected 25, got 52
    VERIFY FAIL /bench/small/folderC: expected 34, got 59
    VERIFY FAIL /bench/small/folderD: expected 17, got 33
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SCENARIO: Folder List
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Case             Reqs  Total(s)  Avg(ms)  P50(ms)  P95(ms)  P99(ms)  Min(ms)  Max(ms)    OK  Fail Verified
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  all folders        10      0.04      3.4      2.0     10.2     15.7      1.0     17.0     7     0        ✗
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════

=== Scenario: Move Folder ===
  [small/folderA] moving /bench/small/folderA → /bench/small/moved_folderA (250 files)...
    VERIFY FAIL: new path has 135 files, expected 250
  [small/folderB] moving /bench/small/folderB → /bench/small/moved_folderB (250 files)...
    VERIFY FAIL: new path has 35 files, expected 250
  [medium/folderA] moving /bench/medium/folderA → /bench/medium/moved_folderA (50 files)...
    VERIFY FAIL: new path has 23 files, expected 50
  [large/folderA] moving /bench/large/folderA → /bench/large/moved_folderA (15 files)...
    VERIFY FAIL: new path has 10 files, expected 15
  [xlarge (parent)] moving /bench/xlarge → /bench/xlarge_moved (10 files)...
    VERIFY FAIL: new path has 1 files, expected 10
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SCENARIO: Move Folder
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Case                         Files  Latency(ms) Verified
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  small/folderA                  250          7.0        ✗
  small/folderB                  250          6.0        ✗
  medium/folderA                  50          5.0        ✗
  large/folderA                   15          4.0        ✗
  xlarge (parent)                 10          5.0        ✗
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════

=== Scenario: Delete Files ===
  [small/folderC] listing files in /bench/small/folderC...
  [small/folderC] deleting 59 files (concurrency=59)...
    VERIFY FAIL: folder still has 59 files
  [small/folderD] listing files in /bench/small/folderD...
  [small/folderD] deleting 33 files (concurrency=33)...
    VERIFY FAIL: folder still has 33 files
  [medium/folderB] listing files in /bench/medium/folderB...
  [medium/folderB] deleting 52 files (concurrency=52)...
    VERIFY FAIL: folder still has 52 files
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SCENARIO: Delete Files
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Case                   Files  Conc  Total(s)  Avg(ms)  P50(ms)  P95(ms)  P99(ms)    OK  Fail Verified
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  small/folderC             59    59      0.03     16.1     16.0     27.0     27.4     0    59        ✗
  small/folderD             33    33      0.02      8.8      8.0     14.0     14.0     0    33        ✗
  medium/folderB            52    52      0.02     11.3     11.5     20.0     21.0     0    52        ✗
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════

=== Scenario: Delete Folder ===
  [moved_folderA (small)] deleting folder /bench/small/moved_folderA (~250 files)...
    INFO: parent /bench/small has 3 remaining files
  [moved_folderB (small)] deleting folder /bench/small/moved_folderB (~250 files)...
    INFO: parent /bench/small has 2 remaining files
  [moved_folderA (medium)] deleting folder /bench/medium/moved_folderA (~50 files)...
    INFO: parent /bench/medium has 1 remaining files
  [moved_folderA (large)] deleting folder /bench/large/moved_folderA (~15 files)...
    INFO: parent /bench/large has 1 remaining files
  [folderB (large)] deleting folder /bench/large/folderB (~15 files)...
  [xlarge_moved] deleting folder /bench/xlarge_moved (~10 files)...
    INFO: parent /bench has 3 remaining files
  [huge] deleting folder /bench/huge (~6 files)...
    INFO: parent /bench has 2 remaining files
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SCENARIO: Delete Folder
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Case                         Files  Latency(ms) Verified
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  moved_folderA (small)          250         20.0        ✓
  moved_folderB (small)          250         12.0        ✓
  moved_folderA (medium)          50         22.0        ✓
  moved_folderA (large)           15         26.0        ✓
  folderB (large)                 15         22.0        ✓
  xlarge_moved                    10         75.0        ✓
  huge                             6        440.0        ✓
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════

════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  SUMMARY
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
  Scenario            Total(s)  Total Files    Success %        Avg MB/s
  ────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  Upload                145.16         1146        26.4%            63.4
  Download               36.85          302       100.0%           259.5
  Folder List             0.04           10       100.0%               —
  Move Folder             0.03          575       100.0%               —
  Delete Files            0.07          144         0.0%               —
  Delete Folder           0.62          596       100.0%               —
════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════
```