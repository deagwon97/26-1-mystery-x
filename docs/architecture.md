# 아키텍처 문서

## 시스템 아키텍처

### 전체 흐름

```
┌─────────────────────────────────────────────────────────────────┐
│                         Client                                   │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ HTTP/HTTPS
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    OpenResty (Nginx + Lua)                      │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐  │
│  │  on_request  │  │   upload     │  │    download          │  │
│  │    .lua      │  │    .lua      │  │    .lua              │  │
│  └──────────────┘  └──────────────┘  └──────────────────────┘  │
│  ┌──────────────┐  ┌──────────────┐                            │
│  │ before_resp  │  │   health     │                            │
│  │   .lua       │  │   .lua       │                            │
│  └──────────────┘  └──────────────┘                            │
└─────────────────────────────────────────────────────────────────┘
                    │                   │
                    │ Internal API      │ Static File
                    ▼                   ▼
┌─────────────────────────────┐  ┌─────────────────────────────┐
│      Spring Boot App        │  │      File Storage           │
│  ┌──────────────────────┐   │  │  ┌──────────────────────┐   │
│  │   FileController     │   │  │  │  /app/data/uploads/  │   │
│  └──────────────────────┘   │  │  └──────────────────────┘   │
│  ┌──────────────────────┐   │  └─────────────────────────────┘
│  │   FileService        │   │
│  └──────────────────────┘   │
│  ┌──────────────────────┐   │
│  │  FileRepository      │   │
│  └──────────────────────┘   │
└─────────────────────────────┘
                    │
                    │ JDBC
                    ▼
┌─────────────────────────────┐
│        SQLite DB            │
│  ┌──────────────────────┐   │
│  │   files table        │   │
│  │  - id (UUID)         │   │
│  │  - user_id           │   │
│  │  - file_name         │   │
│  │  - file_path         │   │
│  │  - file_size         │   │
│  │  - uploaded_at       │   │
│  └──────────────────────┘   │
└─────────────────────────────┘
```

## 컴포넌트 상세

### 1. OpenResty (Reverse Proxy & Load Balancer)

#### 역할
- 파일 업로드/다운로드 트래픽 처리
- 멀티파트 폼 데이터 파싱 최적화
- 요청 전처리 및 응답 후처리
- Rate limiting 및 동시성 제어

#### Lua 스크립트

| 파일 | 역할 |
|------|------|
| `on_request.lua` | 요청 시작 시 처리 (로깅, 인증 체크) |
| `upload.lua` | 파일 업로드 처리 (메타데이터 생성, 파일 저장) |
| `download.lua` | 파일 다운로드 처리 (메타데이터 조회, 스트리밍) |
| `before_response.lua` | 응답 헤더 수정 및 추가 |
| `health.lua` | 헬스 체크 엔드포인트 |
| `meta_http.lua` | 메타데이터 API 호출 헬퍼 |

#### 업로드 흐름

```
1. Client → POST /files/upload (multipart/form-data)
2. Nginx: 헤더에서 X-User-Id, X-File-Path, X-File-Name 추출
3. Nginx: 요청 바디 크기 계산
4. Nginx → POST /_meta/upload (메타데이터 생성 요청)
5. Spring Boot: UUID 생성, DB 에 메타데이터 저장
6. Spring Boot → Nginx: fileId 반환
7. Nginx: 임시 파일을 영구 저장소로 이동
8. Nginx → Client: 업로드 결과 반환
```

#### 다운로드 흐름

```
1. Client → GET /files/{id}/download
2. Nginx → GET /_meta/download/{id} (메타데이터 조회)
3. Spring Boot → Nginx: 파일명, 크기 반환
4. Nginx: 파일 스트리밍 시작
5. Nginx → Client: 파일 전송 (Range 요청 지원)
```

### 2. Spring Boot Application

#### 역할
- 비즈니스 로직 처리
- 파일 메타데이터 관리
- 데이터베이스 연동
- 비동기 처리 지원

#### 주요 클래스

| 클래스 | 역할 |
|--------|------|
| `FileController` | REST API 엔드포인트 처리 |
| `FileService` | 비즈니스 로직 구현 |
| `FileRepository` | 데이터베이스 접근 |
| `LividApplication` | Spring Boot 애플리케이션 진입점 |

#### 데이터 모델

```sql
CREATE TABLE files (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    uploaded_at TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    file_size INTEGER NOT NULL
);

CREATE INDEX idx_files_user_path ON files(user_id, file_path);
```

### 3. 동시성 제어

#### Semaphore 기반 제어

```kotlin
// 업로드 동시성 제어
private val uploadSemaphore = Semaphore(maxConcurrency, true)

// 다운로드 동시성 제어
private val downloadSemaphore = Semaphore(maxConcurrency, true)
```

#### Rate Limiter

```kotlin
// Fixed Window Rate Limiter
class FixedWindowRateLimiter(
    maxRequestsPerWindow: Int,
    windowMillis: Long
)
```

#### In-Flight Limiter

```kotlin
// 바이트 단위 동시 처리량 제어
class InFlightByteLimiter(maxInFlightBytes: Long)
```

### 4. 대기열 관리

#### 업로드 대기열

```kotlin
private val uploadQueue = ArrayDeque<QueuedUploadRequest>()
private val uploadQueueBytesLimiter = InFlightByteLimiter(maxQueueBytes)
```

#### 다운로드 대기열

```kotlin
private val downloadQueue = ConcurrentLinkedQueue<QueuedDownloadRequest>()
private val downloadQueueBytesLimiter = InFlightByteLimiter(maxQueueBytes)
```

## 성능 최적화 전략

### 1. 비동기 처리

- Spring WebMvc Async 설정
- DeferredResult 를 사용한 비동기 응답
- 스레드 풀 분리 (업로드/다운로드)

### 2. 스트리밍

- `StreamingResponseBody` 를 사용한 메모리 효율적 파일 전송
- 큰 파일도 메모리 로딩 없이 스트리밍

### 3. 병렬 처리

- 폴더 삭제 시 병렬 파일 삭제
- 대용량 파일 처리를 위한 Chunk 기반 처리

### 4. 캐싱

- Nginx 레이어에서 정적 파일 캐싱 가능
- 메타데이터 조회 결과 캐싱 (추천)

## 확장성

### 수평 확장

- Stateless API 디자인
- 공유 스토리지 사용 (NFS, S3 등)
- DB 샤딩 가능 (user_id 기반)

### 수직 확장

- 동시성 제한자 튜닝
- JVM 힙/메타스페이스 최적화
- DB 연결 풀 조정

## 모니터링

### 메트릭

- 요청/응답 시간
- 동시 연결 수
- 처리량 (RPS, MB/s)
- 에러율

### 헬스 체크

```
GET /actuator/health
GET /health
```

### 로그

- 접근 로그 (Nginx)
- 애플리케이션 로그 (Spring Boot)
- DB 쿼리 로그
