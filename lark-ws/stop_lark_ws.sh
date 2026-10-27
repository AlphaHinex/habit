#!/bin/bash
# 停止正在运行的 lark-ws。
#
# 用 pgrep -x 精确匹配「进程名」，不要用 -f 匹配命令行：
# 脚本自身路径里就含 lark-ws，按命令行匹配会把调用它的 cron shell
# 和 update_and_run_lark_ws.sh 一起杀掉，表现为主流程执行到第 1 步就中断。

set -u

PATTERN="${LARK_WS_PATTERN:-lark-ws}"

PIDS=$(pgrep -x "$PATTERN" 2>/dev/null || true)

if [ -z "$PIDS" ]; then
  echo "未发现正在运行的 lark-ws 进程"
  exit 0
fi

echo "正在终止如下 lark-ws 进程: $(echo $PIDS)"
kill $PIDS 2>/dev/null || true

# 等进程真正退出并释放 flock，否则新实例会撞上单实例锁
for _ in $(seq 1 20); do
  pgrep -x "$PATTERN" > /dev/null 2>&1 || break
  sleep 0.5
done

if pgrep -x "$PATTERN" > /dev/null 2>&1; then
  echo "进程未正常退出，强制终止"
  pkill -9 -x "$PATTERN" 2>/dev/null || true
  sleep 1
fi

echo "lark-ws 相关进程已全部终止"
