#!/bin/bash
# 一键停止当前lark-ws、下载最新构建产物并启动

DIR="$(cd "$(dirname "$0")"; pwd)"

# 凭据由 start.sh 统一从环境变量 / .env 加载，本脚本不再处理

echo "1. 停止正在运行的 lark-ws 进程..."
bash "$DIR/stop_lark_ws.sh"

echo
echo "2. 下载最新版本的 lark-ws 二进制..."
bash "$DIR/download_latest_artifact.sh"

# 找到最新解压的二进制包名称
GOOS=$(uname | tr '[:upper:]' '[:lower:]')
if [ "$GOOS" = "darwin" ]; then GOOS="darwin"; fi
if [ "$GOOS" = "linux" ]; then GOOS="linux"; fi

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) GOARCH="amd64";;
  arm64|aarch64) GOARCH="arm64";;
  *) echo "不支持的体系结构: $ARCH"; exit 1;;
esac

FILENAME="lark-ws-${GOOS}-${GOARCH}"

echo
echo "3. 解压最新 lark-ws..."
unzip -o "$DIR/${FILENAME}.zip" -d "$DIR"
rm -f "$DIR/${FILENAME}.zip"

# 统一二进制名为 lark-ws，与 start.sh / stop_lark_ws.sh 保持一致
BIN="$DIR/lark-ws"
mv -f "$DIR/$FILENAME" "$BIN"
chmod +x "$BIN"

echo
echo "4. 启动（环境变量由 start.sh 校验）..."
exec bash "$DIR/start.sh"
