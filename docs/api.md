# API 문서

## 기본 정보

- **Base URL**: `http://localhost:8080` (개발 환경)
- **Content-Type**: `application/json` (JSON API)
- **Authentication**: 헤더 기반 (`X-User-Id`)

## 공통 헤더

### 요청 헤더

| 헤더 | 필수 | 설명 | 예시 |
|------|------|------|------|
| `X-User-Id` | 예 | 사용자 식별자 | `user123` |
| `Content-Type` | 상황에 따라 | 요청 바디 타입 | `multipart/form-data` |

### 응답 헤더

| 헤더 | 설명 |
|------|------|
| `Content-Type` | 응답 바디 타입 |
| `Content-Length` | 응답 바디 크기 (바이트) |

## 파일 API

### 1. 파일 업로드

#### 멀티파트 폼 방식

```
POST /files/upload
Content-Type: multipart/form-data
X-User-Id: {user_id}
X-File-Path: {file_path}
X-File-Name: {file_name}
```

**파라미터**

| 파라미터 | 타입 | 필수 | 설명 |
|----------|------|------|------|
| file | MultipartFile | 예 | 업로드할 파일 |

**예시 요청**

```bash
curl -X POST http://localhost:8080/files/upload \
  -H "X-User-Id: user123" \
  -H "X-File-Path: /documents" \
  -H "X-File-Name: report.pdf" \
  -F "file=@/path/to/report.pdf"
```

**응답**

```json
{
  "id": "uuid-string",
  "userId": "user123",
  "uploadedAt": "2026-04-05T10:30:00Z",
  "fileName": "report.pdf",
  "filePath": "/documents/report.pdf",
  "fileSize": 1048576
}
```

#### Raw 바디 방식 (대용량 파일)

```
POST /files/upload
Content-Type: application/octet-stream
X-User-Id: {user_id}
X-File-Path: {file_path}
X-File-Name: {file_name}
Content-Length: {file_size}
```

**예시 요청**

```bash
curl -X POST http://localhost:8080/files/upload \
  -H "X-User-Id: user123" \
  -H "X-File-Path: /videos" \
  -H "X-File-Name: movie.mp4" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @movie.mp4
```

### 2. 파일 다운로드

```
GET /files/{id}/download
X-User-Id: {user_id}
```

**파라미터**

| 파라미터 | 타입 | 필수 | 설명 |
|----------|------|------|------|
| id | String | 예 | 파일 ID (URL 경로) |

**Range 요청 지원**

```
GET /files/{id}/download?range=bytes=0-1023
X-User-Id: {user_id}
```

**예시 요청**

```bash
curl -X GET http://localhost:8080/files/uuid-string/download \
  -H "X-User-Id: user123" \
  -o downloaded_file.pdf
```

**응답 헤더**

```
Content-Type: application/pdf
Content-Length: 1048576
Content-Disposition: attachment; filename="report.pdf"
```

### 3. 파일 삭제

```
DELETE /files
X-User-Id: {user_id}
```

**요청 바디**

```json
{
  "filePath": "/documents/report.pdf"
}
```

**예시 요청**

```bash
curl -X DELETE http://localhost:8080/files \
  -H "X-User-Id: user123" \
  -H "Content-Type: application/json" \
  -d '{"filePath": "/documents/report.pdf"}'
```

**응답**

```json
{
  "success": true,
  "message": "File deleted successfully"
}
```

## 폴더 API

### 1. 폴더 목록 조회

```
GET /files/folder
X-User-Id: {user_id}
```

**쿼리 파라미터**

| 파라미터 | 타입 | 필수 | 설명 |
|----------|------|------|------|
| path | String | 아니오 | 조회할 폴더 경로 (기본: `/`) |

**예시 요청**

```bash
curl -X GET "http://localhost:8080/files/folder?path=/documents" \
  -H "X-User-Id: user123"
```

**응답**

```json
{
  "folders": [
    { "name": "work", "path": "/documents/work" },
    { "name": "personal", "path": "/documents/personal" }
  ],
  "files": [
    {
      "id": "uuid-1",
      "name": "report.pdf",
      "path": "/documents/report.pdf",
      "size": 1048576,
      "uploadedAt": "2026-04-05T10:30:00Z"
    }
  ]
}
```

### 2. 폴더 이동

```
POST /files/move-folder
X-User-Id: {user_id}
```

**요청 바디**

```json
{
  "fromPath": "/documents/old-folder",
  "toPath": "/documents/new-folder"
}
```

**예시 요청**

```bash
curl -X POST http://localhost:8080/files/move-folder \
  -H "X-User-Id: user123" \
  -H "Content-Type: application/json" \
  -d '{
    "fromPath": "/documents/old-folder",
    "toPath": "/documents/new-folder"
  }'
```

**응답**

```json
{
  "success": true,
  "message": "Folder moved successfully"
}
```

### 3. 폴더 삭제

```
DELETE /files/folder
X-User-Id: {user_id}
```

**요청 바디**

```json
{
  "folderPath": "/documents/to-delete"
}
```

**예시 요청**

```bash
curl -X DELETE http://localhost:8080/files/folder \
  -H "X-User-Id: user123" \
  -H "Content-Type: application/json" \
  -d '{"folderPath": "/documents/to-delete"}'
```

**응답**

```json
{
  "success": true,
  "deletedCount": 15,
  "message": "Folder and its contents deleted successfully"
}
```

## 내부 API

### 1. 업로드 메타데이터 생성

```
POST /internal/files/upload-metadata
Content-Type: application/json
```

**요청 바디**

```json
{
  "userId": "user123",
  "filePath": "/documents",
  "fileName": "report.pdf",
  "fileSize": 1048576
}
```

**응답**

```json
{
  "id": "uuid-string",
  "userId": "user123",
  "uploadedAt": "2026-04-05T10:30:00Z",
  "fileName": "report.pdf",
  "filePath": "/documents/report.pdf",
  "fileSize": 1048576
}
```

### 2. 다운로드 메타데이터 조회

```
GET /internal/files/{id}/download-metadata
```

**응답**

```json
{
  "id": "uuid-string",
  "fileName": "report.pdf",
  "fileSize": 1048576,
  "storagePath": "/app/data/uploads/uuid-string"
}
```

## 오류 처리

### 오류 응답 형식

```json
{
  "error": "ERROR_CODE",
  "message": "상세 오류 메시지"
}
```

### 오류 코드

| HTTP Status | 오류 코드 | 설명 |
|-------------|-----------|------|
| 400 | BAD_REQUEST | 잘못된 요청 |
| 401 | UNAUTHORIZED | 인증 실패 |
| 403 | FORBIDDEN | 권한 없음 |
| 404 | NOT_FOUND | 리소스 없음 |
| 409 | CONFLICT | 중복된 리소스 |
| 413 | PAYLOAD_TOO_LARGE | 파일 크기 초과 |
| 429 | TOO_MANY_REQUESTS | 요청 과다 |
| 500 | INTERNAL_ERROR | 서버 오류 |
| 503 | SERVICE_UNAVAILABLE | 서비스 일시 정지 |

### 오류 예시

```json
{
  "error": "PAYLOAD_TOO_LARGE",
  "message": "File size exceeds maximum allowed size (512MB)"
}
```

## Rate Limiting

### 다운로드 제한

- 기본: 30 요청/초
- 동시성: 80 연결

### 업로드 제한

- 기본 동시성: 16 연결
- 인플라이트 제한: 2GB

### 제한 초과 응답

```json
{
  "error": "TOO_MANY_REQUESTS",
  "message": "Rate limit exceeded. Please try again later."
}
```

HTTP 429 Too Many Requests
