# MoreToken

A session-preserving routing gateway with **free-first** policy: one endpoint, automatic model switching.

**By default routes through free tiers; seamlessly falls back to paid on failure; deterministically returns to free when health recovers.**

Goals: reduce development cost + keep sessions alive. See [ADR-003](docs/decisions/ADR-003-positioning.md) and [Architecture](docs/architecture/ARCHITECTURE.md) for details.

> **Status: T-000 ~ T-009 delivered and end-to-end verified** (as of 2026-09-19).
> Both protocol paths tested at 200 OK, tool_use and streaming both work, 79 keys loaded via `vault:` pointers.
> The only outstanding item is **T-008 frontend subscription** — belongs to external repo `E:/repos/aimly-console`, this repo has delivered the contract and backend publish side.

## Features

- **Stable endpoint**: Claude Code / OpenAI clients configure once at `http://localhost:8462`, provider failures don't break sessions
- **Virtual models**: Clients specify "what type" (`auto:reasoning`), gateway picks the concrete model — model changes are transparent to clients
- **Dual protocol**: `/v1/messages` (Anthropic, for Claude Code) + `/v1/chat/completions` (OpenAI)
- **Format isolation**: Two protocol paths are completely isolated internally, each with independent free→paid chains
- **Free-first + deterministic regression**: Any healthy free key routes free; all down → paid fallback; free recovery ≥T min triggers return (debounce <T prevents flapping)
- **Multi-key rotation**: Account-level rate limits absorbed by key rotation (agnes free tier 20 RPM/**account**, accounts isolated); 429 cooldown 60s, 401/403 ban 1h
- **Zero external deps**: Go 1.25 pure stdlib, single binary; NATS and vault called via `exec` to local CLI, no Go dependencies

## Build and Run

```bash
go build -o bin/moretoken.exe .
bin/moretoken.exe -config config/config.json
```

Long-lived via dev-fleet (`ops/dev-fleet/services.yaml` entry `moretoken`); manually started processes die with the session.

## Key Configuration (Important)

**Keys are NOT written to `config/config.json`** — that file is committed, plaintext in repo = leak. Keys are **pointers**, resolved at startup:

```jsonc
{
  "id": "agnes", "base_url": "https://apihub.agnes-ai.com/v1",
  "format": "openai", "tier": "free",
  "keys": ["vault:freellm/provider/agnes/agnes-aimly", "env:AGNES_KEY_2"]
}
```

| Prefix | Meaning |
|------|------|
| `vault:PATH` | Vault store entry (`vault get PATH`). **Recommended** — plaintext lives only in the vault |
| `env:NAME`  | Environment variable |

Repo spec `ops/dev-fleet/STANDARD.md` §7 requires keys from vault, no plaintext in config.

**Parse failure semantics**: not fatal, placeholder retained → that key treated as "no credentials" and excluded from pool → pool empty → returns **503** (rather than sending placeholder as key upstream and getting 401 back).
Failure details written to startup log (`WARN unresolved key: <provider>[N]: vault:<path> (<reason>)`), reflected in `/health` `keys` / `unresolved` fields.

## API Endpoints

| Endpoint | Method | Description |
|------|------|------|
| `/v1/messages` | POST | Claude Code (Anthropic Messages protocol) |
| `/v1/chat/completions` | POST | OpenAI-compatible client |
| `/v1/models` | GET | Model listing |
| `/health` | GET | Pool status: `keys` (actually usable) / `unresolved` / `cooling` / `backed_off` |
| `/decisions` | GET | Recent routing decisions log (`?n=` controls count; no key values, no request body; virtual models include `want`/`model`) |

## Monitoring

**WorkBoard** (external repo `E:/repos/aimly-console`) MoreToken module:

```bash
cd E:/repos/aimly-console
npm run dev
```

Renders current tier (free/paid), pool status table (keys / unresolved / cooling / backed_off),
switch event timeline; **90s no status = shows "bus offline"**, no silent failure.

**Data path**: this gateway → NATS `aimly.system.freetier.status` (30s) / `.event` (instant) → frontend subscription.

> ⚠️ This path was long broken (T-014): `nats.exe` not in service PATH, and gateway-inherited
> `NATS_SERVERS`/`NATS_USER_CREDS`/`NATS_CA_FILE` **stock nats CLI doesn't recognize any of them**
> (it recognizes `NATS_URL`/`NATS_CREDS`/`NATS_CA`), CLI falls back to its default context
> (pointing at local `127.0.0.1:4222`) → publish all fail, errors ignored → silence.
> Startup log now explicitly prints `nats: publish enabled (cli=… server=…)`, publish failures logged deduplicated WARN by reason.

> **Request body limit**: default **32 MiB**,超限返回 **413** with limit info.
> Old implementation silently truncated via `io.LimitReader` — overflow not reported, just read less, half JSON forwarded upstream,
> upstream returns `unexpected end of JSON input` blaming the caller, gateway log stays silent.
> ~220K tokens hits the wall, long sessions必然踩. Now changed to explicit rejection.

## Virtual Models (Recommended Usage)

Clients **don't need to name specific models**, just declare "what type", gateway picks one available:

```jsonc
{"model": "auto"}             // any type
{"model": "auto:reasoning"}   // reasoning type
{"model": "auto:coding"}      // coding type
{"model": "auto:fast"}        // fast
{"model": "auto:general"}     // general
```

`GET /v1/models` lists these virtual names for client discovery.

**Where types are declared**: `kinds` field in each model entry in `config/config.json`.

> ⚠️ **Which types actually distinguish** (2026-09-19实测, evidence at [`docs/quality/MODEL-CAPABILITY.md`](docs/quality/MODEL-CAPABILITY.md)):
>
> | Type | Distinguishes? | Note |
> |------|:--------------:|------|
> | `reasoning` | ✅ **Yes** | Train problem 10 samples: `agnes-2.5-flash` 10/10, `minimax-m3` 6/10, rest 0/10 |
> | `fast` | ✅ Yes | Median latency 0.9s–1.9s (agnes 3 models only have data) |
> | `coding` | ❌ **No** | All models 88–100%, `auto:coding` effectively equals `auto` |
> | `general` | ❌ **No** | All models 100% |
>
> `coding`/`general` retained as **universal capability** declarations (truthful), but **don't expect them to filter for better coders** — that would be false precision. Distinguishing requires evaluation data first.

**Semantic guarantees**:

| Behavior | Note |
|------|------|
| Filter candidates by type | Providers without the type skipped; **explicit error if nobody provides it**, no degradation to unrelated model |
| Each candidate parses independently | Provider A and B may hold different models, each picks its own |
| model field rewritten | Virtual name never sent upstream (would return "model not found" pointing wrong direction) |
| Literal models unaffected | Client sends concrete model name → **body unchanged byte-for-byte**, behavior identical to before |
| Switches visible | `/decisions` records `want` (e.g. `auto:reasoning`) and `model` (concrete model landed on),便于发现上游换了 |

**Why switching is safe**: agnes credentials don't restrict models (same key serves all models), so **switching = changing account, not model**; and agnes doesn't return `thinking` blocks, conversation history has no model-specific signature, cross-model resend naturally acceptable (all tested).

> Session continuity guaranteed by **client resending full history** — gateway fully stateless, doesn't store sessions, no context handoff.
> Agent-side session resume (three-element protocol) is orthogonal to gateway responsibilities.

## Connect Claude Code

**Type `cc-ft` directly in terminal** (14th launch path, since 2026-09-19):

```powershell
cc-ft          # interactive
cc-ft -p "..." # headless
```

What it does: probe `:8462` (if unreachable, explicit error and abort, **no silent hang**) → switch `CLAUDE_CONFIG_DIR`
to `D:\ClaudeConfig\.claude\profiles\cc-ft` → start Claude Code.

`ANTHROPIC_MODEL` is **virtual model** `auto:reasoning` — so upstream model changes, account switches, free↔paid transitions,
**this launch path needs no config changes**.

Manual connect (any client):

```bash
set ANTHROPIC_BASE_URL=http://localhost:8462
set ANTHROPIC_MODEL=auto:reasoning
```

Free tier has **two vendors** (agnes native Anthropic protocol, xkiro实测 `/v1/messages` also 200):
`agnes-anthropic` → `xkiro-anthropic` (MiniMax/Qwen, cross-vendor); all down → `agnes-paid-anthropic` fallback.

> **Vendor diversity**: agnes and xkiro are different platforms, agnes platform-level failure → xkiro still serves.
> But **paid fallback still same-vendor as free主力** (agnes) — true paid-tier cross-vendor isolation needs another vendor's paid key in repo.

### Upstream Layout

| provider | protocol | tier | accounts | Vendor / Models |
|----------|------|:--:|:------:|------------|
| `agnes` | openai | free | 33 | agnes (agnes-2.5/2.0-flash) |
| `xkiro` | openai | free | 11 | **xkiro** (MiniMax M3 / Qwen3 Coder / Qwen3.7 Max, `:free` only) |
| `agnes-anthropic` | anthropic | free | 33 | agnes |
| `xkiro-anthropic` | anthropic | free | 11 | **xkiro** (cross-vendor fallback) |
| `agnes-paid-anthropic` | anthropic | paid | 1 | agnes Token Plan |
| `agnes-paid-openai` | openai | paid | 1 | agnes Token Plan |

## Project Structure

```
moretoken/
├── main.go                    # Entry: assemble config → router → proxy, start state machine + NATS publish
├── config/config.json         # Provider declarations (keys as vault:/env: pointers)
├── internal/
│   ├── config/                # Config load + key pointer resolution (env:/vault:)
│   ├── provider/              # Upstream calls + failure categorization (I7 retry safety boundary) + URL assembly
│   ├── pool/                  # Multi-key pool: RR + 429 cooldown + 401/403 long ban
│   ├── router/                # Format isolation + free-first + same-format fallback (per-request fallback)
│   ├── state/                 # Free-tier observer: targeted probe + smooth reporting (**doesn't participate in routing**, since 2026-09-20)
│   ├── proxy/                 # Protocol endpoints + decision log
│   └── nats/                  # Status/event publish (exec nats.cli, missing degrades to no-op)
└── docs/
    ├── decisions/             # ADR-001(history) ADR-002(tech stack) ADR-003(positioning)
    ├── architecture/          # ARCHITECTURE.md
    ├── quality/               # TEST-MATRIX.md (invariants I1-I9 + coverage registry)
    ├── tasks/                 # TASKS.md + per-task definitions
    └── workboard-contract.md  # Frontend subscription contract (T-008 basis)
```

## Documentation

- [Positioning Decision ADR-003](docs/decisions/ADR-003-positioning.md)
- [System Architecture](docs/architecture/ARCHITECTURE.md)
- [Test Matrix (invariants I1-I9)](docs/quality/TEST-MATRIX.md)
- [Task Overview](docs/tasks/TASKS.md)
- [WorkBoard Contract](docs/workboard-contract.md)
