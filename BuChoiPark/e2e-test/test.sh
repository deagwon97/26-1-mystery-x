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
echo "0) DB 초기화 및 업로드된 파일 삭제:"
sqlite3 /app/data/sqlite/livid.db "DELETE FROM files;"
rm -rf /app/data/uploads/*
rm -rf ./test.txt

# 1) 업로드
echo "1) 파일 업로드:"
UPLOAD_RES=$(curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/docs/test.txt" \
  -F "file=@/app/e2e-test/upload-original/test.txt")
echo "$UPLOAD_RES" | jq

# 2) 업로드 응답에서 id 추출 (jq 필요)
FILE_ID=$(echo "$UPLOAD_RES" | jq -r '.id')

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/docs/test2.txt" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/docs/test3.txt" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/folderA/file1" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/folderA/file2" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/folderA/folderB/file3" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/folderA/folderB/file4" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/folderA/folderB/folderC/file6" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

curl -s -X POST "http://localhost:8080/files/upload" \
  -F "userId=user-123" \
  -F "filePath=/folderA/folderB/folderC/file7" \
  -F "file=@/app/e2e-test/upload-original/test.txt"

# 3) 전체 목록 조회
echo "3) 전체 파일 목록:"
curl -s "http://localhost:8080/files" | jq

# 4) 사용자 필터 목록 조회
echo "4) 사용자 필터 목록:"
curl -s "http://localhost:8080/files?userId=user-123" | jq -r '.[0].id'

# 5) 다운로드
echo "5) 파일 다운로드:"
curl -OJ "http://localhost:8080/files/${FILE_ID}/download" 

# 6) 파일 이동 (메타데이터만 변경, 폴더 경로만 지정해도 파일명 자동 보정)
echo "6) 파일 이동:"
curl -s -X POST "http://localhost:8080/files/${FILE_ID}/move" \
  -H "Content-Type: application/json" \
  -d '{"filePath":"/virtual/moved/"}' | jq

echo "업데이트 후 전체 파일 목록:"
curl -s "http://localhost:8080/files" | jq


# 7) 폴더 이동 (prefix 기반 일괄 변경)
echo "7) 폴더 이동:"
curl -s -X POST "http://localhost:8080/files/move-folder" \
  -H "Content-Type: application/json" \
  -d '{"fromPath":"/virtual/moved","toPath":"/docs2"}' | jq 

echo "업데이트 후 전체 파일 목록:"
curl -s "http://localhost:8080/files" | jq


# 8) 다운로드된 파일과 원본 데이터가 일치하는지 확인
echo "8) 다운로드된 파일과 원본 데이터가 일치하는지 확인:"
if cmp -s "./test.txt" "/app/e2e-test/upload-original/test.txt"; then
  echo "파일이 일치합니다."
else
  echo "파일이 일치하지 않습니다."
fi

# 9) 폴더 내 파일 목록 조회
echo "9) 폴더 내 파일 목록 조회:"
curl -s "http://localhost:8080/files/folder?folderPath=/folderA&userId=user-123" | jq


# 10) 파일 삭제 (userId + filePath)
echo "10) 파일 삭제(userId + filePath):"
DELETE_STATUS=$(curl -s -o /tmp/delete_result.json -w "%{http_code}" -X DELETE \
  "http://localhost:8080/files?userId=user-123&filePath=/docs2/test.txt")

cat /tmp/delete_result.json | jq
echo "삭제 응답 코드: ${DELETE_STATUS}"

if [[ "$DELETE_STATUS" == "200" ]]; then
  echo "삭제 API 호출 성공"
else
  echo "삭제 API 호출 실패"
fi

echo "삭제 후 DB 검증(해당 경로 0건 기대):"
sqlite3 /app/data/sqlite/livid.db "SELECT COUNT(*) FROM files WHERE user_id='user-123' AND file_path='/docs2/test.txt';"

echo "삭제 후 물리 파일 검증(파일 없어야 함):"
if [[ -f "/app/data/uploads/${FILE_ID}" ]]; then
  echo "물리 파일이 남아있습니다: /app/data/uploads/${FILE_ID}"
else
  echo "물리 파일이 정상 삭제되었습니다."
fi


# 11) 폴더 삭제 (userId + folderPath, 하위 전체 삭제)
echo "11) 폴더 삭제(userId + folderPath):"
DELETE_FOLDER_STATUS=$(curl -s -o /tmp/delete_folder_result.json -w "%{http_code}" -X DELETE \
  "http://localhost:8080/files/folder?userId=user-123&folderPath=/folderA")

cat /tmp/delete_folder_result.json | jq
echo "폴더 삭제 응답 코드: ${DELETE_FOLDER_STATUS}"

if [[ "$DELETE_FOLDER_STATUS" == "200" ]]; then
  echo "폴더 삭제 API 호출 성공"
else
  echo "폴더 삭제 API 호출 실패"
fi

echo "폴더 삭제 후 DB 검증(/folderA 하위 0건 기대):"
sqlite3 /app/data/sqlite/livid.db "SELECT COUNT(*) FROM files WHERE user_id='user-123' AND file_path LIKE '/folderA/%';"

echo "폴더 삭제 후 DB 검증(/docs 하위 2건 유지 기대):"
sqlite3 /app/data/sqlite/livid.db "SELECT COUNT(*) FROM files WHERE user_id='user-123' AND file_path LIKE '/docs/%';"

echo "폴더 삭제 후 전체 물리 파일 수 검증(2개 기대):"
find /app/data/uploads -type f | wc -l


# 12) 테스트 데이터 정리
echo "12) 테스트가 완료되었습니다. 다운로드 된 파일을 삭제합니다."
sqlite3 /app/data/sqlite/livid.db "DELETE FROM files;"
rm -rf /app/data/uploads/*
rm -rf ./test.txt
rm -rf /tmp/delete_result.json
rm -rf /tmp/delete_folder_result.json
