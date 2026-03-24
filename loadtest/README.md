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
