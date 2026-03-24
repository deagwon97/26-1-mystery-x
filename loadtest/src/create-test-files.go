//go:build genfiles

// create-test-files.go — 벤치마크용 테스트 파일 생성 독립 실행 도구
//
// Usage:
//   go run -tags genfiles create-test-files.go [-data-dir ./bench-data] [-seed 42]
//
// benchmark.go 와 동일한 seed/구조로 파일을 생성하므로,
// 미리 파일을 만들어 두면 벤치마크 시작 시 재생성을 건너뜁니다.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

type fileCase struct {
	Name      string
	Size      int64
	Count     int
	Prefix    string
	Folders   []string
	PerFolder int
}

var cases = []fileCase{
	{Name: "small", Size: 3 * 1024 * 1024, Count: 1000, Prefix: "small",
		Folders: []string{"folderA", "folderB", "folderC", "folderD"}, PerFolder: 250},
	{Name: "medium", Size: 30 * 1024 * 1024, Count: 100, Prefix: "medium",
		Folders: []string{"folderA", "folderB"}, PerFolder: 50},
	{Name: "large", Size: 100 * 1024 * 1024, Count: 30, Prefix: "large",
		Folders: []string{"folderA", "folderB"}, PerFolder: 15},
	{Name: "xlarge", Size: 300 * 1024 * 1024, Count: 10, Prefix: "xlarge",
		Folders: []string{"folderA"}, PerFolder: 10},
	{Name: "huge", Size: 500 * 1024 * 1024, Count: 6, Prefix: "huge",
		Folders: []string{"folderA"}, PerFolder: 6},
}

func main() {
	dataDir := flag.String("data-dir", "./bench-data", "output directory")
	seed := flag.Int64("seed", 42, "random seed (same as benchmark.go)")
	flag.Parse()

	start := time.Now()
	totalFiles := 0
	totalBytes := int64(0)

	for _, fc := range cases {
		caseStart := time.Now()
		idx := 0
		for _, folder := range fc.Folders {
			dir := filepath.Join(*dataDir, fc.Prefix, folder)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fatalf("mkdir %s: %v", dir, err)
			}
			for i := 0; i < fc.PerFolder; i++ {
				idx++
				fname := fmt.Sprintf("%s_%04d.bin", fc.Prefix, idx)
				fpath := filepath.Join(dir, fname)

				// Skip if already exists with correct size
				if info, err := os.Stat(fpath); err == nil && info.Size() == fc.Size {
					fmt.Printf("  [%s] %s — already exists, skip\n", fc.Name, fname)
					totalFiles++
					totalBytes += fc.Size
					continue
				}

				hash, err := genFile(fpath, fc.Size, *seed+int64(idx))
				if err != nil {
					fatalf("generate %s: %v", fpath, err)
				}

				totalFiles++
				totalBytes += fc.Size
				fmt.Printf("  [%s] %s  sha256=%s\n", fc.Name, fname, hash[:16]+"…")
			}
		}
		fmt.Printf("  [%s] done (%d files, %.1fs)\n\n", fc.Name, fc.Count, time.Since(caseStart).Seconds())
	}

	fmt.Printf("=== Complete: %d files, %.2f GB, %.1fs ===\n",
		totalFiles, float64(totalBytes)/(1024*1024*1024), time.Since(start).Seconds())
}

func genFile(path string, size int64, seed int64) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	rng := rand.New(rand.NewSource(seed))
	hasher := sha256.New()
	w := io.MultiWriter(f, hasher)

	buf := make([]byte, 64*1024)
	remaining := size
	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		rng.Read(buf[:n])
		if _, err := w.Write(buf[:n]); err != nil {
			return "", err
		}
		remaining -= n
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
