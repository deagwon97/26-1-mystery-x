## benchmark

### resource spec
- cpu: 4 Core
- mem: 8 Gib
- disk: 50GB, 200GB

### 테스트 파일 케이스: 3G * 5
(예상되는 테스트 시간: 10분 ~ 15분)
- 3MB x 1000개
- 30MB x 100개
- 100MB x 30개
- 300MB x 10개
- 500MB x 6개

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
