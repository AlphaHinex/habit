#!/bin/bash
# 下载 habit 仓库最新编译产物，根据当前系统自动选择

OWNER="AlphaHinex"
REPO="habit"
ARTIFACT_PREFIX="lark-ws"

# 获取 GOOS 和 GOARCH
GOOS=$(uname | tr '[:upper:]' '[:lower:]')
if [ "$GOOS" = "darwin" ]; then GOOS="darwin"; fi
if [ "$GOOS" = "linux" ]; then GOOS="linux"; fi

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) GOARCH="amd64";;
  arm64|aarch64) GOARCH="arm64";;
  *) echo "不支持的体系结构: $ARCH"; exit 1;;
esac

FILENAME="${ARTIFACT_PREFIX}-${GOOS}-${GOARCH}"

# 检查 gh CLI 是否安装
if ! command -v gh &> /dev/null; then
  echo "请先安装 GitHub CLI (gh): https://cli.github.com/"
  exit 1
fi

# 获取 artifact 列表
ARTIFACT_ID=$(gh api \
  repos/$OWNER/$REPO/actions/artifacts \
  --jq ".artifacts[] | select(.name==\"${FILENAME}\") | .id" | head -n1)

if [ -z "$ARTIFACT_ID" ]; then
  echo "找不到对应的 artifact: $FILENAME"
  exit 1
fi

# 下载 artifact（为 zip 文件）
DOWNLOAD_URL="https://api.github.com/repos/$OWNER/$REPO/actions/artifacts/$ARTIFACT_ID/zip"
curl -L -H "Authorization: token $(gh auth token)" \
     -H "Accept: application/vnd.github+json" \
     "$DOWNLOAD_URL" -o "${FILENAME}.zip"

echo "下载完成: ${FILENAME}.zip"
