## benchmark

### resource spec
- cpu: 4 Core
- mem: 8 Gib
- disk: 50GB, 200GB

### 테스트 파일 케이스: 3G * 5
(예상되는 테스트 시간: 10분 ~ 15분)
- 3MB x 1000 concurrent
- 30MB x 100 concurrent
- 100MB x 30 concurrent
- 300MB x 10 concurrent
- 500MB x 6 concurrent

### TEST Endpoints
- POST /files/upload	파일 업로드
- GET /files/{id}/download	파일 다운로드
- GET /files/folder	폴더 내 파일 목록 조회
- POST /files/move-folder 폴더 이동
- DELETE /files	단일 파일 삭제
    - 다중 파일 삭제 -> 사용자 관점에서 삭제되는데 걸리는 시간
- DELETE /files/folder	폴더 삭제
    - 다중 파일 삭제 -> 사용자 관점에서 삭제되는데 걸리는 시간

---

## benchmark.go 상세 스펙

`benchmark.go` 는 파일 서버의 사용자 관점 성능을 6개 시나리오로 평가하는 단일 파일 벤치마크입니다.

---

### CLI 플래그

| 플래그 | 타입 | 기본값 | 설명 |
|--------|------|--------|------|
| `-host` | string | `http://localhost:8080` | 대상 서버 URL |
| `-user-id` | string | `bench-user` | 테스트용 고정 userId |
| `-upload-mode` | string | `multipart` | 업로드 방식 (`multipart` / `raw`) |
| `-data-dir` | string | `./bench-data` | 테스트 파일 생성/저장 디렉토리 |
| `-seed` | int64 | `42` | 테스트 파일 생성용 랜덤 시드 (재현성 보장) |

---

### 테스트 파일 생성 (genfiles 모드)

벤치마크 시작 전, 테스트 파일이 `-data-dir`에 존재하지 않으면 자동 생성합니다.
이미 존재하면 재사용합니다.

- 동일한 seed → 동일한 파일 내용 (바이트 단위 재현)
- 파일명 패턴: `{size}_{index}.bin` (예: `3mb_0001.bin`, `500mb_0003.bin`)
- 각 파일의 SHA-256 해시를 메모리에 보관 (다운로드 검증용)

| 케이스 | 파일 크기 | 파일 수 |
|--------|----------|---------|
| small  | 3 MB     | 1000    |
| medium | 30 MB    | 100     |
| large  | 100 MB   | 30      |
| xlarge | 300 MB   | 10      |
| huge   | 500 MB   | 6       |

---

### 폴더 구조 설계

업로드 시 파일을 다양한 폴더에 분산 배치하여, move/delete 시나리오에 충분한 케이스를 확보합니다.

```
/bench/
├── small/
│   ├── folderA/          (250개)
│   ├── folderB/          (250개)
│   ├── folderC/          (250개)
│   └── folderD/          (250개)
├── medium/
│   ├── folderA/          (50개)
│   └── folderB/          (50개)
├── large/
│   ├── folderA/          (15개)
│   └── folderB/          (15개)
├── xlarge/
│   └── folderA/          (10개)
└── huge/
    └── folderA/          (6개)
```

---

### 시나리오 실행 순서 및 상세

모든 시나리오는 순차 실행됩니다. 각 시나리오 내에서 5개 파일 케이스별로 개별 측정합니다.

#### 1. Upload

각 케이스별로 모든 파일을 **동시(concurrent)** 업로드합니다.

- 동시성 = 해당 케이스의 파일 수 (3MB→1000, 30MB→100, ...)
- 응답에서 받은 `id`를 파일별로 저장 (이후 시나리오에서 사용)
- 측정: 전체 소요 시간, 개별 요청 latency (min/avg/max/p50/p95/p99), 처리량(MB/s), 성공/실패 수

#### 2. Download

업로드된 모든 파일을 케이스별로 동시 다운로드합니다.

- 동시성 = 해당 케이스의 파일 수
- **검증**: 다운로드된 바이트 수 확인 + SHA-256 해시 비교 (원본과 일치 여부)
- 측정: 전체 소요 시간, 개별 요청 latency, 처리량(MB/s), 성공/실패/검증실패 수

#### 3. Folder List

