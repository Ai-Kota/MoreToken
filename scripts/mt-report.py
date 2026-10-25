#!/usr/bin/env python3
"""mt-report.py —— 读 MoreToken 运行记录（T-050），给智能体出优化证据摘要。

用法：
  python scripts/mt-report.py                 # 最近 1 天
  python scripts/mt-report.py --days 7
  python scripts/mt-report.py --json          # 机器可读，便于智能体继续分析
  python scripts/mt-report.py --dir docker/runtime/telemetry --days 30

记录格式见 docs/telemetry.md。只读，不发请求。
"""
from __future__ import annotations

import argparse
import datetime as dt
import glob
import json
import os
from collections import Counter, defaultdict

OUTCOMES = [
    "ok", "client_canceled", "exhausted", "context_too_long", "model_not_found",
    "request_too_large", "rate_limited", "upstream_error", "client_error",
    "auth_error", "forbidden", "not_found", "no_response", "unknown",
]


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="MoreToken telemetry report")
    p.add_argument("--dir", default=os.path.join(
        os.path.dirname(__file__), "..", "docker", "runtime", "telemetry"))
    p.add_argument("--days", type=int, default=1, help="只看最近 N 天（0=全部）")
    p.add_argument("--json", action="store_true", help="输出 JSON 摘要")
    return p.parse_args()


