#!/usr/bin/env bash
# mt-admission-ready.sh —— 准入选表的"能不能接"判据（只读，不改任何东西）。
#
# 为什么要有它（2026-10-01）：接入前的最后一道闸应当是**可复现的判据**，不是"看着差不多"。
# 实测教训：严格准入下编程池曾经是 **空的**（最好的模型只有 4/5）—— 那种表直接接上去，
# `auto:coding` 会一个候选都没有、当场报错，**比不接更糟**。
# 本脚本把"池子够不够填"变成一条命令能答的问题。
#
# 用法：
#   bash docker/mt-admission-ready.sh [<admitted.json 路径>]
#   默认路径：docker/runtime/admitted.json
#
# 退出码：0 = 两池都非空、可以接；1 = 不建议接（并说明缺什么）。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
TABLE="${1:-$HERE/runtime/admitted.json}"
CONFIG="$ROOT/config/config.json"

[ -f "$TABLE" ] || { echo "✗ 表不存在: $TABLE"; exit 1; }
[ -f "$CONFIG" ] || { echo "✗ 配置不存在: $CONFIG"; exit 1; }

# 强制 UTF-8：本机控制台是 GBK，而 ✓/✗ 不在 GBK 字符集里 ——
# 不设它会在**最后一行输出**处抛 UnicodeEncodeError，脚本明明判对了却以崩溃收场（实测踩过）。
export PYTHONIOENCODING=utf-8

python - "$TABLE" "$CONFIG" <<'PYEOF'
import json, io, sys

table_path, cfg_path = sys.argv[1], sys.argv[2]
rows = json.load(io.open(table_path, encoding='utf-8'))
cfg = json.load(io.open(cfg_path, encoding='utf-8'))
ratio = cfg.get('admission_min_ratio') or 1.0
if not (0 < ratio <= 1):
    ratio = 1.0

def admits(x, dim):
    """与 moretoken admission.admits 同口径：①整行覆盖完整 ②该维度通过率达标。"""
    if not x.get('anthropic_ok'):
        return False
    if x.get('battery_verdict') not in ('PASS', 'FAIL', 'UNCERTAIN'):
        return False
    d = (x.get('dims') or {}).get(dim) or {}
    if d.get('total', 0) <= 0:
        return False
    return d.get('passed', 0) + 1e-9 >= ratio * d['total']

total = len(rows)
covered = sum(1 for x in rows
              if x.get('anthropic_ok') and x.get('battery_verdict') in ('PASS', 'FAIL', 'UNCERTAIN'))
R = [x['model'] for x in rows if admits(x, 'reasoning')]
C = [x['model'] for x in rows if admits(x, 'coding')]

print('  表:     %s' % table_path)
print('  准入线: %.0f%%' % (ratio * 100))
print('  条目:   %d 条，其中覆盖完整(可判)的 %d 条' % (total, covered))
print('  推理池: %d 个' % len(R))
print('  编程池: %d 个' % len(C))

# 健全性闸（**与网关 internal/admission.reload 同口径**）：
# 有内容却一条维度样本都认不出 ⇒ 疑为跨仓契约漂移。此时网关会判"表不可用"并回退 kinds，
# 所以这里也必须先报"漂移"，而不是笼统说"池空" —— 否则诊断指向错的地方，人会去查配额。
recognizable = sum(1 for x in rows if any((d or {}).get('total', 0) > 0 for d in (x.get('dims') or {}).values()))
if total > 0 and recognizable == 0:
    print()
    print('✗ 不建议接：表有 %d 条，但**无一条含可识别的维度样本** —— 疑为格式/契约漂移' % total)
    print('  （字段名被改过？对照 internal/admission 的 ModelAdmission 结构）')
    print('  网关遇到这种表会判"不可用"并回退 kinds，所以接上去也等于没接。')
    sys.exit(1)
print()
for name, pool in (('推理', R), ('编程', C)):
    if pool:
        print('  %s池成员: %s' % (name, ', '.join(pool[:8]) + (' …' if len(pool) > 8 else '')))

# 覆盖率过低时不该接：表只覆盖了一小部分模型 ⇒ 接上去会把其余全挡在池外。
#
# ⚠️ 判据按**比例**不是绝对条数（2026-10-01 自查修正）：原先写 `covered < 10`，
# 而实测那份表是「69 条里覆盖 10 条」= **14% 覆盖率**却被放行 —— 绝对阈值在这种
# 分子分母双变的情况下形同虚设。表越全，"至少覆盖多少条"才越该跟总量挂钩。
MIN_COVER_RATIO = 0.5
cover_pct = (covered / total * 100) if total else 0.0
print()
if cover_pct < MIN_COVER_RATIO * 100:
    print('✗ 不建议接：覆盖完整仅 %d/%d（%.0f%% < 门槛 %.0f%%）——'
          % (covered, total, cover_pct, MIN_COVER_RATIO * 100))
    print('  表还太薄，接上去会把绝大多数模型挡在池外。等配额恢复重跑一轮再判。')
    sys.exit(1)
if not R:
    print('✗ 不建议接：推理池为空 ⇒ auto:reasoning 会无候选直接报错。')
    sys.exit(1)
if not C:
    print('✗ 不建议接：**编程池为空** ⇒ auto:coding 会无候选直接报错（比不接更糟）。')
    sys.exit(1)
print('✓ 可以接：两池都非空（推理 %d / 编程 %d），覆盖 %d/%d（%.0f%%）。'
      % (len(R), len(C), covered, total, cover_pct))
PYEOF
