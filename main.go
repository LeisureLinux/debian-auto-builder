package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Configuration
const (
	DebianPackagesURL = "https://packages.debian.org/stable/amd64/"
	GitHubAPIBase     = "https://api.github.com/repos"
	AptRepoURL        = "http://repo.freelamp.com/unbound-dashboard" // 示例 URL
)

// PackageInfo holds information about a Debian package
type PackageInfo struct {
	Name       string `json:"Package"`
	Version    string `json:"Version"`
	Homepage   string `json:"Homepage,omitempty"`
	BinaryArch []struct {
		Arch     string `json:"arch"`
		Size     string `json:"size"`
		Filename string `json:"filename"`
	} `json:"Binary-Architecture"`
}

// GitHubRelease represents a GitHub release
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Asset represents an asset in a GitHub release
type Asset struct {
	Name       string `json:"name"`
	BrowserURL string `json:"browser_download_url"`
	URL        string `json:"url"`
}

// TrackedPackage is the main structure for tracking packages
type TrackedPackage struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	Package    string `json:"package"`
	VersionTag string `json:"version_tag,omitempty"` // optional version override
}

// ScanResult represents the result of a scan operation
type ScanResult struct {
	Package        string         `json:"package"`
	DebianInfo     interface{}    `json:"debian_info"`
	GitHubInfo     *GitHubRelease `json:"github_info"`
	InDebian       bool           `json:"in_debian"`
	HasDebAsset    bool           `json:"has_deb_asset"`
	Amd64Available bool           `json:"amd64_available"`
	Arm64Available bool           `json:"arm64_available"`
	GapDetected    bool           `json:"gap_detected"`
	Action         string         `json:"action"` // "skip", "rehost", or "build"
}