def load(dir_path: str, days: int) -> tuple[list[dict], list[dict], list[str]]:
    files = sorted(glob.glob(os.path.join(dir_path, "telemetry-*.jsonl")))
    if days > 0:
        cutoff = dt.date.today() - dt.timedelta(days=days - 1)
        kept = []
        for f in files:
            name = os.path.basename(f)
            try:
                d = dt.date.fromisoformat(name[len("telemetry-"):-len(".jsonl")])
            except ValueError:
                kept.append(f)
                continue
            if d >= cutoff:
                kept.append(f)
        files = kept
    reqs, snaps = [], []
    for f in files:
        with open(f, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if row.get("type") == "request":
                    reqs.append(row)
                elif row.get("type") == "snapshot":
                    snaps.append(row)
    return reqs, snaps, files


def pct(vals: list[int], q: float) -> int:
    if not vals:
        return 0
    xs = sorted(vals)
    idx = max(0, min(len(xs) - 1, int(round((len(xs) - 1) * q))))
    return xs[idx]


def lat_stats(rows: list[dict], key: str) -> dict:
    vals = [int(r.get(key) or 0) for r in rows if r.get(key)]
    if not vals:
        return {"n": 0, "p50": 0, "p95": 0, "p99": 0, "max": 0}
    return {
        "n": len(vals),
        "p50": pct(vals, 0.50),
        "p95": pct(vals, 0.95),
        "p99": pct(vals, 0.99),
        "max": max(vals),
    }


def summarize(reqs: list[dict], snaps: list[dict], files: list[str]) -> dict:
    by = lambda k: Counter(r.get(k) or "?" for r in reqs)
    canceled = sum(1 for r in reqs if r.get("outcome") == "client_canceled")
    ok = sum(1 for r in reqs if r.get("outcome") == "ok")
    denom = len(reqs) - canceled
    success_rate = (ok / denom) if denom else 0.0

    models: dict[str, list[dict]] = defaultdict(list)
    for r in reqs:
        models[f"{r.get('provider') or '?'} / {r.get('model') or '?'}"].append(r)

    key_attempts = Counter()
    key_fails = Counter()
    key_fail_kinds: dict[str, Counter] = defaultdict(Counter)
    fail_reasons = Counter()
    for r in reqs:
        if r.get("outcome") not in (None, "ok", "client_canceled") and r.get("fail_reason"):
            fail_reasons[str(r["fail_reason"])[:160]] += 1
        for a in r.get("attempts") or []:
            kid = a.get("key_id") or f"{a.get('provider')}/?"
            key_attempts[kid] += 1
            if a.get("kind") not in (None, "ok"):
                key_fails[kid] += 1
                key_fail_kinds[kid][a.get("kind")] += 1

    tokens_in = sum(int(r.get("input_tokens") or 0) for r in reqs)
    tokens_out = sum(int(r.get("output_tokens") or 0) for r in reqs)
    usage_known = sum(1 for r in reqs if r.get("usage_known"))

    # T-049：coding 供给冗余证据——哪些 provider 被准入过滤空、最终由谁服务、是否真失败。
    coding = [r for r in reqs if r.get("want_kind") == "coding"]
    coding_empty = Counter()
    coding_served = Counter()
    for r in coding:
        for a in r.get("attempts") or []:
            if a.get("kind") == "admission_empty":
                coding_empty[a.get("provider") or "?"] += 1
        if r.get("outcome") == "ok":
            coding_served[r.get("provider") or "?"] += 1

    return {
        "files": [os.path.basename(f) for f in files],
        "requests": len(reqs),
        "snapshots": len(snaps),
        "ok": ok,
        "client_canceled": canceled,
        "success_rate_excl_cancel": round(success_rate, 4),
        "outcomes": dict(by("outcome")),
        "protocols": dict(by("protocol")),
        "clients": dict(by("client")),
        "providers": dict(by("provider")),
        "want_kinds": dict(by("want_kind")),
        "latency": lat_stats(reqs, "total_ms"),
        "ttft": lat_stats([r for r in reqs if r.get("stream")], "ttft_ms"),
        "tokens": {"input": tokens_in, "output": tokens_out, "usage_known_requests": usage_known},
        "coding_supply": {
            "requests": len(coding),
            "served_by_provider": dict(coding_served),
            "admission_empty_by_provider": dict(coding_empty),
            "failed": sum(1 for r in coding
                          if r.get("outcome") not in (None, "ok", "client_canceled")),
        },
        "models": {
            k: {
                "count": len(v),
                "ok": sum(1 for r in v if r.get("outcome") == "ok"),
                "fail": sum(1 for r in v if r.get("outcome") not in (None, "ok", "client_canceled")),
                "p95_ms": lat_stats(v, "total_ms")["p95"],
            }
            for k, v in models.items()
        },
        "keys": {
            k: {"attempts": n, "fails": key_fails[k], "fail_kinds": dict(key_fail_kinds[k])}
            for k, n in key_attempts.items()
        },
        "top_fail_reasons": fail_reasons.most_common(10),
        "latest_snapshot": snaps[-1] if snaps else None,
    }


def print_text(s: dict) -> None:
    print("=== MoreToken telemetry report ===")
    print(f"files: {', '.join(s['files']) if s['files'] else '(none)'}")
    if not s["requests"]:
        print("requests: 0（没有请求记录；看 latest_snapshot 判断网关是否活着）")
    else:
        print(f"requests: {s['requests']} | ok {s['ok']} ({s['success_rate_excl_cancel']*100:.1f}% "
              f"excluding client_canceled) | canceled {s['client_canceled']}")
    print(f"outcomes: {s['outcomes']}")
    print(f"protocols: {s['protocols']}")
    print(f"clients: {s['clients']}")
    print(f"providers: {s['providers']}")
    print(f"want_kinds: {s['want_kinds']}")
    cs = s.get("coding_supply") or {}
    if cs.get("requests"):
        print(f"coding_supply: requests={cs['requests']} served={cs['served_by_provider']} "
              f"admission_empty={cs['admission_empty_by_provider']} failed={cs['failed']}")
    lat = s["latency"]
    print(f"latency_ms: n={lat['n']} p50={lat['p50']} p95={lat['p95']} p99={lat['p99']} max={lat['max']}")
    tt = s["ttft"]
    print(f"ttft_ms(stream): n={tt['n']} p50={tt['p50']} p95={tt['p95']} p99={tt['p99']}")
    print(f"tokens: in={s['tokens']['input']} out={s['tokens']['output']} "
          f"(usage known in {s['tokens']['usage_known_requests']} requests)")
    print("\nmodels (top 15 by count):")
    for k, v in sorted(s["models"].items(), key=lambda kv: kv[1]["count"], reverse=True)[:15]:
        print(f"  {k:<48} n={v['count']:<5} ok={v['ok']:<5} fail={v['fail']:<4} p95={v['p95_ms']}ms")
    print("\nkeys (top 15 by attempts):")
    for k, v in sorted(s["keys"].items(), key=lambda kv: kv[1]["attempts"], reverse=True)[:15]:
        fk = ",".join(f"{a}:{b}" for a, b in v["fail_kinds"].items()) or "-"
        print(f"  {k:<40} attempts={v['attempts']:<5} fails={v['fails']:<4} kinds={fk}")
    if s["top_fail_reasons"]:
        print("\ntop fail reasons:")
        for reason, n in s["top_fail_reasons"]:
            print(f"  {n:>4}  {reason}")
    snap = s["latest_snapshot"]
    if snap:
        print(f"\nlatest snapshot: {snap.get('ts')} tier={snap.get('tier')} "
              f"uptime={snap.get('uptime_s')}s")
        for p in snap.get("pools") or []:
            print(f"  pool {p.get('provider'):<24} keys={p.get('keys')} "
                  f"cooling={p.get('cooling')} backed_off={p.get('backed_off')}")
        m = (snap.get("metrics") or {})
        if m:
            print(f"  metrics: total={m.get('total')} fail={m.get('fail')} "
                  f"success_rate={m.get('success_rate')} avg_ms={m.get('avg_ms')}")
        a = snap.get("admission") or {}
        if a:
            print(f"  admission: active={a.get('active')} models={a.get('models')} "
                  f"reasoning={a.get('reasoning')} coding={a.get('coding')} "
                  f"min_ratio={a.get('min_ratio')} err={a.get('err')}")


def main() -> None:
    args = parse_args()
    reqs, snaps, files = load(os.path.abspath(args.dir), args.days)
    summary = summarize(reqs, snaps, files)
    if args.json:
        print(json.dumps(summary, ensure_ascii=False, indent=1))
    else:
        print_text(summary)


if __name__ == "__main__":
    main()
