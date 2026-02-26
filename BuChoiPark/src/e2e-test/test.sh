#!/bin/bash

echo 해당 스크립트는 파일 업로드, 다운로드, 이동 기능을 테스트하기 위한 E2E 테스트입니다.
echo 항상 db 및 업로드된 파일을 초기화됩니다.
echo "정말 진행하시겠습니까? (y/n)"
read -r answer
if [[ "$answer" != "y" ]]; then
  echo "테스트가 취소되었습니다."
  exit 0
fi

# 0) DB 초기화 (개발용)
sqlite3 /app/data/sqlite/livid.db "DELETE FROM files;"
rm -rf /app/data/uploads/*
rm -rf ./test.txt

# 1) 업로드
UPLOAD_RES=$(curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/docs/test.txt" \
  -F "file=@/app/src/e2e-test/upload-original/test.txt")

echo "$UPLOAD_RES"

# 2) 업로드 응답에서 id 추출 (jq 필요)
FILE_ID=$(echo "$UPLOAD_RES" | jq -r '.id')

# 3) 전체 목록 조회
echo "전체 파일 목록:"
curl -s "http://localhost:8080/files" | jq

# 4) 사용자 필터 목록 조회
echo "사용자 필터 목록:"
curl -s "http://localhost:8080/files?userId=user-123" | jq -r '.[0].id'

# 5) 다운로드
curl -OJ "http://localhost:8080/files/${FILE_ID}/download" 

# 6) 파일 이동 (메타데이터만 변경, 폴더 경로만 지정해도 파일명 자동 보정)
echo "파일 이동:"
curl -s -X POST "http://localhost:8080/files/${FILE_ID}/move" \
  -H "Content-Type: application/json" \
  -d '{"filePath":"/virtual/moved/"}'

echo "업데이트 후 전체 파일 목록:"
curl -s "http://localhost:8080/files" | jq


# 7) 폴더 이동 (prefix 기반 일괄 변경)
echo "폴더 이동:"
curl -s -X POST "http://localhost:8080/files/move-folder" \
  -H "Content-Type: application/json" \
  -d '{"fromPath":"/virtual/moved","toPath":"/docs2"}'

echo "업데이트 후 전체 파일 목록:"
curl -s "http://localhost:8080/files" | jq

# 다운로드된 파일과 원본 데이터가 일치하는지 확인
echo "다운로드된 파일과 원본 데이터가 일치하는지 확인:"
if cmp -s "./test.txt" "/app/src/e2e-test/upload-original/test.txt"; then
  echo "파일이 일치합니다."
else
  echo "파일이 일치하지 않습니다."
fi

echo "테스트가 완료되었습니다. 다운로드 된 파일을 삭제합니다."
rm -rf ./test.txt