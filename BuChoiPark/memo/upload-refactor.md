# Upload Lua 리팩터링 메모

## 목표 및 전제
- 목표: 단일 물리 노드 환경에서 업로드 처리 성능(특히 p95/p99) 개선
- 전제: 확장(멀티 노드, 오브젝트 스토리지 분리)은 지금 단계에서 고려하지 않음
- 전략: Nginx 직저장 구조는 유지하되, 불필요한 I/O와 worker 점유를 줄이는 방향으로 개선

## 지금까지 한 것
- 업로드 경로 성능 병목을 식별했다.
- 핵심 병목 4개를 우선순위로 정리했다.
	- Lua 동기 파일 I/O worker 점유
	- body 전체 버퍼링
	- temp 파일 재복사
	- 메타데이터 내부 호출 직렬 대기
- 현재 방향을 "Nginx 직저장 유지 + 로컬 최적화"로 확정했다.

## 지금 하고 있는 것
- 아래 단기 계획 항목을 구현 가능한 작업 단위로 쪼개고, 적용 순서를 고정한다.
- 1차 목표는 디스크 재복사 제거(rename 우선)와 file size 계산 비용 절감이다.

## 단기 plan (즉시 적용)

### 1) temp 재복사 제거: rename 우선, 실패 시 copy fallback
- 대상: `nginx/lua/upload.lua` (기준: 기존 line 81 부근)
- 변경 범위:
	- `body_file` 경로를 최종 경로로 먼저 `os.rename(body_file, target_path)` 시도
	- rename 실패 시에만 기존 chunk copy 로직 수행
- 기대 효과:
	- 같은 파일시스템이면 rename은 메타데이터 연산 중심으로 매우 빠름
	- 대용량 업로드의 디스크 read/write 총량 감소
- 롤백:
	- rename 분기 제거 후 기존 copy-only 로직으로 복귀
- 검증:
	- 업로드 기능 정상 동작(200 응답, 다운로드 무결성)
	- loadtest에서 `POST /files/upload` p95/p99 비교

### 2) file size 계산: Content-Length 헤더 우선 사용
- 대상: `nginx/lua/upload.lua` (기준: 기존 line 33 부근)
- 변경 범위:
	- `headers["Content-Length"]`를 정수로 파싱해 `file_size`로 우선 사용
	- 헤더가 없거나 비정상이면 기존 방식(`body_data` 길이 또는 파일 seek) fallback
- 기대 효과:
	- body_file 케이스에서 file size 계산용 추가 파일 open/seek 비용 감소
- 롤백:
	- 헤더 우선 분기 제거 후 기존 계산 방식 복귀
- 검증:
	- 메타데이터의 `fileSize` 정확성 확인

### 3) upload location 전용 최소 튜닝
- 대상: `nginx/conf.d/default.conf` (`location = /files/upload`)
- 변경 범위(초기안):
	- 업로드 경로에만 body/temp/log 관련 설정 적용
	- 전역 영향 없이 upload 엔드포인트에 한정
- 기대 효과:
	- 다른 API와의 간섭 최소화
	- 업로드 트래픽 변동 시 운영 안정성 증가
- 롤백:
	- 해당 location 내부 튜닝 항목만 제거
- 검증:
	- 기능 회귀 없음
	- 기존 read API 지연 악화 없음

### 4) body temp 경로와 최종 저장 경로를 같은 파일시스템으로 정렬
- 대상: `nginx/conf.d/default.conf`
- 변경 범위:
	- `client_body_temp_path`를 `/app/data/uploads`와 동일 파일시스템 경로로 지정
	- 디렉터리 권한/정리 정책 포함 점검
- 기대 효과:
	- rename 성공률 상승
	- copy fallback 발생 빈도 감소
- 롤백:
	- 기존 temp 경로로 되돌림
- 검증:
	- rename 성공률 로그(또는 카운터) 확인
	- 블록 I/O 감소 여부 관찰

## 중기 plan (안정화)
1. rename/copy 분기별 카운터 및 실패 원인 로깅 추가
2. upload 요청 처리시간을 단계별로 분해 측정
	 - body read
	 - metadata call
	 - file persist(rename or copy)
3. 장애 시 고아 메타데이터 정리 배치(보상 처리)
4. 운영 가이드 정리
	 - temp 디렉터리 용량/정리 정책
	 - 디스크 여유 임계치 알람

## 장기 plan (구조 개선 후보, 지금은 보류)
1. 업로드 플로우를 init/upload/complete 형태로 분리
2. multipart 유사 업로드 지원(재시도/부분 실패 복구 강화)
3. 메타데이터 완료 확정(finalize) 계약 도입

## 성능 리스크 및 체크포인트
- rename 실패가 잦으면 여전히 copy 비용이 크게 발생한다.
- temp 경로와 최종 경로가 다른 마운트면 rename은 `EXDEV`로 실패한다.
- 메타데이터 생성 후 저장 실패 시 정합성 이슈가 남는다.

## 테스트/측정 기준
- 기준 도구: `loadtest/README.md`에 정리된 스크립트 사용
- 최소 관찰 지표:
	- `POST /files/upload` p95/p99
	- write error rate
	- timeout/connection reset/broken pipe/503
	- docker block I/O 및 서버 로그 에러
- 합격 기준(초안):
	- 기능 회귀 0
	- write error rate 개선 또는 유지
	- p95/p99 유의미 개선

## 실행 순서 (체크리스트)
- [ ] `upload.lua`: rename 우선 + copy fallback
- [ ] `upload.lua`: Content-Length 우선 fileSize 계산
- [ ] `default.conf`: upload location 전용 최소 튜닝
- [ ] `default.conf`: body temp 경로 파일시스템 정렬
- [ ] loadtest 전/후 비교 리포트 기록
