


# 실험1)
- 일시: 2026-03-15
- server
    - debug-server 바로 호출
    - commit 7fef38ed4b7a17b9684d8e472b1b4e71741ec549 (spring에서만 upload 다운로드 구현)
- client
    - io_stress.go로 upload/read 혼합 부하 발생
## 결과
I/O stress started
baseURL=http://server-debug:8080 stages=10:10s,20:10s seed=1773543669519677789
mode: read-ratio=0.80 write-ratio=0.20 upload-size=500~1000MB
prepare: skip=false count=8 sizeMB=100 userId=io-stress-user
prepared 1/8 id=019cef70-c1bd-7575-86f5-28945d49f7ea path=/io-stress/prepare/01/f-2807897286163344557.bin
prepared 2/8 id=019cef70-c528-74f4-85c9-ed65d0643a98 path=/io-stress/prepare/02/f-7859795261734414912.bin
prepared 3/8 id=019cef70-c887-7f0e-93b5-fc4cb8911e42 path=/io-stress/prepare/03/f-4481704640929478495.bin
prepared 4/8 id=019cef70-cb78-7d1b-9c0e-25937e651bc7 path=/io-stress/prepare/04/f-6695300630016125594.bin
prepared 5/8 id=019cef70-cea1-7853-a4fb-bbfbbfd5de6f path=/io-stress/prepare/05/f-6128032333758751814.bin
prepared 6/8 id=019cef70-d3ba-7c13-af10-c938ad828ce2 path=/io-stress/prepare/06/f-7239049639189643823.bin
prepared 7/8 id=019cef70-d71a-793f-a658-d0c45c0ccafe path=/io-stress/prepare/07/f-4800313602740387577.bin
prepared 8/8 id=019cef70-db1f-7999-b1db-c1cbe83376e5 path=/io-stress/prepare/08/f-4139126328267292189.bin
prepared file IDs: 019cef70-c1bd-7575-86f5-28945d49f7ea,019cef70-c528-74f4-85c9-ed65d0643a98,019cef70-c887-7f0e-93b5-fc4cb8911e42,019cef70-cb78-7d1b-9c0e-25937e651bc7,019cef70-cea1-7853-a4fb-bbfbbfd5de6f,019cef70-d3ba-7c13-af10-c938ad828ce2,019cef70-d71a-793f-a658-d0c45c0ccafe,019cef70-db1f-7999-b1db-c1cbe83376e5
stage: concurrency=10 duration=10s
stage: concurrency=20 duration=10s

================ I/O Stress Report ================
elapsed: 20.001739491s
target mix: read 80.00% / write 20.00%

-- Endpoint p95/p99 + throughput --
GET /files/{id}/download
  count=46 errors=0 errorRate=0.00% bytes=2824.82 MiB throughput=141.23 MiB/s p95=10.00102258s p99=10.001182505s
  status codes: 200:29, 0:17
  status=0 breakdown: context_canceled:17
POST /files/upload
  count=9 errors=0 errorRate=0.00% bytes=5936.00 MiB throughput=296.77 MiB/s p95=8.912914863s p99=8.912914863s
  status codes: 413:7, 0:2
  status=0 breakdown: context_canceled:2

-- Overall Mix --
ops: read=46 (83.64%), write=9 (16.36%), total=55
bytes: read=2824.82 MiB, write=5936.00 MiB, total=8760.82 MiB
aggregate throughput: 438.00 MiB/s

-- External Checks To Run In Parallel --
iostat -x 1 : check %util, await, avgqu-sz
vmstat 1    : check wa(iowait) trend
docker stats: check Block I/O growth + CPU wait symptoms
===================================================

# 실험2)
- 일시: 2026-03-15
- server
    - nginx 에서 upload/download 구현
    - commit 7fef38ed4b7a17b9684d8e472b1b4e71741ec549 (lua에서 업로드 다운로드 구현)
- client
    - io_stress.go로 upload/read 혼합 부하 발생
## 결과
I/O stress started
baseURL=http://nginx:8080 stages=10:10s,20:10s seed=1773543547637709495
mode: read-ratio=0.80 write-ratio=0.20 upload-size=500~1000MB
prepare: skip=false count=8 sizeMB=100 userId=io-stress-user
prepared 1/8 id=019cef6e-e6d8-77ae-ab7d-d9876024ec47 path=/io-stress/prepare/01/f-8234765770297094269.bin
prepared 2/8 id=019cef6e-ec80-7a99-9e3e-b6a608917ea2 path=/io-stress/prepare/02/f-6640625244705422523.bin
prepared 3/8 id=019cef6e-f0f2-71a1-8f75-a74cfed6c76d path=/io-stress/prepare/03/f-1877628656796608235.bin
prepared 4/8 id=019cef6e-f517-7997-8006-a3a108499339 path=/io-stress/prepare/04/f-6134639586653640669.bin
prepared 5/8 id=019cef6e-f8fa-76e3-9fe8-41e34cf6ddd8 path=/io-stress/prepare/05/f-5612515370071251554.bin
prepared 6/8 id=019cef6f-0117-763a-9d62-db76896396f6 path=/io-stress/prepare/06/f-3551601774984314749.bin
prepared 7/8 id=019cef6f-054f-7664-803b-81b80d510296 path=/io-stress/prepare/07/f-593468262252737385.bin
prepared 8/8 id=019cef6f-0b9d-72ca-a319-cef1fd59b193 path=/io-stress/prepare/08/f-4774044589627646052.bin
prepared file IDs: 019cef6e-e6d8-77ae-ab7d-d9876024ec47,019cef6e-ec80-7a99-9e3e-b6a608917ea2,019cef6e-f0f2-71a1-8f75-a74cfed6c76d,019cef6e-f517-7997-8006-a3a108499339,019cef6e-f8fa-76e3-9fe8-41e34cf6ddd8,019cef6f-0117-763a-9d62-db76896396f6,019cef6f-054f-7664-803b-81b80d510296,019cef6f-0b9d-72ca-a319-cef1fd59b193
stage: concurrency=10 duration=10s
stage: concurrency=20 duration=10s

