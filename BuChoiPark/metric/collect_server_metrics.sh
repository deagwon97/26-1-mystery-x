#!/usr/bin/env bash
set -euo pipefail

# 사용법:
#   collect_server_metrics.sh <results_dir> [interval_sec] [server_container]
# 예시:
#   collect_server_metrics.sh ./results/20260308_153000 1 buchoipark-server-debug

RESULTS_DIR="${1:?results dir is required}"
INTERVAL_SEC="${2:-1}"
SERVER_CONTAINER="${3:-buchoipark-server-debug}"

mkdir -p "$RESULTS_DIR"

DOCKER_STATS_CSV="$RESULTS_DIR/docker_stats.csv"
SOCKET_ESTAB_CSV="$RESULTS_DIR/socket_established.csv"
SOCKET_RAW_LOG="$RESULTS_DIR/socket_raw.log"
CGROUP_IO_CSV="$RESULTS_DIR/cgroup_io.csv"
CGROUP_IO_RAW_LOG="$RESULTS_DIR/cgroup_io_raw.log"
HOST_CPU_CSV="$RESULTS_DIR/host_cpu.csv"
PSI_IO_CSV="$RESULTS_DIR/psi_io.csv"
COLLECTOR_LOG="$RESULTS_DIR/collector.log"

cat > "$DOCKER_STATS_CSV" <<'EOF'
timestamp,cpu_perc,mem_usage,mem_perc,net_io,block_io,pids
EOF

cat > "$SOCKET_ESTAB_CSV" <<'EOF'
timestamp,established,source
EOF

cat > "$CGROUP_IO_CSV" <<'EOF'
timestamp,read_bytes,write_bytes,read_ios,write_ios,discard_bytes,discard_ios,source
EOF

cat > "$HOST_CPU_CSV" <<'EOF'
timestamp,cpu_total_delta,cpu_idle_delta,cpu_iowait_delta,cpu_iowait_pct
EOF

cat > "$PSI_IO_CSV" <<'EOF'
timestamp,some_avg10,some_avg60,some_avg300,some_total,full_avg10,full_avg60,full_avg300,full_total,source
EOF

echo "[$(date -Iseconds)] collector started (container=$SERVER_CONTAINER interval=${INTERVAL_SEC}s)" >> "$COLLECTOR_LOG"

prev_cpu_total=""
prev_cpu_idle=""
prev_cpu_iowait=""

read_host_cpu_stat() {
  awk '/^cpu / {print $2,$3,$4,$5,$6,$7,$8,$9,$10}' /proc/stat 2>/dev/null | head -n 1
}

