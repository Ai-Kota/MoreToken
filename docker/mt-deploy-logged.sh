#!/usr/bin/env bash
# mt-deploy-logged.sh —— 计划任务入口（T-028）。
# 由 run-mt-deploy-hidden.vbs 经 wscript 隐藏宿主调用（无闪窗，对齐 dev-fleet 防弹黑窗纪律）。
# 与直接跑 mt-deploy.sh 的唯一区别：时间戳 + 全量落 docker/runtime/deploy.log + 日志自截断。
set -u

# 计划任务环境加固（T-028 实测教训）：Task Scheduler 下 BASH_SOURCE 是 Windows
# 反斜杠路径，dirname 不认反斜杠分隔 → cd 到错误目录 → 静默 exit 1（上次结果=1）。
# 对策：硬编码仓库根（两种路径形态都试）+ 显式补 MSYS 工具 PATH（调度器环境没有
# /usr/bin，sed/grep/seq/date 全在那）。不依赖任何继承环境。
cd /e/AImlyForge/tools/agent/moretoken 2>/dev/null \
  || cd "E:/AImlyForge/tools/agent/moretoken" 2>/dev/null \
  || exit 1
export PATH="/usr/bin:/bin:$PATH"

mkdir -p docker/runtime
LOG="docker/runtime/deploy.log"

# 日志自截断：>1MB 时保留后半 500KB（每日一条记录，年增也就几百 KB，但防线要在）。
if [ -f "$LOG" ]; then
  SIZE=$(stat -c%s "$LOG" 2>/dev/null || echo 0)
  if [ "$SIZE" -gt 1000000 ]; then
    tail -c 500000 "$LOG" > "$LOG.tmp" && mv -f "$LOG.tmp" "$LOG"
  fi
fi

{
  echo "===== $(date '+%F %T') mt-deploy (scheduled) ====="
  bash docker/mt-deploy.sh
  echo "===== exit=$? ====="
} >> "$LOG" 2>&1
