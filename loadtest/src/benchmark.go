//go:build !genfiles

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type FileCase struct {
	Name     string
	Size     int64
	Count    int
	Prefix   string   // e.g. "small", "medium"
	Folders  []string // sub-folder names
	PerFolder int
}

type UploadedFile struct {
	ID       string
	FilePath string // virtual path on server
	LocalPath string // path on disk
	Hash     string // sha256 hex
	Size     int64
	Case     string
}

type LatencyStats struct {
	Min, Max, Avg, P50, P95, P99 float64
}

type CaseResult struct {
	CaseName    string
	Files       int
	Concurrency int
	TotalSec    float64
	Stats       LatencyStats
	MBps        float64
	OK          int
	Fail        int
	HashFail    int
	Verified    bool
	Latency     float64 // single-op latency (ms) for move/delete
}

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

var (
	flagHost       string
	flagUserID     string
	flagUploadMode string
	flagDataDir    string
	flagSeed       int64
	flagTimeout    time.Duration

	httpClient *http.Client

	fileCases = []FileCase{
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
)

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	flag.StringVar(&flagHost, "host", "http://localhost:8080", "target server URL")
	flag.StringVar(&flagUserID, "user-id", "bench-user", "test userId")
	flag.StringVar(&flagUploadMode, "upload-mode", "multipart", "upload mode: multipart or raw")
	flag.StringVar(&flagDataDir, "data-dir", "./bench-data", "directory for test files")
	flag.Int64Var(&flagSeed, "seed", 42, "random seed for file generation")
	timeout := flag.Duration("timeout", 5*time.Minute, "per-request timeout")
	flag.Parse()
	flagTimeout = *timeout

	httpClient = &http.Client{Timeout: flagTimeout}

	// Strip trailing slash
	flagHost = strings.TrimRight(flagHost, "/")

	// Step 0: Generate test files
	uploaded := generateTestFiles()

	// Step 1: Upload
	fmt.Println()
	uploadResults, uploadedFiles := scenarioUpload(uploaded)
	printUploadDownloadTable("Upload ("+flagUploadMode+")", uploadResults)

	// Step 2: Download
	fmt.Println()
	downloadResults := scenarioDownload(uploadedFiles)
	printDownloadTable("Download", downloadResults)

	// Step 3: Folder List
	fmt.Println()
	folderResults := scenarioFolderList(uploadedFiles)
	printFolderListTable("Folder List", folderResults)

	// Step 4: Move Folder
	fmt.Println()
	moveResults := scenarioMoveFolder()
	printMoveDeleteTable("Move Folder", moveResults)

	// Step 5: Delete Files
	fmt.Println()
	deleteFileResults := scenarioDeleteFiles()
	printMoveDeleteTable("Delete Files", deleteFileResults)

	// Step 6: Delete Folder
	fmt.Println()
	deleteFolderResults := scenarioDeleteFolder()
	printMoveDeleteTable("Delete Folder", deleteFolderResults)

	// Summary
	fmt.Println()
	printSummary(uploadResults, downloadResults, folderResults, moveResults, deleteFileResults, deleteFolderResults)
}

// ---------------------------------------------------------------------------
// Test file generation
// ---------------------------------------------------------------------------

type localFile struct {
	Path     string
	Hash     string
	Size     int64
	Case     string
	VirtPath string // virtual path: /bench/<prefix>/<folder>/<filename>
}

