package output

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"container-monitor/collector"
)

type CSVWriter struct {
	file *os.File
	csv  *csv.Writer
}

func NewCSVWriter(outputDir string, fileName string) (*CSVWriter, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}

	path := filepath.Join(outputDir, fileName)
	isNewFile := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		isNewFile = true
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open metrics file: %w", err)
	}

	w := csv.NewWriter(f)
	cw := &CSVWriter{file: f, csv: w}
	if isNewFile {
		if err := cw.csv.Write([]string{
			"timestamp",
			"container_id",
			"container_name",
			"cpu_percent",
			"memory_usage",
			"memory_limit",
			"memory_percent",
			"mem_pgpgin",
			"mem_pgpgout",
			"block_read_bytes",
			"block_write_bytes",
			"net_rx_bytes",
			"net_tx_bytes",
			"pids",
		}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("write csv header: %w", err)
		}
		cw.csv.Flush()
		if err := cw.csv.Error(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("flush csv header: %w", err)
		}
	}

	return cw, nil
}

func (w *CSVWriter) Write(sample collector.MetricSample) error {
	row := []string{
		sample.Timestamp.Format(time.RFC3339Nano),
		sample.ContainerID,
		sample.ContainerName,
		fmt.Sprintf("%.2f", sample.CPUPercent),
		strconv.FormatUint(sample.MemoryUsage, 10),
		strconv.FormatUint(sample.MemoryLimit, 10),
		fmt.Sprintf("%.2f", sample.MemoryPercent),
		strconv.FormatUint(sample.MemPgPgIn, 10),
		strconv.FormatUint(sample.MemPgPgOut, 10),
		strconv.FormatUint(sample.BlockReadBytes, 10),
		strconv.FormatUint(sample.BlockWriteBytes, 10),
		strconv.FormatUint(sample.NetRxBytes, 10),
		strconv.FormatUint(sample.NetTxBytes, 10),
		strconv.FormatUint(sample.Pids, 10),
	}

	if err := w.csv.Write(row); err != nil {
		return fmt.Errorf("write csv row: %w", err)
	}
	w.csv.Flush()
	if err := w.csv.Error(); err != nil {
		return fmt.Errorf("flush csv row: %w", err)
	}
	return nil
}

func (w *CSVWriter) Close() error {
	w.csv.Flush()
	if err := w.csv.Error(); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}
