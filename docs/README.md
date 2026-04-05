# BuChoiPark (부최박) - 파일 서버 프로젝트

## 개요

BuChoiPark 는 고성능 파일 저장 및 관리 서비스입니다. 다중 사용자 환경에서 파일 업로드, 다운로드, 폴더 관리 기능을 제공하며, 대용량 파일 처리와 동시성 제어에 최적화되어 있습니다.

## 주요 기능

- **파일 관리**: 업로드, 다운로드, 삭제
- **폴더 관리**: 생성, 조회, 삭제, 이동
- **사용자 구분**: 사용자별 파일 및 폴더 격리
- **대용량 파일 처리**: 500MB 이상 파일 지원
- **고동시성 처리**: 동시 업로드/다운로드 최적화
- **파괴적 작업 방지**: 논리적 삭제와 물리적 삭제 분리

## 기술 스택

| 구분 | 기술 |
|------|------|
| Backend | Kotlin, Spring Boot 4.0.2 |
| Database | SQLite |
| Web Server | OpenResty (Nginx + Lua) |
| Build Tool | Gradle (Kotlin DSL) |
| JDK | Java 25 |
| Testing | Kotest, JUnit 5 |
| Load Testing | Go (benchmark tool) |

## 프로젝트 구조

```
.
├── BuChoiPark/              # 메인 애플리케이션
│   ├── src/                 # 소스 코드
│   ├── nginx/               # Nginx 설정
│   ├── deploy/              # 배포 설정 (Docker)
│   ├── metric/              # 성능 모니터링
│   └── loadtest/            # 부하 테스트
├── loadtest/                # 벤치마크 도구
│   └── src/                 # Go 벤치마크 소스
└── docs/                    # 프로젝트 문서
```

## 아키텍처

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│   Client    │────▶│  OpenResty   │────▶│ Spring Boot │
│             │◀────│  (Nginx+Lua) │◀────│  (Kotlin)   │
└─────────────┘     └──────────────┘     └─────────────┘
                           │                     │
                           │                     ▼
                           │              ┌──────────────┐
                           │              │   SQLite     │
                           │              └──────────────┘
                           │
                           ▼
                    ┌──────────────┐
                    │ File Storage │
                    └──────────────┘
```

### 컴포넌트 설명

1. **OpenResty (Nginx + Lua)**
   - 파일 업로드/다운로드의 전처리 및 후처리
   - 멀티파트 폼 데이터 처리 최적화
   - Rate limiting 및 동시성 제어
   - 헤더 기반 인증/권한위임

2. **Spring Boot Application**
   - 비즈니스 로직 처리
   - 파일 메타데이터 관리
   - DB 연동 (SQLite)
   - 비동기 처리 지원

3. **SQLite**
   - 파일 메타데이터 저장
   - 사용자 정보 관리
   - 접근 제어 정보

## API 엔드포인트

### 파일 관련

| Method | Endpoint | 설명 |
|--------|----------|------|
| POST | `/files/upload` | 파일 업로드 |
| GET | `/files/{id}/download` | 파일 다운로드 |
| DELETE | `/files` | 파일 삭제 |

### 폴더 관련

| Method | Endpoint | 설명 |
|--------|----------|------|
| GET | `/files/folder` | 폴더 내 파일 목록 조회 |
| POST | `/files/move-folder` | 폴더 이동 |
| DELETE | `/files/folder` | 폴더 삭제 |

### 내부 API (Nginx 를 통한 호출)

| Method | Endpoint | 설명 |
|--------|----------|------|
| POST | `/internal/files/upload-metadata` | 업로드 메타데이터 생성 |
| GET | `/internal/files/{id}/download-metadata` | 다운로드 메타데이터 조회 |

## 성능 최적화

### 동시성 제어

- **업로드**: Semaphore 기반 동시성 제한 (기본: 16)
- **다운로드**: Semaphore 기반 동시성 제한 (기본: 80)
- **대기열 관리**: 요청 수 및 데이터 크기 기반 제한

### Rate Limiting

- 다운로드 요청 속도 제한 (기본: 30 req/s)
- 윈도우 기반 리미터 사용

### 메모리 관리

- In-flight 데이터 크기 제한
- 대기열 크기 제한 (바이트 단위)
- 타임아웃 기반 큐 정리

## 개발 환경 설정

### 필수 요구사항

- JDK 25 이상
- Docker & Docker Compose
- SQLite3 (로컬 개발 시)

### 로컬 개발 시작

```bash
# Spring Boot 애플리케이션 실행
cd BuChoiPark
./gradlew bootRun

# 또는 Docker Compose 사용
cd BuChoiPark/deploy
docker-compose up -d
```

### 환경 변수

| 변수 | 설명 | 기본값 |
|------|------|--------|
| `SPRING_PROFILES_ACTIVE` | 활성화된 프로파일 | - |
| `SQLITE_DB_PATH` | SQLite DB 경로 | - |
| `APP_STORAGE_DIR` | 파일 저장 디렉토리 | - |
| `APP_UPLOAD_MAX_CONCURRENCY` | 업로드 동시성 | 16 |
| `APP_DOWNLOAD_MAX_CONCURRENCY` | 다운로드 동시성 | 80 |

## 벤치마크

### 테스트 시나리오

1. **Upload**: 파일 동시 업로드
2. **Download**: 동시 다운로드 + 무결성 검증
3. **Folder List**: 폴더 목록 조회
4. **Move Folder**: 폴더 이동
5. **Delete Files**: 개별 파일 동시 삭제
6. **Delete Folder**: 폴더 단위 삭제

### 테스트 파일 구성

| 케이스 | 크기 | 파일 수 | 동시성 |
|--------|------|---------|--------|
| small | 3 MB | 1000 | 1000 |
| medium | 30 MB | 100 | 100 |
| large | 100 MB | 30 | 30 |
| xlarge | 300 MB | 10 | 10 |
| huge | 500 MB | 6 | 6 |

## 모니터링

- Spring Boot Actuator 를 통한 헬스 체크
- `/actuator/health` 엔드포인트
- 컨테이너 리소스 모니터링 도구 포함

## 라이선스

MIT License