func generateTestFiles() []localFile {
	fmt.Println("=== Loading test files ===")
	var files []localFile

	for _, fc := range fileCases {
		caseDir := filepath.Join(flagDataDir, fc.Prefix)
		idx := 0
		reused, created := 0, 0
		for _, folder := range fc.Folders {
			folderDir := filepath.Join(caseDir, folder)
			for i := 0; i < fc.PerFolder; i++ {
				idx++
				fname := fmt.Sprintf("%s_%04d.bin", fc.Prefix, idx)
				fpath := filepath.Join(folderDir, fname)
				virtPath := fmt.Sprintf("/bench/%s/%s/%s", fc.Prefix, folder, fname)

				if info, err := os.Stat(fpath); err == nil && info.Size() == fc.Size {
					h, err := hashFile(fpath)
					if err != nil {
						fatalf("hash existing file %s: %v", fpath, err)
					}
					files = append(files, localFile{
						Path: fpath, Hash: h, Size: fc.Size,
						Case: fc.Name, VirtPath: virtPath,
					})
					reused++
					continue
				}

				if err := os.MkdirAll(folderDir, 0o755); err != nil {
					fatalf("mkdir %s: %v", folderDir, err)
				}

				h, err := generateFile(fpath, fc.Size, flagSeed+int64(idx))
				if err != nil {
					fatalf("generate %s: %v", fpath, err)
				}

				files = append(files, localFile{
					Path: fpath, Hash: h, Size: fc.Size,
					Case: fc.Name, VirtPath: virtPath,
				})
				created++

				if created%100 == 0 {
					fmt.Printf("  [%s] creating... %d files\n", fc.Name, created)
				}
			}
		}
		if created > 0 {
			fmt.Printf("  [%s] created %d, reused %d\n", fc.Name, created, reused)
		} else {
			fmt.Printf("  [%s] reused %d files (all exist)\n", fc.Name, reused)
		}
	}

	fmt.Printf("  Total: %d files loaded\n", len(files))
	return files
}

func generateFile(path string, size int64, seed int64) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	rng := rand.New(rand.NewSource(seed))
	hasher := sha256.New()
	w := io.MultiWriter(f, hasher)

	buf := make([]byte, 64*1024) // 64KB chunks
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

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// Scenario 1: Upload
// ---------------------------------------------------------------------------

func scenarioUpload(files []localFile) ([]CaseResult, []UploadedFile) {
	fmt.Println("=== Scenario: Upload ===")

	var allUploaded []UploadedFile
	var results []CaseResult

	for _, fc := range fileCases {
		// Filter files for this case
		var caseFiles []localFile
		for _, f := range files {
			if f.Case == fc.Name {
				caseFiles = append(caseFiles, f)
			}
		}

		fmt.Printf("  [%s] uploading %d files (concurrency=%d)...\n", fc.Name, len(caseFiles), len(caseFiles))

		type uploadResult struct {
			file    localFile
			id      string
			latency time.Duration
			err     error
		}

		resultsCh := make(chan uploadResult, len(caseFiles))
		var wg sync.WaitGroup

		start := time.Now()
		for _, lf := range caseFiles {
			wg.Add(1)
			go func(lf localFile) {
				defer wg.Done()
				t := time.Now()
				id, err := doUpload(lf)
				resultsCh <- uploadResult{file: lf, id: id, latency: time.Since(t), err: err}
			}(lf)
		}
		wg.Wait()
		close(resultsCh)
		totalDur := time.Since(start)

		var latencies []float64
		ok, fail := 0, 0
		for r := range resultsCh {
			latencies = append(latencies, float64(r.latency.Milliseconds()))
			if r.err != nil {
				fail++
				fmt.Printf("    FAIL upload %s: %v\n", r.file.VirtPath, r.err)
			} else {
				ok++
				allUploaded = append(allUploaded, UploadedFile{
					ID:        r.id,
					FilePath:  r.file.VirtPath,
					LocalPath: r.file.Path,
					Hash:      r.file.Hash,
					Size:      r.file.Size,
					Case:      r.file.Case,
				})
			}
		}

		totalMB := float64(fc.Size) * float64(ok) / (1024 * 1024)
		results = append(results, CaseResult{
			CaseName:    fc.Name,
			Files:       len(caseFiles),
			Concurrency: len(caseFiles),
			TotalSec:    totalDur.Seconds(),
			Stats:       calcStats(latencies),
			MBps:        totalMB / totalDur.Seconds(),
			OK:          ok,
			Fail:        fail,
		})
	}

	return results, allUploaded
}

func doUpload(lf localFile) (string, error) {
	if flagUploadMode == "raw" {
		return doUploadRaw(lf)
	}
	return doUploadMultipart(lf)
}

func doUploadMultipart(lf localFile) (string, error) {
	f, err := os.Open(lf.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer writer.Close()

		_ = writer.WriteField("userId", flagUserID)
		_ = writer.WriteField("filePath", lf.VirtPath)

		part, err := writer.CreateFormFile("file", filepath.Base(lf.Path))
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, f); err != nil {
			pw.CloseWithError(err)
			return
		}
	}()

	req, err := http.NewRequest("POST", flagHost+"/files/upload", pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct{ ID string `json:"id"` }
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %v", err)
	}
	return result.ID, nil
}

