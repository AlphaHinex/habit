#!/bin/bash
# 自动停止当前系统中所有运行中的 lark-ws- 进程

PIDS=$(pgrep -f "lark-ws-")
if [ -z "$PIDS" ]; then
  echo "未发现正在运行的 lark-ws- 进程"
  exit 0
fi

echo "正在终止如下 lark-ws- 进程: $PIDS"
kill $PIDS

# 可选：强杀残留进程
sleep 2
if pgrep -f "lark-ws-" > /dev/null; then
  echo "有进程未能正常终止，尝试强制终止"
  pkill -9 -f "lark-ws-"
fi

echo "lark-ws- 相关进程已全部终止"
