#!/bin/bash
set -euo pipefail

echo "🚀 Starting Debian Auto Builder API Server..."

# 检查是否有 Go mod cache 问题，如果没有 vendor 就先清理
if [[ ! -f go.sum ]]; then
    echo "⏳ Running go mod tidy (this may take a moment)..."
    export GOPROXY=https://goproxy.cn,direct
    go mod tidy || {
        echo "❌ Failed to run go mod tidy"
        exit 1
    }
fi

# 启动服务
echo "✅ Server ready on port 8080"
echo ""
echo "API Endpoints:"
echo "  GET /health     - Health check"
echo "  POST /scan      - Scan tracked packages"
echo "  POST /auto-scan - Auto scan Debian + GitHub (full pipeline)"
echo ""

exec go run main.go "$@"
