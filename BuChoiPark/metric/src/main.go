package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"container-monitor/collector"
	"container-monitor/output"
)

type displayConfig struct {
	separatorWidth int
	compact        bool
}

type sampleRates struct {
	BlockReadBps  float64
	BlockWriteBps float64
	NetRxBps      float64
	NetTxBps      float64
}

func main() {
	target := flag.String("target", getenv("TARGET_CONTAINER", ""), "Target container name or ID (required)")
	interval := flag.Duration("interval", 1*time.Second, "Polling interval (e.g. 1s, 500ms)")
	outputDir := flag.String("out", getenv("OUTPUT_DIR", "/app/results"), "Output directory path")
	outputFile := flag.String("file", getenv("OUTPUT_FILE", "container-monitor.csv"), "Output csv file name")
	fit := flag.Bool("fit", false, "Fit output width to the current terminal window")
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "--target is required (or set TARGET_CONTAINER)")
		os.Exit(2)
	}
	if *interval <= 0 {
		fmt.Fprintln(os.Stderr, "--interval must be > 0")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	collectorClient, err := collector.NewDockerCollector()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init collector: %v\n", err)
		os.Exit(1)
	}
	defer collectorClient.Close()

	containerID, containerName, err := collectorClient.ResolveContainer(ctx, *target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve target container: %v\n", err)
		os.Exit(1)
	}

	writer, err := output.NewCSVWriter(*outputDir, *outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to init writer: %v\n", err)
		os.Exit(1)
	}
	defer writer.Close()

	display := newDisplayConfig(*fit)

	fmt.Printf("monitoring started target=%s(%s) interval=%s output=%s/%s\n", containerName, containerID[:12], interval.String(), *outputDir, *outputFile)
	fmt.Println(strings.Repeat("-", display.separatorWidth))
	fmt.Printf(" %-8s %-8s %-8s %-12s %-12s %-12s %-12s %4s\n", "time", "cpu%", "mem%", "io_rd/s", "io_wr/s", "net_rx/s", "net_tx/s", "pid")
	fmt.Println(strings.Repeat("-", display.separatorWidth))

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	var previousSample *collector.MetricSample

	for {
		select {
		case <-ctx.Done():
			fmt.Println("monitoring stopped")
			return
		case <-ticker.C:
			sample, err := collectorClient.CollectOnce(ctx, containerID, containerName)
			if err != nil {
				fmt.Fprintf(os.Stderr, "collect error: %v\n", err)
				continue
			}

			if err := writer.Write(sample); err != nil {
				fmt.Fprintf(os.Stderr, "write error: %v\n", err)
				continue
			}

			rates := calculateRates(previousSample, sample)
			printSample(sample, rates, display)
			currentSample := sample
			previousSample = &currentSample
		}
	}
}

func printSample(sample collector.MetricSample, rates sampleRates, display displayConfig) {
	line := fmt.Sprintf(
		" %-8s %7.2f%% %7.2f%% %-12s %-12s %-12s %-12s %4d",
		sample.Timestamp.Format("15:04:05"),
		sample.CPUPercent,
		sample.MemoryPercent,
		humanRate(rates.BlockReadBps),
		humanRate(rates.BlockWriteBps),
		humanRate(rates.NetRxBps),
		humanRate(rates.NetTxBps),
		sample.Pids,
	)

	fmt.Println(line)
}

func calculateRates(previous *collector.MetricSample, current collector.MetricSample) sampleRates {
	if previous == nil {
		return sampleRates{}
	}

	seconds := current.Timestamp.Sub(previous.Timestamp).Seconds()
	if seconds <= 0 {
		return sampleRates{}
	}

	return sampleRates{
		BlockReadBps:  deltaRate(current.BlockReadBytes, previous.BlockReadBytes, seconds),
		BlockWriteBps: deltaRate(current.BlockWriteBytes, previous.BlockWriteBytes, seconds),
		NetRxBps:      deltaRate(current.NetRxBytes, previous.NetRxBytes, seconds),
		NetTxBps:      deltaRate(current.NetTxBytes, previous.NetTxBytes, seconds),
	}
}

func deltaRate(current uint64, previous uint64, seconds float64) float64 {
	if current < previous || seconds <= 0 {
		return 0
	}
	return float64(current-previous) / seconds
}

func newDisplayConfig(fit bool) displayConfig {
	config := displayConfig{
		separatorWidth: 80,
		compact:        false,
	}
	if !fit {
		return config
	}

	width := terminalWidth()
	if width <= 0 {
		width = 80
	}
	if width < 60 {
		width = 60
	}

	config.separatorWidth = width
	config.compact = width < 110
	return config
}

func terminalWidth() int {
	if columns := os.Getenv("COLUMNS"); columns != "" {
		var parsed int
		_, err := fmt.Sscanf(columns, "%d", &parsed)
		if err == nil && parsed > 0 {
			return parsed
		}
	}

	type winsize struct {
		row    uint16
		col    uint16
		xpixel uint16
		ypixel uint16
	}

	ws := &winsize{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.col)
}

func humanBytes(value uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	converted := float64(value)
	unitIndex := 0
	for converted >= 1024 && unitIndex < len(units)-1 {
		converted /= 1024
		unitIndex++
	}
	if unitIndex == 0 {
		return fmt.Sprintf("%.0f%s", converted, units[unitIndex])
	}
	return fmt.Sprintf("%.2f%s", converted, units[unitIndex])
}

func humanRate(value float64) string {
	units := []string{"B/s", "KiB/s", "MiB/s", "GiB/s", "TiB/s"}
	converted := value
	unitIndex := 0
	for converted >= 1024 && unitIndex < len(units)-1 {
		converted /= 1024
		unitIndex++
	}
	if unitIndex == 0 {
		return fmt.Sprintf("%.0f%s", converted, units[unitIndex])
	}
	return fmt.Sprintf("%.2f%s", converted, units[unitIndex])
}

func getenv(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
