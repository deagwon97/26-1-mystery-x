# 배포 가이드

## 환경 요구사항

### 하드웨어

| 구성 | 최소 | 권장 |
|------|------|------|
| CPU | 2 Core | 4 Core |
| 메모리 | 4 GB | 8 GB |
| 디스크 | 50 GB | 200 GB+ |

### 소프트웨어

- Docker 20.10+
- Docker Compose 2.0+
- JDK 25+ (로컬 빌드 시)

## 로컬 개발 환경

### 1. Docker Compose 로 실행

```bash
cd BuChoiPark/deploy
docker-compose up -d
```

**실행되는 컨테이너**

| 컨테이너 | 포트 | 설명 |
|----------|------|------|
| server-debug | 3030:8080 | Spring Boot 애플리케이션 |
| nginx | 3031:8080 | OpenResty 리버스 프록시 |
| loadtest | - | 부하 테스트 도구 |
| container-monitor | - | 성능 모니터링 |

### 2. 로컬 개발 모드

```bash
# Spring Boot 직접 실행
cd BuChoiPark
./gradlew bootRun

# 환경 변수 설정
export SQLITE_DB_PATH=./data/sqlite/livid.db
export APP_STORAGE_DIR=./data/uploads
```

## 프로덕션 배포

### Docker 이미지 빌드

```bash
cd BuChoiPark
docker build -f deploy/Dockerfile -t buchoipark:latest .
```

### Docker Compose 배포

`deploy/docker-compose.prod.yaml` 생성:

```yaml
version: '3.8'

services:
  server:
    image: buchoipark:latest
    container_name: buchoipark-server
    environment:
      - SPRING_PROFILES_ACTIVE=prod
      - SQLITE_DB_PATH=/app/data/sqlite/livid.db
      - APP_STORAGE_DIR=/app/data/uploads
      - APP_UPLOAD_MAX_CONCURRENCY=16
      - APP_DOWNLOAD_MAX_CONCURRENCY=80
    volumes:
      - ./data/sqlite:/app/data/sqlite
      - ./data/uploads:/app/data/uploads
    ports:
      - "8080:8080"
    restart: unless-stopped
    deploy:
      resources:
        limits:
          cpus: '4.0'
          memory: 8G

  nginx:
    image: openresty/openresty:1.27.1.2-0-alpine
    container_name: buchoipark-nginx
    volumes:
      - ./nginx/conf.d:/etc/nginx/conf.d:ro
      - ./nginx/lua:/etc/nginx/lua:ro
      - ./data/uploads:/app/data/uploads
    ports:
      - "80:8080"
    depends_on:
      - server
    restart: unless-stopped
```

배포:

```bash
docker-compose -f deploy/docker-compose.prod.yaml up -d
```

## 환경 변수

### 애플리케이션 설정

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `SPRING_PROFILES_ACTIVE` | 활성화 프로파일 | `dev` |
| `SQLITE_DB_PATH` | SQLite DB 경로 | - |
| `APP_STORAGE_DIR` | 파일 저장 디렉토리 | - |

### 업로드 설정

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `APP_UPLOAD_MAX_CONCURRENCY` | 업로드 동시성 | `16` |
| `APP_UPLOAD_MAX_INFLIGHT_BYTES` | 인플라이트 바이트 제한 | `2147483648` (2GB) |
| `APP_UPLOAD_MAX_QUEUE_BYTES` | 대기열 바이트 제한 | `1073741824` (1GB) |
| `APP_UPLOAD_MAX_QUEUE_REQUESTS` | 대기열 요청 수 제한 | `1000` |
| `APP_UPLOAD_QUEUE_TIMEOUT_MS` | 대기열 타임아웃 | `10000` |

### 다운로드 설정

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `APP_DOWNLOAD_MAX_CONCURRENCY` | 다운로드 동시성 | `80` |
| `APP_DOWNLOAD_MAX_QUEUE_BYTES` | 대기열 바이트 제한 | `1073741824` (1GB) |
| `APP_DOWNLOAD_MAX_QUEUE_REQUESTS` | 대기열 요청 수 제한 | `1000` |
| `APP_DOWNLOAD_QUEUE_TIMEOUT_MS` | 대기열 타임아웃 | `10000` |
| `APP_DOWNLOAD_RATE_LIMIT_RPS` | 초당 요청 수 제한 | `30` |

### Spring Boot 설정

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `SERVER_PORT` | 서버 포트 | `8080` |
| `SPRING_SERVLET_MULTIPART_MAX_FILE_SIZE` | 최대 파일 크기 | `512MB` |
| `SPRING_SERVLET_MULTIPART_MAX_REQUEST_SIZE` | 최대 요청 크기 | `512MB` |

## 데이터 관리

### SQLite 백업

```bash
# DB 백업
sqlite3 data/sqlite/livid.db ".backup 'backup-$(date +%Y%m%d).db'"

# DB 복원
sqlite3 data/sqlite/livid.db ".restore 'backup-20260405.db'"
```

### 파일 저장소 백업

```bash
# tar 로 백업
tar -czf uploads-backup-$(date +%Y%m%d).tar.gz data/uploads/

# rsync 로 동기화
rsync -av data/uploads/ backup-server:/backup/uploads/
```

## 모니터링

### 헬스 체크

```bash
# 애플리케이션 헬스 체크
curl http://localhost:8080/actuator/health

# Nginx 헬스 체크
curl http://localhost:8080/lua-health
```

### 로그 확인

```bash
# 애플리케이션 로그
docker logs -f buchoipark-server

# Nginx 로그
docker logs -f buchoipark-nginx
```

### 리소스 모니터링

```bash
# 컨테이너 리소스 사용량
docker stats buchoipark-server buchoipark-nginx
```

## 스케일링

### 수평 확장

1. **Load Balancer 설정**

```nginx
upstream backend {
    server server1:8080;
    server server2:8080;
    server server3:8080;
}

server {
    listen 80;
    location / {
        proxy_pass http://backend;
    }
}
```

2. **공유 스토리지 사용**

- NFS 마운트
- S3 호환 스토리지
- 분산 파일 시스템

### 수직 확장

```yaml
deploy:
  resources:
    limits:
      cpus: '8.0'
      memory: 16G
    reservations:
      cpus: '4.0'
      memory: 8G
```

## 업그레이드

### 무중단 배포

```bash
# 새로운 이미지 빌드
docker-compose -f deploy/docker-compose.prod.yaml pull

# 블루 -그린 배포
docker-compose -f deploy/docker-compose.prod.yaml up -d

# 건강 확인
curl http://localhost:8080/actuator/health

# 이전 컨테이너 정리
docker-compose -f deploy/docker-compose.prod.yaml down
```

## 장애 복구

### DB 복구

```bash
# 최신 백업으로 복원
docker-compose down
sqlite3 data/sqlite/livid.db ".restore 'backup.db'"
docker-compose up -d
```

### 파일 복구

```bash
# 백업에서 파일 복원
tar -xzf uploads-backup.tar.gz -C data/
```
