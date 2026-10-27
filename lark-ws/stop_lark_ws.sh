#!/bin/bash
# 停止所有正在运行的 lark-ws 进程。
# 同时兼容 "./lark-ws" 与 "lark-ws-<os>-<arch>" 两种二进制名，
# 避免旧版本因名字带横杠而杀不掉、导致多实例并发写同一个仓库。

PATTERN="${LARK_WS_PATTERN:-lark-ws}"
PIDS=$(pgrep -f "$PATTERN" | grep -v -x "$$" || true)

if [ -z "$PIDS" ]; then
  echo "未发现正在运行的 lark-ws 进程"
  exit 0
fi

echo "正在终止如下 lark-ws 进程: $PIDS"
kill $PIDS

# 等待进程释放 flock 与未完成的 GitHub 写入
sleep 2
if pgrep -f "$PATTERN" > /dev/null; then
  echo "有进程未能正常终止，尝试强制终止"
  pkill -9 -f "$PATTERN"
fi

echo "lark-ws 相关进程已全部终止"
