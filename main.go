package main

import (
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
	http.HandleFunc("/scan", handleScan)
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

	results := make([]ScanResult, len(trackedPackages))

	for i, pkg := range trackedPackages {
		result := ScanPackage(pkg)
		results[i] = result

		if result.GapDetected {
			fmt.Printf("⚠️ Package %s: gap detected, triggering build\n", pkg.Package)
			triggerBuildForPackage(pkg)
		} else if result.InDebian {
			fmt.Printf("ℹ️ Package %s: Already in Debian (skip)\n", pkg.Package)
		} else if result.Action == "rehost" {
			fmt.Printf("✅ Package %s: GitHub has all arches, rehost only\n", pkg.Package)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
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

	determineAction(&result)
	return result
}

// determineAction decides skip/rehost/build based on detected gaps.
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

func triggerBuildForPackage(pkg TrackedPackage) {
	// 这里可以:
	// 1. POST to apt-repo workflow API
	// 2. Create a GitHub issue/PR with build recipe
	// 3. Send webhook notification

	fmt.Printf("🚀 Triggering build for %s/%s\n", pkg.Owner, pkg.Repo)

	// Example: dispatch GitHub Actions workflow
	dispatchWorkflow(pkg)
}

func dispatchWorkflow(pkg TrackedPackage) {
	// 这里可以触发 apt-repo 或 ghdeb 的 GitHub Actions workflow
	// 需要 GITHUB_TOKEN 作为环境变量

	fmt.Printf("Would trigger CI for %s... (requires GITHUB_TOKEN)\n", pkg.Package)
}
