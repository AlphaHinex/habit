#!/bin/bash
# 一键停止当前lark-ws、下载最新构建产物并启动

DIR="$(cd "$(dirname "$0")"; pwd)"

# 加载 .env （若存在）
if [ -f "$DIR/.env" ]; then
  set -a
  . "$DIR/.env"
  set +a
fi

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
echo "3. 解压并启动最新 lark-ws..."
unzip -o "$DIR/${FILENAME}.zip" -d "$DIR"

chmod +x "$DIR/$FILENAME"

# 后台启动，输出到 log 文件
nohup "$DIR/$FILENAME" > "$DIR/${FILENAME}.log" 2>&1 &

echo "启动完成：$DIR/$FILENAME，日志输出至 $DIR/${FILENAME}.log"