func doUploadRaw(lf localFile) (string, error) {
	f, err := os.Open(lf.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	req, err := http.NewRequest("POST", flagHost+"/files/upload", f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-User-Id", flagUserID)
	req.Header.Set("X-File-Path", lf.VirtPath)
	req.Header.Set("X-File-Name", filepath.Base(lf.Path))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct{ ID string `json:"id"` }
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %v", err)
	}
	return result.ID, nil
}

// ---------------------------------------------------------------------------
// Scenario 2: Download
// ---------------------------------------------------------------------------

func scenarioDownload(files []UploadedFile) []CaseResult {
	fmt.Println("=== Scenario: Download ===")

	var results []CaseResult

	for _, fc := range fileCases {
		var caseFiles []UploadedFile
		for _, f := range files {
			if f.Case == fc.Name {
				caseFiles = append(caseFiles, f)
			}
		}
		if len(caseFiles) == 0 {
			continue
		}

		fmt.Printf("  [%s] downloading %d files (concurrency=%d)...\n", fc.Name, len(caseFiles), len(caseFiles))

		type dlResult struct {
			latency  time.Duration
			err      error
			hashFail bool
		}

		resultsCh := make(chan dlResult, len(caseFiles))
		var wg sync.WaitGroup

		start := time.Now()
		for _, uf := range caseFiles {
			wg.Add(1)
			go func(uf UploadedFile) {
				defer wg.Done()
				t := time.Now()
				hashFail, err := doDownloadAndVerify(uf)
				resultsCh <- dlResult{latency: time.Since(t), err: err, hashFail: hashFail}
			}(uf)
		}
		wg.Wait()
		close(resultsCh)
		totalDur := time.Since(start)

		var latencies []float64
		ok, fail, hashFail := 0, 0, 0
		for r := range resultsCh {
			latencies = append(latencies, float64(r.latency.Milliseconds()))
			if r.err != nil {
				fail++
			} else if r.hashFail {
				hashFail++
			} else {
				ok++
			}
		}

		totalMB := float64(fc.Size) * float64(ok+hashFail) / (1024 * 1024)
		results = append(results, CaseResult{
			CaseName:    fc.Name,
			Files:       len(caseFiles),
			Concurrency: len(caseFiles),
			TotalSec:    totalDur.Seconds(),
			Stats:       calcStats(latencies),
			MBps:        totalMB / totalDur.Seconds(),
			OK:          ok,
			Fail:        fail,
			HashFail:    hashFail,
		})
	}

	return results
}

func doDownloadAndVerify(uf UploadedFile) (hashFail bool, err error) {
	resp, err := httpClient.Get(flagHost + "/files/" + uf.ID + "/download")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	hasher := sha256.New()
	n, err := io.Copy(hasher, resp.Body)
	if err != nil {
		return false, fmt.Errorf("read body: %v", err)
	}

	if n != uf.Size {
		return true, nil
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != uf.Hash {
		return true, nil
	}

	return false, nil
}

// ---------------------------------------------------------------------------
// Scenario 3: Folder List
// ---------------------------------------------------------------------------

func scenarioFolderList(files []UploadedFile) []CaseResult {
	fmt.Println("=== Scenario: Folder List ===")

	// Build expected folder → file count map
	folderCounts := make(map[string]int)
	for _, f := range files {
		dir := filepath.Dir(f.FilePath) // e.g. /bench/small/folderA
		folderCounts[dir]++
	}

	// Also add parent folders: /bench/small, /bench
	parentCounts := make(map[string]int)
	for dir, cnt := range folderCounts {
		parentCounts[dir] = cnt
		parent := filepath.Dir(dir)
		for parent != "/" && parent != "." {
			parentCounts[parent] += cnt
			parent = filepath.Dir(parent)
		}
	}

	type folderResult struct {
		folder   string
		expected int
		got      int
		latency  time.Duration
		err      error
	}

	var folders []string
	for f := range folderCounts {
		folders = append(folders, f)
	}
	sort.Strings(folders)

	var results []CaseResult
	var allLatencies []float64
	ok, fail, verifyFail := 0, 0, 0

	start := time.Now()
	for _, folder := range folders {
		t := time.Now()
		got, err := doFolderList(folder)
		lat := time.Since(t)
		allLatencies = append(allLatencies, float64(lat.Milliseconds()))

		expected := folderCounts[folder]
		if err != nil {
			fail++
			fmt.Printf("    FAIL folder list %s: %v\n", folder, err)
		} else if got != expected {
			verifyFail++
			fmt.Printf("    VERIFY FAIL %s: expected %d, got %d\n", folder, expected, got)
		} else {
			ok++
		}
	}
	totalDur := time.Since(start)

	results = append(results, CaseResult{
		CaseName:    "all folders",
		Files:       len(folders),
		Concurrency: 1,
		TotalSec:    totalDur.Seconds(),
		Stats:       calcStats(allLatencies),
		OK:          ok,
		Fail:        fail,
		HashFail:    verifyFail,
		Verified:    verifyFail == 0 && fail == 0,
	})

	return results
}

func doFolderList(folderPath string) (int, error) {
	u := fmt.Sprintf("%s/files/folder?folderPath=%s&userId=%s",
		flagHost, url.QueryEscape(folderPath), url.QueryEscape(flagUserID))

	resp, err := httpClient.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var items []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return 0, fmt.Errorf("decode: %v", err)
	}
	return len(items), nil
}

// ---------------------------------------------------------------------------
// Scenario 4: Move Folder
// ---------------------------------------------------------------------------

type moveCase struct {
	Name  string
	From  string
	To    string
	Files int
}

func scenarioMoveFolder() []CaseResult {
	fmt.Println("=== Scenario: Move Folder ===")

	cases := []moveCase{
		{"small/folderA", "/bench/small/folderA", "/bench/small/moved_folderA", 250},
		{"small/folderB", "/bench/small/folderB", "/bench/small/moved_folderB", 250},
		{"medium/folderA", "/bench/medium/folderA", "/bench/medium/moved_folderA", 50},
		{"large/folderA", "/bench/large/folderA", "/bench/large/moved_folderA", 15},
		{"xlarge (parent)", "/bench/xlarge", "/bench/xlarge_moved", 10},
	}

	var results []CaseResult
	for _, mc := range cases {
		fmt.Printf("  [%s] moving %s → %s (%d files)...\n", mc.Name, mc.From, mc.To, mc.Files)

		t := time.Now()
		err := doMoveFolder(mc.From, mc.To)
		lat := time.Since(t)

		verified := false
		if err != nil {
			fmt.Printf("    FAIL: %v\n", err)
		} else {
			// Verify: old path should be empty, new path should have files
			oldCount, err1 := doFolderList(mc.From)
			newCount, err2 := doFolderList(mc.To)
			if err1 != nil && !isNotFound(err1) {
				fmt.Printf("    VERIFY FAIL (old path): %v\n", err1)
			} else if err2 != nil {
				fmt.Printf("    VERIFY FAIL (new path): %v\n", err2)
			} else if oldCount != 0 {
				fmt.Printf("    VERIFY FAIL: old path still has %d files\n", oldCount)
			} else if newCount != mc.Files {
				fmt.Printf("    VERIFY FAIL: new path has %d files, expected %d\n", newCount, mc.Files)
			} else {
				verified = true
			}
		}

		results = append(results, CaseResult{
			CaseName: mc.Name,
			Files:    mc.Files,
			Latency:  float64(lat.Milliseconds()),
			Verified: verified,
			OK:       boolToInt(err == nil),
			Fail:     boolToInt(err != nil),
		})
	}

	return results
}

func doMoveFolder(from, to string) error {
	body, _ := json.Marshal(map[string]string{"fromPath": from, "toPath": to})
	req, err := http.NewRequest("POST", flagHost+"/files/move-folder", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Scenario 5: Delete Files
// ---------------------------------------------------------------------------

type deleteFileCase struct {
	Name       string
	FolderPath string
	Files      int
}

func scenarioDeleteFiles() []CaseResult {
	fmt.Println("=== Scenario: Delete Files ===")

	cases := []deleteFileCase{
		{"small/folderC", "/bench/small/folderC", 250},
		{"small/folderD", "/bench/small/folderD", 250},
		{"medium/folderB", "/bench/medium/folderB", 50},
	}

	var results []CaseResult
	for _, dc := range cases {
		fmt.Printf("  [%s] listing files in %s...\n", dc.Name, dc.FolderPath)

		// Get file list first
		filePaths, err := doFolderListPaths(dc.FolderPath)
		if err != nil {
			fmt.Printf("    FAIL list: %v\n", err)
			results = append(results, CaseResult{CaseName: dc.Name, Files: dc.Files, Fail: 1})
			continue
		}

		fmt.Printf("  [%s] deleting %d files (concurrency=%d)...\n", dc.Name, len(filePaths), len(filePaths))

		type delResult struct {
			latency time.Duration
			err     error
		}

		resultsCh := make(chan delResult, len(filePaths))
		var wg sync.WaitGroup

		start := time.Now()
		for _, fp := range filePaths {
			wg.Add(1)
			go func(fp string) {
				defer wg.Done()
				t := time.Now()
				err := doDeleteFile(fp)
				resultsCh <- delResult{latency: time.Since(t), err: err}
			}(fp)
		}
		wg.Wait()
		close(resultsCh)
		totalDur := time.Since(start)

		var latencies []float64
		ok, fail := 0, 0
		for r := range resultsCh {
			latencies = append(latencies, float64(r.latency.Milliseconds()))
			if r.err != nil {
				fail++
				fmt.Printf("    DELETE FAIL: %v\n", r.err)
			} else {
				ok++
			}
		}

		// Verify: folder should be empty
		verified := false
		afterCount, err := doFolderList(dc.FolderPath)
		if err != nil && !isNotFound(err) {
			fmt.Printf("    VERIFY FAIL: %v\n", err)
		} else if afterCount == 0 || isNotFound(err) {
			verified = true
		} else {
			fmt.Printf("    VERIFY FAIL: folder still has %d files\n", afterCount)
		}

		results = append(results, CaseResult{
			CaseName:    dc.Name,
			Files:       len(filePaths),
			Concurrency: len(filePaths),
			TotalSec:    totalDur.Seconds(),
			Stats:       calcStats(latencies),
			OK:          ok,
			Fail:        fail,
			Verified:    verified,
		})
	}

	return results
}

func doFolderListPaths(folderPath string) ([]string, error) {
	u := fmt.Sprintf("%s/files/folder?folderPath=%s&userId=%s",
		flagHost, url.QueryEscape(folderPath), url.QueryEscape(flagUserID))

	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var items []struct {
		FilePath string `json:"filePath"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("decode: %v", err)
	}

	var paths []string
	for _, item := range items {
		paths = append(paths, item.FilePath)
	}
	return paths, nil
}

func doDeleteFile(filePath string) error {
	u := fmt.Sprintf("%s/files?userId=%s&filePath=%s",
		flagHost, url.QueryEscape(flagUserID), url.QueryEscape(filePath))

	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Scenario 6: Delete Folder
// ---------------------------------------------------------------------------

type deleteFolderCase struct {
	Name       string
	FolderPath string
	Files      int
}

func scenarioDeleteFolder() []CaseResult {
	fmt.Println("=== Scenario: Delete Folder ===")

	cases := []deleteFolderCase{
		{"moved_folderA (small)", "/bench/small/moved_folderA", 250},
		{"moved_folderB (small)", "/bench/small/moved_folderB", 250},
		{"moved_folderA (medium)", "/bench/medium/moved_folderA", 50},
		{"moved_folderA (large)", "/bench/large/moved_folderA", 15},
		{"folderB (large)", "/bench/large/folderB", 15},
		{"xlarge_moved", "/bench/xlarge_moved", 10},
		{"huge", "/bench/huge", 6},
	}

	var results []CaseResult
	for _, dc := range cases {
		fmt.Printf("  [%s] deleting folder %s (~%d files)...\n", dc.Name, dc.FolderPath, dc.Files)

		t := time.Now()
		err := doDeleteFolder(dc.FolderPath)
		lat := time.Since(t)

		verified := false
		if err != nil {
			fmt.Printf("    FAIL: %v\n", err)
		} else {
			// Verify: folder list should return empty or 404
			count, err := doFolderList(dc.FolderPath)
			if err != nil && isNotFound(err) {
				verified = true
			} else if err != nil {
				fmt.Printf("    VERIFY FAIL: %v\n", err)
			} else if count == 0 {
				verified = true
			} else {
				fmt.Printf("    VERIFY FAIL: folder still has %d files\n", count)
			}

			// Verify: parent folder should not contain this folder's files
			if verified {
				parent := filepath.Dir(dc.FolderPath)
				parentCount, err := doFolderList(parent)
				if err != nil && !isNotFound(err) {
					fmt.Printf("    VERIFY WARN: parent check failed: %v\n", err)
				} else if parentCount > 0 {
					// This is okay — parent may have other children
					// We just log it for visibility
					fmt.Printf("    INFO: parent %s has %d remaining files\n", parent, parentCount)
				}
			}
		}

		results = append(results, CaseResult{
			CaseName: dc.Name,
			Files:    dc.Files,
			Latency:  float64(lat.Milliseconds()),
			Verified: verified,
			OK:       boolToInt(err == nil),
			Fail:     boolToInt(err != nil),
		})
	}

	return results
}

func doDeleteFolder(folderPath string) error {
	u := fmt.Sprintf("%s/files/folder?userId=%s&folderPath=%s",
		flagHost, url.QueryEscape(flagUserID), url.QueryEscape(folderPath))

	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Statistics
// ---------------------------------------------------------------------------

func calcStats(latencies []float64) LatencyStats {
	if len(latencies) == 0 {
		return LatencyStats{}
	}

	sort.Float64s(latencies)
	sum := 0.0
	for _, v := range latencies {
		sum += v
	}

	return LatencyStats{
		Min: latencies[0],
		Max: latencies[len(latencies)-1],
		Avg: sum / float64(len(latencies)),
		P50: percentile(latencies, 50),
		P95: percentile(latencies, 95),
		P99: percentile(latencies, 99),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := p / 100.0 * float64(len(sorted)-1)
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

// ---------------------------------------------------------------------------
// Output formatting
// ---------------------------------------------------------------------------

const (
	lineW = 120
	sep   = "═"
	thin  = "─"
)

func hline(char string) string {
	return strings.Repeat(char, lineW)
}

func printUploadDownloadTable(title string, results []CaseResult) {
	fmt.Println(hline(sep))
	fmt.Printf("  SCENARIO: %s\n", title)
	fmt.Println(hline(sep))
	fmt.Printf("  %-10s %5s %5s %9s %8s %8s %8s %8s %8s %8s %8s %5s %5s\n",
		"Case", "Files", "Conc", "Total(s)", "Avg(ms)", "P50(ms)", "P95(ms)", "P99(ms)", "Min(ms)", "Max(ms)", "MB/s", "OK", "Fail")
	fmt.Printf("  %s\n", hline(thin))
	for _, r := range results {
		fmt.Printf("  %-10s %5d %5d %9.2f %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f %5d %5d\n",
			r.CaseName, r.Files, r.Concurrency, r.TotalSec,
			r.Stats.Avg, r.Stats.P50, r.Stats.P95, r.Stats.P99,
			r.Stats.Min, r.Stats.Max, r.MBps, r.OK, r.Fail)
	}
	fmt.Println(hline(sep))
}

func printDownloadTable(title string, results []CaseResult) {
	fmt.Println(hline(sep))
	fmt.Printf("  SCENARIO: %s\n", title)
	fmt.Println(hline(sep))
	fmt.Printf("  %-10s %5s %5s %9s %8s %8s %8s %8s %8s %8s %8s %5s %5s %5s\n",
		"Case", "Files", "Conc", "Total(s)", "Avg(ms)", "P50(ms)", "P95(ms)", "P99(ms)", "Min(ms)", "Max(ms)", "MB/s", "OK", "Fail", "Hash✗")
	fmt.Printf("  %s\n", hline(thin))
	for _, r := range results {
		fmt.Printf("  %-10s %5d %5d %9.2f %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f %5d %5d %5d\n",
			r.CaseName, r.Files, r.Concurrency, r.TotalSec,
			r.Stats.Avg, r.Stats.P50, r.Stats.P95, r.Stats.P99,
			r.Stats.Min, r.Stats.Max, r.MBps, r.OK, r.Fail, r.HashFail)
	}
	fmt.Println(hline(sep))
}

func printFolderListTable(title string, results []CaseResult) {
	fmt.Println(hline(sep))
	fmt.Printf("  SCENARIO: %s\n", title)
	fmt.Println(hline(sep))
	fmt.Printf("  %-15s %5s %9s %8s %8s %8s %8s %8s %8s %5s %5s %8s\n",
		"Case", "Reqs", "Total(s)", "Avg(ms)", "P50(ms)", "P95(ms)", "P99(ms)", "Min(ms)", "Max(ms)", "OK", "Fail", "Verified")
	fmt.Printf("  %s\n", hline(thin))
	for _, r := range results {
		v := "✗"
		if r.Verified {
			v = "✓"
		}
		fmt.Printf("  %-15s %5d %9.2f %8.1f %8.1f %8.1f %8.1f %8.1f %8.1f %5d %5d %8s\n",
			r.CaseName, r.Files, r.TotalSec,
			r.Stats.Avg, r.Stats.P50, r.Stats.P95, r.Stats.P99,
			r.Stats.Min, r.Stats.Max, r.OK, r.Fail, v)
	}
	fmt.Println(hline(sep))
}

func printMoveDeleteTable(title string, results []CaseResult) {
	fmt.Println(hline(sep))
	fmt.Printf("  SCENARIO: %s\n", title)
	fmt.Println(hline(sep))

	hasStats := false
	for _, r := range results {
		if r.TotalSec > 0 {
			hasStats = true
			break
		}
	}

	if hasStats {
		// Delete Files style: has concurrency and stats
		fmt.Printf("  %-22s %5s %5s %9s %8s %8s %8s %8s %5s %5s %8s\n",
			"Case", "Files", "Conc", "Total(s)", "Avg(ms)", "P50(ms)", "P95(ms)", "P99(ms)", "OK", "Fail", "Verified")
		fmt.Printf("  %s\n", hline(thin))
		for _, r := range results {
			v := "✗"
			if r.Verified {
				v = "✓"
			}
			fmt.Printf("  %-22s %5d %5d %9.2f %8.1f %8.1f %8.1f %8.1f %5d %5d %8s\n",
				r.CaseName, r.Files, r.Concurrency, r.TotalSec,
				r.Stats.Avg, r.Stats.P50, r.Stats.P95, r.Stats.P99,
				r.OK, r.Fail, v)
		}
	} else {
		// Move / Delete Folder style: single latency
		fmt.Printf("  %-28s %5s %12s %8s\n", "Case", "Files", "Latency(ms)", "Verified")
		fmt.Printf("  %s\n", hline(thin))
		for _, r := range results {
			v := "✗"
			if r.Verified {
				v = "✓"
			}
			fmt.Printf("  %-28s %5d %12.1f %8s\n",
				r.CaseName, r.Files, r.Latency, v)
		}
	}
	fmt.Println(hline(sep))
}

func printSummary(upload, download, folderList, move, deleteFile, deleteFolder []CaseResult) {
	fmt.Println(hline(sep))
	fmt.Printf("  SUMMARY\n")
	fmt.Println(hline(sep))
	fmt.Printf("  %-15s %12s %12s %12s %15s\n",
		"Scenario", "Total(s)", "Total Files", "Success %", "Avg MB/s")
	fmt.Printf("  %s\n", hline(thin))

	scenarios := []struct {
		name    string
		results []CaseResult
	}{
		{"Upload", upload},
		{"Download", download},
		{"Folder List", folderList},
		{"Move Folder", move},
		{"Delete Files", deleteFile},
		{"Delete Folder", deleteFolder},
	}

	for _, s := range scenarios {
		totalTime := 0.0
		totalFiles := 0
		totalOK := 0
		totalFail := 0
		totalMBps := 0.0
		mbpsCount := 0

		for _, r := range s.results {
			if r.TotalSec > 0 {
				totalTime += r.TotalSec
			} else {
				totalTime += r.Latency / 1000.0
			}
			totalFiles += r.Files
			totalOK += r.OK
			totalFail += r.Fail
			if r.MBps > 0 {
				totalMBps += r.MBps
				mbpsCount++
			}
		}

		successRate := 0.0
		if totalOK+totalFail > 0 {
			successRate = float64(totalOK) / float64(totalOK+totalFail) * 100
		}

		avgMBps := "—"
		if mbpsCount > 0 {
			avgMBps = fmt.Sprintf("%.1f", totalMBps/float64(mbpsCount))
		}

		fmt.Printf("  %-15s %12.2f %12d %11.1f%% %15s\n",
			s.name, totalTime, totalFiles, successRate, avgMBps)
	}
	fmt.Println(hline(sep))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 404")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
