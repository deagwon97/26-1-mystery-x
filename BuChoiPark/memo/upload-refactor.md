# Upload Lua 리팩터링 메모

## 목적
- 현재 [nginx/lua/upload.lua](nginx/lua/upload.lua) 업로드 경로의 성능 병목 지점을 정리한다.
- 이후 리팩터링 작업의 기준 문서로 사용한다.

## 현재 구조 요약
- 업로드 요청은 Nginx Lua에서 본문을 읽는다.
- 메타데이터를 내부 API `/_meta/upload`로 생성한다.
- 생성된 id를 파일명으로 사용해 `/app/data/uploads/{id}`에 저장한다.

## 성능상 주요 이슈 (우선순위)

### 1) 동기 파일 I/O로 인한 Worker 블로킹 (가장 심각)
- 위치: [nginx/lua/upload.lua](nginx/lua/upload.lua#L39), [nginx/lua/upload.lua](nginx/lua/upload.lua#L73), [nginx/lua/upload.lua](nginx/lua/upload.lua#L87)
- 원인: `io.open`, `read`, `write`는 블로킹 호출이라 파일 처리 중 worker가 점유된다.
- 영향: 동시 업로드 증가 시 처리량 저하, 지연시간 급증, p95/p99 악화.

### 2) 요청 본문 전체 버퍼링
- 위치: [nginx/lua/upload.lua](nginx/lua/upload.lua#L24), [nginx/lua/upload.lua](nginx/lua/upload.lua#L25), [nginx/lua/upload.lua](nginx/lua/upload.lua#L26)
- 원인: `ngx.req.read_body()` 기반이라 스트리밍이 아니라 전체 수신 후 처리한다.
- 영향: 대용량에서 메모리/디스크 임시 저장 비용 증가, 응답까지 지연 증가.

### 3) Temp 파일에서 최종 저장소로 재복사
- 위치: [nginx/lua/upload.lua](nginx/lua/upload.lua#L81), [nginx/lua/upload.lua](nginx/lua/upload.lua#L92)
- 원인: Nginx body temp 파일을 다시 읽어 최종 파일로 기록한다.
- 영향: 디스크 read/write가 2배 가까이 증가해 I/O 병목 가능성이 커진다.

### 4) 메타데이터 내부 호출의 직렬 대기
- 위치: [nginx/lua/upload.lua](nginx/lua/upload.lua#L54), [nginx/lua/upload.lua](nginx/lua/upload.lua#L59)
- 원인: 업로드마다 `/_meta/upload` 응답을 기다린 뒤 다음 단계로 진행한다.
- 영향: 메타 API 지연이 업로드 전체 지연으로 직결된다.

## 설정 관점 점검 포인트
- 현재 [nginx/conf.d/default.conf](nginx/conf.d/default.conf#L4) 에는 `client_max_body_size`만 명시되어 있다.
- body 버퍼/임시 파일 관련 설정(`client_body_buffer_size`, temp path, 디스크 특성) 점검이 필요하다.

## 리스크
- 메타데이터 생성 후 파일 저장 실패 시 정합성 이슈(고아 메타데이터)가 발생할 수 있다.
- 재시도/보상 트랜잭션 정책 부재 시 장애 상황에서 운영 부담이 커진다.

## 개선 우선순위 제안
1. 업로드 경로에서 Lua 동기 파일 I/O 제거 또는 최소화
2. 전체 body 버퍼링 대신 스트리밍 처리로 전환
3. 메타데이터 생성-저장 플로우를 선발급/완료확정 구조로 재설계
4. Nginx body/temp 관련 설정과 저장 디스크 I/O 성능 튜닝

## 다음 작업 기준
- 이 문서를 기준으로 리팩터링 작업을 진행한다.
- 각 개선안은 다음 항목을 함께 정의한다.
	- 변경 범위
	- 기대 효과(지연/처리량)
	- 실패 시 롤백 방법
	- 테스트 방법(대용량/동시 업로드)