================ I/O Stress Report ================
elapsed: 20.00133979s
target mix: read 80.00% / write 20.00%

-- Endpoint p95/p99 + throughput --
GET /files/{id}/download
  count=56 errors=0 errorRate=0.00% bytes=5321.46 MiB throughput=266.06 MiB/s p95=10.000282124s p99=10.000429703s
  status codes: 200:55, 0:1
  status=0 breakdown: context_canceled:1
POST /files/upload
  count=13 errors=0 errorRate=0.00% bytes=9436.00 MiB throughput=471.77 MiB/s p95=20.001001449s p99=20.001001449s
  status codes: 0:12, 413:1
  status=0 breakdown: context_canceled:12

-- Overall Mix --
ops: read=56 (81.16%), write=13 (18.84%), total=69
bytes: read=5321.46 MiB, write=9436.00 MiB, total=14757.46 MiB
aggregate throughput: 737.82 MiB/s

-- External Checks To Run In Parallel --
iostat -x 1 : check %util, await, avgqu-sz
vmstat 1    : check wa(iowait) trend
docker stats: check Block I/O growth + CPU wait symptoms
===================================================

# 실험3)
- 일시: 2026-03-15
- server
    - commit 7fef38ed4b7a17b9684d8e472b1b4e71741ec549 (lua에서 업로드 다운로드 구현)
    - nginx 에서 upload/download 구현
    - lua 업로드시 "임시파일 재복사 대신 rename 우선 시도"
- client
    - io_stress.go로 upload/read 혼합 부하 발생
## 결과
I/O stress started
baseURL=http://nginx:8080 stages=10:10s,20:10s seed=1773543589157683601
mode: read-ratio=0.80 write-ratio=0.20 upload-size=500~1000MB
prepare: skip=false count=8 sizeMB=100 userId=io-stress-user
prepared 1/8 id=019cef6f-88c1-77a5-b2e2-7e4b9c307d30 path=/io-stress/prepare/01/f-662100794662851093.bin
prepared 2/8 id=019cef6f-8cc7-7484-bf60-258f8243eb99 path=/io-stress/prepare/02/f-3728370739175208003.bin
prepared 3/8 id=019cef6f-917b-7ce2-a9f6-2d8fd7587333 path=/io-stress/prepare/03/f-6096056852900641711.bin
prepared 4/8 id=019cef6f-95a8-7a0b-9651-b573908dd3d4 path=/io-stress/prepare/04/f-540189320203894851.bin
prepared 5/8 id=019cef6f-9999-772b-a6c2-e529ea053b32 path=/io-stress/prepare/05/f-7637053382634550358.bin
prepared 6/8 id=019cef6f-9dd4-7c5a-8333-1a438d2b7973 path=/io-stress/prepare/06/f-552721331573260751.bin
prepared 7/8 id=019cef6f-a39d-7575-926c-9e3a76a9bf29 path=/io-stress/prepare/07/f-4300901837600677609.bin
prepared 8/8 id=019cef6f-a782-7723-b99a-ac0c7aa451d9 path=/io-stress/prepare/08/f-1337885424016922444.bin
prepared file IDs: 019cef6f-88c1-77a5-b2e2-7e4b9c307d30,019cef6f-8cc7-7484-bf60-258f8243eb99,019cef6f-917b-7ce2-a9f6-2d8fd7587333,019cef6f-95a8-7a0b-9651-b573908dd3d4,019cef6f-9999-772b-a6c2-e529ea053b32,019cef6f-9dd4-7c5a-8333-1a438d2b7973,019cef6f-a39d-7575-926c-9e3a76a9bf29,019cef6f-a782-7723-b99a-ac0c7aa451d9
stage: concurrency=10 duration=10s
stage: concurrency=20 duration=10s

================ I/O Stress Report ================
elapsed: 20.00168571s
target mix: read 80.00% / write 20.00%

-- Endpoint p95/p99 + throughput --
GET /files/{id}/download
  count=53 errors=0 errorRate=0.00% bytes=4929.42 MiB throughput=246.45 MiB/s p95=10.001124563s p99=10.001331413s
  status codes: 200:52, 0:1
  status=0 breakdown: context_canceled:1
POST /files/upload
  count=15 errors=0 errorRate=0.00% bytes=11441.00 MiB throughput=572.00 MiB/s p95=18.466790076s p99=18.466790076s
  status codes: 0:14, 413:1
  status=0 breakdown: context_canceled:14

-- Overall Mix --
ops: read=53 (77.94%), write=15 (22.06%), total=68
bytes: read=4929.42 MiB, write=11441.00 MiB, total=16370.42 MiB
aggregate throughput: 818.45 MiB/s

-- External Checks To Run In Parallel --
iostat -x 1 : check %util, await, avgqu-sz
vmstat 1    : check wa(iowait) trend
docker stats: check Block I/O growth + CPU wait symptoms
===================================================