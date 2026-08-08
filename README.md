# Debian Auto Builder API Server

一个自动检测 Debian 软件包和 GitHub Releases 缺失架构，并触发构建的 API Server。

## 🎯 功能特性

- ✅ 扫描 tracked packages（从 tracked.json 加载）
- ✅ 查询 each package 是否是官方 Debian 包（通过 homepage → Github）
- ✅ 检查 GitHub Releases 中是否已经有对应架构的.deb资产
- ✅ 识别缺口: `Debian missing` or `GitHub missing amd64/arm64`
- ✅ 触发构建流程: dispatch to apt-repo/workflow recipes

## 🏗️ API设计

### GET /health
返回服务健康状态。

```bash
curl http://localhost:8080/health
# Response: {"status": "ok"}
```

### POST /scan
批量扫描 tracked packages，检测缺口并触发构建。

**Request Body:**
```json
[
  {
    "owner": "dundee",
    "repo": "gdu", 
    "package": "gdu"
  }
]
```

**Response:**
```json
[
  {
    "package": "gdu",
    "debian_info": null,
    "github_info": {...},
    "in_debian": false,
    "has_deb_asset": true,
    "amd64_available": true,
    "arm64_available": true,
    "gap_detected": false,
    "action": "skip"
  }
]
```

## 📦 安装和运行

### 1. 依赖
```bash
go mod tidy
# 会自动下载 golang.org/x/mod/semver
```

### 2. 启动服务
```bash
./debian-auto-builder
```

### 3. 测试扫描
```bash
curl -X POST http://localhost:8080/scan \
  -H "Content-Type: application/json" \
  -d '[{"owner":"dundee","repo":"gdu","package":"gdu"}]'
```

## 🔗 工作流集成

### GitHub Actions Triggering

当检测到缺口时，API 可以触发其他仓库的 CI：

```bash
# 发送到 apt-repo 的 repository_dispatch
curl -X POST https://api.github.com/repos/LeisureLinux/apt-repo/dispatches \
  -H "Accept: application/vnd.github.v3+json" \
  -d '{"event_type":"auto-build-detected","client_payload":{"package":"gdu"}}' \
```

需要配置 GITHUB_TOKEN 环境变量。

### Webhook to Trigger Debian Package Build

检测到缺口后，可以：
1. 自动创建 build recipe (YAML) 到 `deb-builder/recipes/`
2. Dispatch GitHub Actions workflow to deb-builder
3. Build the package and push to apt-repo

## 🚀 下一步计划

- [ ] 添加 Debian Packages.xz 本地缓存和快速查询（离线扫描）
- [ ] 支持批量从 tracked.json 读取 packages
- [ ] Web UI for monitoring gaps and build status  
- [ ] Slack/Discord webhook notifications
- [ ] Rate limit protection to avoid GitHub API throttling