각 폴더 경로에 대해 목록 조회 요청을 수행합니다.

- 조회 대상: 위 폴더 구조의 모든 폴더 (leaf + parent 포함)
- **검증**: 반환된 파일 수가 업로드한 파일 수와 일치하는지 확인
- 측정: 전체 소요 시간, 개별 요청 latency, 성공/실패/검증실패 수

#### 4. Move Folder

다양한 폴더 이동 케이스를 실행합니다.

| 케이스 | from | to | 설명 |
|--------|------|----|------|
| small 분할 이동 | `/bench/small/folderA` | `/bench/small/moved_folderA` | 250개 파일 이동 |
| small 분할 이동 | `/bench/small/folderB` | `/bench/small/moved_folderB` | 250개 파일 이동 |
| medium 이동 | `/bench/medium/folderA` | `/bench/medium/moved_folderA` | 50개 파일 이동 |
| large 이동 | `/bench/large/folderA` | `/bench/large/moved_folderA` | 15개 파일 이동 |
| 상위 폴더 이동 | `/bench/xlarge` | `/bench/xlarge_moved` | 하위 전체 이동 |

- 각 이동 후 **검증**: 원본 경로 folder list → 0개, 새 경로 folder list → 이동한 파일 수 확인
- 측정: 개별 이동 요청 latency, 검증 결과

#### 5. Delete Files (단일 파일 삭제)

이동 완료 후 남은 파일 중 일부를 개별 삭제합니다.

- `small/folderC`의 파일 250개를 동시 개별 삭제
- `small/folderD`의 파일 250개를 동시 개별 삭제
- `medium/folderB`의 파일 50개를 동시 개별 삭제
- **검증**: 삭제 후 해당 폴더 list → 0개 확인
- 측정: 전체 소요 시간, 개별 요청 latency, 성공/실패 수

#### 6. Delete Folder (폴더 삭제)

남은 폴더를 폴더 단위로 삭제합니다. 하위의 모든 파일/폴더가 재귀적으로 삭제되는지 테스트합니다.

| 케이스 | 대상 | 예상 하위 파일 수 | 설명 |
|--------|------|------------------|------|
| moved small A | `/bench/small/moved_folderA` | 250 | 이동된 폴더 삭제 |
| moved small B | `/bench/small/moved_folderB` | 250 | 이동된 폴더 삭제 |
| moved medium | `/bench/medium/moved_folderA` | 50 | 이동된 폴더 삭제 |
| moved large | `/bench/large/moved_folderA` | 15 | 이동된 폴더 삭제 |
| large folderB | `/bench/large/folderB` | 15 | 원본 폴더 삭제 |
| xlarge 전체 | `/bench/xlarge_moved` | 10 | 상위 이동 폴더 삭제 |
| huge 전체 | `/bench/huge` | 6 | 대용량 폴더 삭제 |

- **검증**: 삭제 후 해당 경로 folder list 조회 → 빈 배열 또는 404 확인
- **검증**: 상위 경로(`/bench`) folder list에서 삭제된 폴더가 보이지 않는지 확인
- 측정: 개별 폴더 삭제 latency, 검증 결과

---

### 결과 출력

각 시나리오 완료 시 CLI에 결과 테이블을 출력합니다.

```
═══════════════════════════════════════════════════════════════
  SCENARIO: Upload (multipart)
═══════════════════════════════════════════════════════════════
  Case      Files  Concurrency  Total(s)  Avg(ms)  P50(ms)  P95(ms)  P99(ms)  Min(ms)  Max(ms)  MB/s     OK   Fail
  ────────  ─────  ───────────  ────────  ───────  ───────  ───────  ───────  ───────  ───────  ─────  ─────  ────
  small     1000   1000          12.34     12.3     11.0     25.0     45.0     5.0      80.0     243.1  1000   0
  medium    100    100            8.56     85.6     80.0    120.0    150.0    60.0     200.0     350.5   100   0
  large     30     30            10.23    341.0    330.0    400.0    450.0   280.0     500.0     293.2    30   0
  xlarge    10     10            15.67   1567.0   1500.0   1800.0   1900.0  1200.0    2000.0    191.4    10   0
  huge      6      6            20.12   3353.3   3300.0   3600.0   3700.0  3000.0    3800.0    149.1     6   0
═══════════════════════════════════════════════════════════════

═══════════════════════════════════════════════════════════════
  SCENARIO: Download
═══════════════════════════════════════════════════════════════
  Case      Files  Total(s)  Avg(ms)  P50  P95  P99  Min  Max  MB/s   OK   Fail  HashFail
  ...

═══════════════════════════════════════════════════════════════
  SCENARIO: Move Folder
═══════════════════════════════════════════════════════════════
  Case              Files  Latency(ms)  Verified
  ────────────────  ─────  ───────────  ────────
  small/folderA→    250    123.4        ✓
  ...

═══════════════════════════════════════════════════════════════
  SCENARIO: Delete Folder
═══════════════════════════════════════════════════════════════
  Case              Files  Latency(ms)  Verified
  ────────────────  ─────  ───────────  ────────
  moved_folderA     250    234.5        ✓
  ...
```

