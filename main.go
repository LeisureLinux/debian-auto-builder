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

	results := make([]ScanResult, len(trackedPackages))
	builds := 0
	for i, pkg := range trackedPackages {
		result := ScanPackage(pkg)
		results[i] = result
		if result.GapDetected {
			builds++
			fmt.Printf("⚠️ Package %s: gap detected, triggering build\n", pkg.Package)
			triggerBuildForPackage(pkg)
		} else if result.InDebian {
			fmt.Printf("ℹ️ Package %s: Already in Debian (skip)\n", pkg.Package)
		} else if result.Action == "rehost" {
			fmt.Printf("✅ Package %s: GitHub has all arches, rehost only\n", pkg.Package)
		}
	}

	// Non-JSON summary header for quick CLI/curl use (keeps JSON body clean).
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

// BuildRepoOwner / BuildRepoName are the target repo that receives dispatch
// events and actually builds the .deb packages. Overridable via env.
const (
	BuildRepoOwner = "LeisureLinux"
	BuildRepoName  = "deb-builder"
	BuildEventType = "auto-build-required"
)

func triggerBuildForPackage(pkg TrackedPackage) {
	fmt.Printf("🚀 Triggering build for %s/%s\n", pkg.Owner, pkg.Repo)
	dispatchWorkflow(pkg)
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
