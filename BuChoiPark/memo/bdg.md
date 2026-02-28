## 의견 메모

- userId를 filePath에 포함시키는게 성능상 이점으로 보입니다.
- 어차피 file을 검색할 때는 항상 userId가 필요하기 때문에, filePath에 userId를 포함시키는 것이 검색 시에 더 빠르게 접근할 수 있다고 봅니다.
- /userId/filePath 형태로 저장하면, 특정 userId에 대한 파일들을 쉽게 그룹화할 수 있고, 검색 시에도 해당 userId에 대한 파일들을 빠르게 찾을 수 있을 것입니다.