func main() {
	// One-shot CLI mode: ./debian-auto-builder -scan [tracked.json]
	// Used by GitHub Actions so no server is needed.
	if len(os.Args) > 1 && os.Args[1] == "-scan" {
		path := "tracked.json"
		if len(os.Args) > 2 {
			path = os.Args[2]
		}
		pkgs, err := loadTrackedPackages(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to load %s: %v\n", path, err)
			os.Exit(1)
		}
		results, err := runScan(pkgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Scan failed: %v\n", err)
			os.Exit(1)
		}
		out, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(out))

		gaps := 0
		for _, r := range results {
			if r.GapDetected {
				gaps++
			}
		}
		if gaps > 0 {
			os.Exit(1) // signal gaps to CI via exit code
		}
		return
	}

	http.HandleFunc("/scan", handleScan)
	http.HandleFunc("/auto-scan", handleAutoScan)
	http.HandleFunc("/health", handleHealth)
	fmt.Println("Server starting on :8080")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "ok"}`))
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var trackedPackages []TrackedPackage
	if err := json.NewDecoder(r.Body).Decode(&trackedPackages); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	results, _ := runScan(trackedPackages)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func handleAutoScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// If a body was provided, use it; otherwise fall back to tracked.json.
	var trackedPackages []TrackedPackage
	body, _ := io.ReadAll(r.Body)
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &trackedPackages); err != nil {
			http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
			return
		}
	}
	if len(trackedPackages) == 0 {
		loaded, err := loadTrackedPackages("tracked.json")
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to load tracked.json: %v", err), http.StatusInternalServerError)
			return
		}
		trackedPackages = loaded
	}

	results, _ := runScan(trackedPackages)

	// Non-JSON summary header for quick CLI/curl use (keeps JSON body clean).
	builds := 0
	for _, r := range results {
		if r.GapDetected {
			builds++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Scan-Count", fmt.Sprintf("%d", len(trackedPackages)))
	w.Header().Set("X-Gap-Count", fmt.Sprintf("%d", builds))
	json.NewEncoder(w).Encode(results)
}

func loadTrackedPackages(path string) ([]TrackedPackage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pkgs []TrackedPackage
	if err := json.Unmarshal(data, &pkgs); err != nil {
		return nil, err
	}
	return pkgs, nil
}

func ScanPackage(pkg TrackedPackage) ScanResult {
	result := ScanResult{
		Package:    pkg.Package,
		InDebian:   false,
		GitHubInfo: fetchGitHubReleases(pkg.Owner, pkg.Repo),
	}

	if result.GitHubInfo != nil && len(result.GitHubInfo.Assets) > 0 {
		result.HasDebAsset = hasDebAsset(result.GitHubInfo.Assets)

		for _, asset := range result.GitHubInfo.Assets {
			if strings.Contains(asset.Name, "amd64.deb") ||
				strings.Contains(asset.Name, "_amd64.deb") {
				result.Amd64Available = true
			}
			if strings.Contains(asset.Name, "arm64.deb") ||
				strings.Contains(asset.Name, "_arm64.deb") {
				result.Arm64Available = true
			}
		}
	}

	// 增量构建：若 apt-repo 已有「最新上游版本」的构建产物，直接视为已覆盖。
	// 没有这条规则时，上游不发双架构 deb 资产的包会被每天重复派发重建。
	if aptRepoHasCurrentVersion(pkg.Package, latestVersion(result.GitHubInfo)) {
		result.Action = "skip"
		return result
	}

	determineAction(&result)
	return result
}

// runScan scans all packages, collects gap packages, and triggers ONE batch build.
func runScan(pkgs []TrackedPackage) ([]ScanResult, error) {
	results := make([]ScanResult, len(pkgs))
	var gaps []TrackedPackage
	for i, pkg := range pkgs {
		result := ScanPackage(pkg)
		results[i] = result

		switch {
		case result.GapDetected:
			fmt.Printf("⚠️ Package %s: gap detected\n", pkg.Package)
			gaps = append(gaps, pkg)
		case result.InDebian:
			fmt.Printf("ℹ️ Package %s: Already in Debian (skip)\n", pkg.Package)
		case result.Action == "rehost":
			fmt.Printf("✅ Package %s: GitHub has all arches, rehost only\n", pkg.Package)
		default:
			fmt.Printf("ℹ️ Package %s: %s\n", pkg.Package, result.Action)
		}
	}
	// 批量派发：所有缺口合并为一次 repository_dispatch，deb-builder 端顺序构建、
	// 统一推送一次，避免 N 个并行运行互相抢推 apt-repo 导致非快进冲突。
	if len(gaps) > 0 {
		triggerBuildForPackages(gaps)
	}
	return results, nil
}

func determineAction(result *ScanResult) {
	if result.InDebian {
		result.Action = "skip"
		return
	}

	if !result.HasDebAsset {
		// GitHub releases don't ship .deb at all -> build from source
		result.GapDetected = true
		result.Action = "build"
		return
	}

	if result.Amd64Available && result.Arm64Available {
		// GitHub already ships .deb for both arches -> just rehost
		result.Action = "rehost"
		return
	}

	// Has .deb but missing at least one architecture -> build the missing ones
	result.GapDetected = true
	result.Action = "build"
}

func fetchGitHubReleases(owner, repo string) *GitHubRelease {
	url := fmt.Sprintf("%s/%s/%s/releases", GitHubAPIBase, owner, repo)

	resp, err := http.Get(url + "?per_page=10") // 获取最近的 10 个 release
	if err != nil {
		fmt.Printf("❌ Error fetching GitHub releases: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var releases []GitHubRelease
	json.Unmarshal(body, &releases)

	if len(releases) == 0 {
		return nil
	}

	return &releases[0]
}

func hasDebAsset(assets []Asset) bool {
	for _, asset := range assets {
		nameStr := strings.ToLower(asset.Name)
		if strings.HasSuffix(nameStr, ".deb") &&
			(strings.Contains(nameStr, "amd64") ||
				strings.Contains(nameStr, "arm64")) {
			return true
		}
	}
	return false
}

// BuildRepoOwner / BuildRepoName are the target repo that receives dispatch
// events and actually builds the .deb packages. Overridable via env.
const (
	BuildRepoOwner = "LeisureLinux"
	BuildRepoName  = "deb-builder"
	BuildEventType = "auto-build-required"
)

func triggerBuildForPackage(pkg TrackedPackage) {
	triggerBuildForPackages([]TrackedPackage{pkg})
}

// triggerBuildForPackages sends a single repository_dispatch carrying the full
// gap list (client_payload.packages = comma-separated names). deb-builder's
// receive-trigger builds them sequentially in one runner and pushes once.
// latestVersion 从上游 release 提取版本号（去掉 tag 的 v 前缀）
func latestVersion(r *GitHubRelease) string {
	if r == nil || r.TagName == "" {
		return ""
	}
	return strings.TrimPrefix(r.TagName, "v")
}

var (
	aptRepoDebNames map[string]bool
	aptRepoLoaded   bool
)

// loadAptRepoDebNames 拉取 apt-repo 仓库的 .deb 文件名清单（每次扫描只拉一次）。
// 获取失败时保持空清单，所有包按「未构建」处理，宁可多建不可漏建。
func loadAptRepoDebNames() {
	if aptRepoLoaded {
		return
	}
	aptRepoLoaded = true
	aptRepoDebNames = map[string]bool{}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("BUILDER_PAT")
	}
	url := GitHubAPIBase + "/repos/LeisureLinux/apt-repo/git/trees/main?recursive=1"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if token != "" {
		req.SetBasicAuth("x-access-token", token)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("⚠️ 无法获取 apt-repo 文件清单（按未构建处理）: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Printf("⚠️ apt-repo 清单请求失败 (%d)，按未构建处理\n", resp.StatusCode)
		return
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return
	}
	for _, t := range tree.Tree {
		name := t.Path
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if strings.HasSuffix(name, ".deb") {
			aptRepoDebNames[name] = true
		}
	}
	fmt.Printf("📦 apt-repo 现有 %d 个 .deb 文件\n", len(aptRepoDebNames))
}

// aptRepoHasCurrentVersion 判断 apt-repo 是否已有 pkg 的 version 版本构建产物。
// 匹配 <pkg>_<version>*.deb（允许 +LL 等 repack 后缀），任一架构命中即可。
func aptRepoHasCurrentVersion(pkg, version string) bool {
	if version == "" {
		return false
	}
	loadAptRepoDebNames()
	prefix := pkg + "_" + version
	for name := range aptRepoDebNames {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if len(rest) > 0 && (rest[0] == '_' || rest[0] == '+') {
			return true
		}
	}
	return false
}

func triggerBuildForPackages(pkgs []TrackedPackage) {
	if len(pkgs) == 0 {
		return
	}
	owner := getenv("BUILD_REPO_OWNER", BuildRepoOwner)
	repo := getenv("BUILD_REPO_NAME", BuildRepoName)

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("BUILDER_PAT")
	}
	if token == "" {
		fmt.Println("⚠️ No GITHUB_TOKEN/BUILDER_PAT set; skipping dispatch")
		return
	}

	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		names = append(names, p.Package)
	}

	url := fmt.Sprintf("%s/%s/%s/dispatches", GitHubAPIBase, owner, repo)
	payload := map[string]interface{}{
		"event_type": BuildEventType,
		"client_payload": map[string]string{
			"packages": strings.Join(names, ","), // 新字段：批量包名列表
			"package":  names[0],                  // 向后兼容：保留单包字段
			"source":   "debian-auto-builder",
			"arch":     "amd64,arm64",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("❌ Failed to encode dispatch payload: %v\n", err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Printf("❌ Failed to create dispatch request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.SetBasicAuth("x-access-token", token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Dispatch request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Printf("✅ Dispatched batch build for %d package(s): %s (event=%s)\n", len(names), strings.Join(names, ", "), BuildEventType)
	} else {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Printf("❌ Dispatch failed (%d): %s\n", resp.StatusCode, string(respBody))
	}
}

// dispatchWorkflow sends a GitHub repository_dispatch event to the build repo
// so its CI pipeline can build the package from source. Requires GITHUB_TOKEN
// (or BUILDER_PAT) to be set; otherwise it logs a warning and skips.
func dispatchWorkflow(pkg TrackedPackage) {
	owner := getenv("BUILD_REPO_OWNER", BuildRepoOwner)
	repo := getenv("BUILD_REPO_NAME", BuildRepoName)

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("BUILDER_PAT")
	}
	if token == "" {
		fmt.Printf("⚠️ No GITHUB_TOKEN/BUILDER_PAT set; skipping dispatch for %s\n", pkg.Package)
		return
	}

	url := fmt.Sprintf("%s/%s/%s/dispatches", GitHubAPIBase, owner, repo)

	payload := map[string]interface{}{
		"event_type": BuildEventType,
		"client_payload": map[string]string{
			"package": pkg.Package,
			"owner":   pkg.Owner,
			"repo":    pkg.Repo,
			"version": pkg.VersionTag,
			"source":  "debian-auto-builder",
			"arch":    "amd64,arm64",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("❌ Failed to encode dispatch payload: %v\n", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		fmt.Printf("❌ Failed to create dispatch request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("❌ Dispatch to %s/%s failed: %v\n", owner, repo, err)
		return
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		fmt.Printf("✅ Dispatched build for %s to %s/%s (event=%s)\n", pkg.Package, owner, repo, BuildEventType)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		fmt.Printf("❌ Dispatch to %s/%s rejected (%d): token lacks permission\n", owner, repo, resp.StatusCode)
	default:
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Printf("⚠️ Dispatch to %s/%s returned %d: %s\n", owner, repo, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

// getenv returns the value of env var key, or def if unset/empty.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