최종 요약:

```
═══════════════════════════════════════════════════════════════
  SUMMARY
═══════════════════════════════════════════════════════════════
  Scenario       Total Time(s)  Total Files  Success Rate  Avg Throughput
  ─────────────  ─────────────  ───────────  ────────────  ──────────────
  Upload          66.92          1146         100.0%        243.1 MB/s
  Download        55.30          1146         100.0%        312.5 MB/s
  Folder List      1.23            11         100.0%        —
  Move Folder      2.45           575         100.0%        —
  Delete Files     3.21           550         100.0%        —
  Delete Folder    4.56           596         100.0%        —
═══════════════════════════════════════════════════════════════
```

---

## API 스펙 (대상 서버)

### POST /files/upload — 파일 업로드

**multipart 방식** (기본, `-upload-mode multipart`)

```
POST /files/upload
Content-Type: multipart/form-data

Form fields:
  userId   string  — 소유 유저 ID
  filePath string  — 가상 경로 (예: /docs/report.pdf)
  file     binary  — 파일 본문
```

**raw 방식** (`-upload-mode raw`, Nginx Lua 직저장 경로)

```
POST /files/upload
Content-Type: application/octet-stream
X-User-Id:   <userId>
X-File-Path: <filePath>
X-File-Name: <fileName>

Body: raw binary
```

**응답**
```json
{ "id": "<file_id>" }
```

---

### GET /files/{id}/download — 파일 다운로드

```
GET /files/{id}/download

Path:
  id  string  — 업로드 응답에서 받은 파일 ID

응답: 200 OK + 파일 바이너리 스트림
     404      파일 없음
```

---

### GET /files — 파일 목록 조회

```
GET /files?userId=<userId>

Query:
  userId  string (optional)  — 유저 필터

응답: JSON array  [{ "id": "...", "filePath": "...", ... }]
```

---

### GET /files/folder — 폴더 내 파일 목록 조회

```
GET /files/folder?folderPath=<path>&userId=<userId>

Query:
  folderPath  string  — 조회할 폴더 경로 (예: /folderA)
  userId      string  — 유저 필터

응답: JSON array
```

---

### POST /files/{id}/move — 단일 파일 이동

```
POST /files/{id}/move
Content-Type: application/json

Body:
  { "filePath": "/new/path/" }   — 폴더만 지정 시 파일명 자동 보정

응답: 200 OK + JSON
```

---

### POST /files/move-folder — 폴더 이동 (prefix 일괄 변경)

```
POST /files/move-folder
Content-Type: application/json

Body:
  { "fromPath": "/old/prefix", "toPath": "/new/prefix" }

응답: 200 OK + JSON
```

---

### DELETE /files — 단일 파일 삭제

```
DELETE /files?userId=<userId>&filePath=<filePath>

Query:
  userId    string  — 소유 유저 ID
  filePath  string  — 삭제할 가상 경로

응답: 200 OK  성공 / 404 파일 없음
```

---

### DELETE /files/folder — 폴더 삭제 (하위 전체)

```
DELETE /files/folder?userId=<userId>&folderPath=<folderPath>

Query:
  userId      string  — 소유 유저 ID
  folderPath  string  — 삭제할 폴더 경로 (하위 전체 삭제)

응답: 200 OK  성공 / 404 폴더 없음
```