while true; do
  ts="$(date -Iseconds)"

  # docker stats: 컨테이너 리소스 사용률 기록
  stats_line="$(docker stats --no-stream --format '{{.CPUPerc}},{{.MemUsage}},{{.MemPerc}},{{.NetIO}},{{.BlockIO}},{{.PIDs}}' "$SERVER_CONTAINER" 2>/dev/null || true)"
  if [[ -n "$stats_line" ]]; then
    echo "$ts,$stats_line" >> "$DOCKER_STATS_CSV"
  else
    echo "[$ts] docker stats unavailable" >> "$COLLECTOR_LOG"
  fi

  # 소켓 상태 수집: ss -> /proc/net/tcp fallback 순서
  ss_out="$(docker exec "$SERVER_CONTAINER" sh -lc 'ss -s 2>/dev/null || true' 2>/dev/null || true)"
  if [[ -n "$ss_out" ]]; then
    estab="$(printf '%s\n' "$ss_out" | sed -n 's/.*estab \([0-9][0-9]*\).*/\1/p' | head -n 1)"
    if [[ -n "$estab" ]]; then
      echo "$ts,$estab,ss" >> "$SOCKET_ESTAB_CSV"
      {
        echo "===== $ts (source=ss) ====="
        printf '%s\n' "$ss_out"
      } >> "$SOCKET_RAW_LOG"
    else
      echo "[$ts] ss output exists but estab parse failed" >> "$COLLECTOR_LOG"
      echo "$ts,-1,ss-parse-failed" >> "$SOCKET_ESTAB_CSV"
    fi
  else
    # /proc/net/tcp, /proc/net/tcp6 의 state=01(ESTABLISHED) 개수 합산
    proc_out="$(docker exec "$SERVER_CONTAINER" sh -lc '
      c4=$(awk "NR>1 && \$4==\"01\" {c++} END {print c+0}" /proc/net/tcp 2>/dev/null || echo 0)
      c6=$(awk "NR>1 && \$4==\"01\" {c++} END {print c+0}" /proc/net/tcp6 2>/dev/null || echo 0)
      total=$((c4 + c6))
      echo "established=$total"
      echo "tcp4=$c4 tcp6=$c6"
    ' 2>/dev/null || true)"

    estab="$(printf '%s\n' "$proc_out" | sed -n 's/^established=\([0-9][0-9]*\)$/\1/p' | head -n 1)"
    if [[ -n "$estab" ]]; then
      echo "$ts,$estab,procfs" >> "$SOCKET_ESTAB_CSV"
      {
        echo "===== $ts (source=procfs) ====="
        printf '%s\n' "$proc_out"
      } >> "$SOCKET_RAW_LOG"
    else
      echo "[$ts] socket info unavailable (ss/procfs 모두 실패)" >> "$COLLECTOR_LOG"
      echo "$ts,-1,unavailable" >> "$SOCKET_ESTAB_CSV"
    fi
  fi

  # 컨테이너 cgroup I/O 수집: cgroup v2(io.stat) -> cgroup v1(blkio) fallback
  io_out="$(docker exec "$SERVER_CONTAINER" sh -lc '
    if [ -f /sys/fs/cgroup/io.stat ]; then
      echo "__SRC__=cgroupv2"
      cat /sys/fs/cgroup/io.stat 2>/dev/null || true
    elif [ -f /sys/fs/cgroup/blkio/blkio.throttle.io_service_bytes ]; then
      echo "__SRC__=cgroupv1"
      echo "__BYTES__"
      cat /sys/fs/cgroup/blkio/blkio.throttle.io_service_bytes 2>/dev/null || true
      echo "__IOS__"
      cat /sys/fs/cgroup/blkio/blkio.throttle.io_serviced 2>/dev/null || true
    fi
  ' 2>/dev/null || true)"

  if [[ -n "$io_out" ]]; then
    io_src="$(printf '%s\n' "$io_out" | sed -n 's/^__SRC__=\(.*\)$/\1/p' | head -n 1)"
    read_b="-1"
    write_b="-1"
    read_ios="-1"
    write_ios="-1"
    discard_b="-1"
    discard_ios="-1"

    if [[ "$io_src" == "cgroupv2" ]]; then
      read_b="$(printf '%s\n' "$io_out" | awk '{for(i=1;i<=NF;i++){if($i ~ /^rbytes=/){split($i,a,"="); s+=a[2]}}} END{print s+0}')"
      write_b="$(printf '%s\n' "$io_out" | awk '{for(i=1;i<=NF;i++){if($i ~ /^wbytes=/){split($i,a,"="); s+=a[2]}}} END{print s+0}')"
      read_ios="$(printf '%s\n' "$io_out" | awk '{for(i=1;i<=NF;i++){if($i ~ /^rios=/){split($i,a,"="); s+=a[2]}}} END{print s+0}')"
      write_ios="$(printf '%s\n' "$io_out" | awk '{for(i=1;i<=NF;i++){if($i ~ /^wios=/){split($i,a,"="); s+=a[2]}}} END{print s+0}')"
      discard_b="$(printf '%s\n' "$io_out" | awk '{for(i=1;i<=NF;i++){if($i ~ /^dbytes=/){split($i,a,"="); s+=a[2]}}} END{print s+0}')"
      discard_ios="$(printf '%s\n' "$io_out" | awk '{for(i=1;i<=NF;i++){if($i ~ /^dios=/){split($i,a,"="); s+=a[2]}}} END{print s+0}')"
    elif [[ "$io_src" == "cgroupv1" ]]; then
      bytes_section="$(printf '%s\n' "$io_out" | awk '/^__BYTES__/{f=1;next}/^__IOS__/{f=0}f')"
      ios_section="$(printf '%s\n' "$io_out" | awk '/^__IOS__/{f=1;next}f')"

      read_b="$(printf '%s\n' "$bytes_section" | awk '$1 ~ /^[0-9]+:[0-9]+$/ && $2=="Read" {s+=$3} END{print s+0}')"
      write_b="$(printf '%s\n' "$bytes_section" | awk '$1 ~ /^[0-9]+:[0-9]+$/ && $2=="Write" {s+=$3} END{print s+0}')"
      read_ios="$(printf '%s\n' "$ios_section" | awk '$1 ~ /^[0-9]+:[0-9]+$/ && $2=="Read" {s+=$3} END{print s+0}')"
      write_ios="$(printf '%s\n' "$ios_section" | awk '$1 ~ /^[0-9]+:[0-9]+$/ && $2=="Write" {s+=$3} END{print s+0}')"
    fi

    echo "$ts,$read_b,$write_b,$read_ios,$write_ios,$discard_b,$discard_ios,$io_src" >> "$CGROUP_IO_CSV"
    {
      echo "===== $ts (source=$io_src) ====="
      printf '%s\n' "$io_out"
    } >> "$CGROUP_IO_RAW_LOG"
  else
    echo "[$ts] cgroup io unavailable" >> "$COLLECTOR_LOG"
    echo "$ts,-1,-1,-1,-1,-1,-1,unavailable" >> "$CGROUP_IO_CSV"
  fi

  # host CPU iowait(증분 기반) 수집: 디스크 대기 증가 여부 판단용
  cpu_line="$(read_host_cpu_stat || true)"
  if [[ -n "$cpu_line" ]]; then
    read -r u n s iow idl irq sirq stl _ <<< "$cpu_line"
    total_now=$((u + n + s + iow + idl + irq + sirq + stl))
    idle_now=$idl
    iowait_now=$iow

    if [[ -n "$prev_cpu_total" ]]; then
      total_delta=$((total_now - prev_cpu_total))
      idle_delta=$((idle_now - prev_cpu_idle))
      iowait_delta=$((iowait_now - prev_cpu_iowait))

      if (( total_delta > 0 )); then
        iowait_pct="$(awk -v iw="$iowait_delta" -v td="$total_delta" 'BEGIN {printf "%.2f", (iw*100.0)/td}')"
      else
        iowait_pct="0.00"
      fi
      echo "$ts,$total_delta,$idle_delta,$iowait_delta,$iowait_pct" >> "$HOST_CPU_CSV"
    else
      echo "$ts,-1,-1,-1,-1" >> "$HOST_CPU_CSV"
    fi

    prev_cpu_total="$total_now"
    prev_cpu_idle="$idle_now"
    prev_cpu_iowait="$iowait_now"
  else
    echo "[$ts] host cpu stat unavailable" >> "$COLLECTOR_LOG"
    echo "$ts,-1,-1,-1,-1" >> "$HOST_CPU_CSV"
  fi

  # 컨테이너 PSI I/O 압력 수집: some/full stall로 I/O 포화 징후 확인
  psi_out="$(docker exec "$SERVER_CONTAINER" sh -lc 'cat /proc/pressure/io 2>/dev/null || true' 2>/dev/null || true)"
  if [[ -n "$psi_out" ]]; then
    some_line="$(printf '%s\n' "$psi_out" | awk '/^some / {print}')"
    full_line="$(printf '%s\n' "$psi_out" | awk '/^full / {print}')"

    some_avg10="$(printf '%s\n' "$some_line" | sed -n 's/.*avg10=\([0-9.]*\).*/\1/p' | head -n 1)"
    some_avg60="$(printf '%s\n' "$some_line" | sed -n 's/.*avg60=\([0-9.]*\).*/\1/p' | head -n 1)"
    some_avg300="$(printf '%s\n' "$some_line" | sed -n 's/.*avg300=\([0-9.]*\).*/\1/p' | head -n 1)"
    some_total="$(printf '%s\n' "$some_line" | sed -n 's/.*total=\([0-9][0-9]*\).*/\1/p' | head -n 1)"

    full_avg10="$(printf '%s\n' "$full_line" | sed -n 's/.*avg10=\([0-9.]*\).*/\1/p' | head -n 1)"
    full_avg60="$(printf '%s\n' "$full_line" | sed -n 's/.*avg60=\([0-9.]*\).*/\1/p' | head -n 1)"
    full_avg300="$(printf '%s\n' "$full_line" | sed -n 's/.*avg300=\([0-9.]*\).*/\1/p' | head -n 1)"
    full_total="$(printf '%s\n' "$full_line" | sed -n 's/.*total=\([0-9][0-9]*\).*/\1/p' | head -n 1)"

    some_avg10="${some_avg10:--1}"
    some_avg60="${some_avg60:--1}"
    some_avg300="${some_avg300:--1}"
    some_total="${some_total:--1}"
    full_avg10="${full_avg10:--1}"
    full_avg60="${full_avg60:--1}"
    full_avg300="${full_avg300:--1}"
    full_total="${full_total:--1}"

    echo "$ts,$some_avg10,$some_avg60,$some_avg300,$some_total,$full_avg10,$full_avg60,$full_avg300,$full_total,psi" >> "$PSI_IO_CSV"
  else
    echo "[$ts] psi io unavailable" >> "$COLLECTOR_LOG"
    echo "$ts,-1,-1,-1,-1,-1,-1,-1,-1,unavailable" >> "$PSI_IO_CSV"
  fi

  sleep "$INTERVAL_SEC"
done
