#!/bin/bash
# 启动 lark-ws。
#
# 凭据全部来自环境变量，本文件不保存任何密钥。两种提供方式：
#   1) export APP_ID=... APP_SECRET=... GITHUB_TOKEN=...  然后 ./start.sh
#   2) cp .env.example .env && chmod 600 .env            填好后 ./start.sh
#
# 注意：务必在脚本所在目录启动，因为 pid/state 文件与 flock 单实例锁都在这个目录，
# 从不同目录启动会让多个实例各自持有一把锁。

set -u

DIR="$(cd "$(dirname "$0")"; pwd)"
cd "$DIR" || exit 1

# 可选：从 .env 加载（文件中的值会覆盖同名环境变量）
if [ -f "$DIR/.env" ]; then
  set -a
  . "$DIR/.env"
  set +a
fi

missing=""
for name in APP_ID APP_SECRET GITHUB_TOKEN; do
  if [ -z "${!name:-}" ]; then
    missing="$missing $name"
  fi
done

if [ -n "$missing" ]; then
  echo "缺少环境变量:$missing" >&2
  echo "请通过 export 或 $DIR/.env 提供（可参考 $DIR/.env.example）。" >&2
  exit 1
fi

nohup "$DIR/lark-ws" > "$DIR/lark-ws.log" 2>&1 &
echo "lark-ws started: pid $!, log $DIR/lark-ws.log"
