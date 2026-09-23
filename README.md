# MoreToken

A session-preserving routing gateway with **free-first** policy: one endpoint, automatic model switching.

**By default routes through free tiers; seamlessly falls back to paid on failure; deterministically returns to free when health recovers.**

Goals: reduce development cost + keep sessions alive. See [ADR-003](docs/decisions/ADR-003-positioning.md) and [Architecture](docs/architecture/ARCHITECTURE.md) for details.

> **Status: T-000 ~ T-009 delivered and end-to-end verified** (as of 2026-09-19).
> Both protocol paths tested at 200 OK, tool_use and streaming both work, 79 keys loaded via `vault:` pointers.

---

## 🚀 Quick Start

### Build & Run (No External Dependencies Required)

```bash
git clone https://github.com/Ai-Kota/MoreToken.git
cd MoreToken
go build -o bin/moretoken .
./bin/moretoken -config config/config.example.json
```

The gateway starts on `http://localhost:8462` immediately — no vault, no NATS, no database needed.

### Configuration

Keys in `config/config.example.json` support three formats:

```jsonc
{
  "id": "example", "base_url": "https://api.example.com/v1",
  "format": "openai", "tier": "free",
  "keys": [
    "sk-plaintext-key",           // Plain text (insecure for committed configs)
    "env:EXAMPLE_API_KEY",        // From environment variable ⭐ Recommended
    "vault:provider/example"      // From vault store (optional)
  ]
}
```

**To start without vault/NATS:**

1. Copy `config/config.example.json` → `config/config.json`
2. Use `env:` prefix for all keys (or plain strings for testing)
3. Run: `./bin/moretoken -config config/config.json`

All core routing works. Monitoring features gracefully disable when vault/NATS are absent.

---

## Features

- **Stable endpoint**: Claude Code / OpenAI clients configure once at `http://localhost:8462`, provider failures don't break sessions
- **Virtual models**: Clients specify "what type" (`auto:reasoning`), gateway picks the concrete model — model changes are transparent to clients
- **Dual protocol**: `/v1/messages` (Anthropic, for Claude Code) + `/v1/chat/completions` (OpenAI)
- **Format isolation**: Two protocol paths are completely isolated internally, each with independent free→paid chains
- **Free-first + deterministic regression**: Any healthy free key routes free; all down → paid fallback; free recovery ≥T min triggers return (debounce <T prevents flapping)
- **Multi-key rotation**: Account-level rate limits absorbed by key rotation (20 RPM/account); 429 cooldown 60s, 401/403 ban 1h
- **Zero external Go dependencies**: Pure standard library, single binary

---

## 📊 What's Optional vs Required

| Feature | Required? | When Needed |
|---------|-----------|-------------|
| **Core routing** | ✅ Always | Never optional |
| **Key resolution** | `env:` keys work without vault | Use `env:` prefix for keys or `VAULT_BIN` env var |
| **Monitoring/NATS** | ❌ Optional | Missing = no-op, gateway still serves requests |
| **WorkBoard UI** | ❌ Optional | Separate frontend repo, not needed for operation |

**Default behavior without infrastructure:**
- No vault → keys with `vault:` prefix show as "unresolved", excluded from pool
- No NATS → no status publishing to WorkBoard, but `/health` endpoint still works locally
- Gateway remains fully functional with any keys that resolve successfully

---

## 🆚 Comparison with FreeLLMAPI

| Dimension | MoreToken | FreeLLMAPI |
|-----------|-----------|------------|
| **Cost certainty** | ✅ **Deterministic state machine**: free → paid → free (guaranteed) | ❌ Scoring pool with bandit exploration; random sampling may hit paid |
| **Session persistence** | ✅ **Constant endpoint**, automatic provider swap under the hood | ⚠️ Session may break when switching providers |
| **Code footprint** | ~2000 lines, pure stdlib, no DB | 1600+ lines router + 48 libs + SQLite + 40+ migrations |
| **Build & deploy** | Single binary, `go build` | Node.js + SQLite + desktop UI install |
| **Observability** | `/health` + `/decisions` endpoints + optional NATS stream | Limited — no built-in decision audit trail |
| **Self-healing** | Deterministic regression (returns to free when recovered ≥T min) | Best-effort, no guarantee |
| **External dependencies** | Zero Go deps; vault/NATS optional CLI tools | Multiple npm packages + SQLite |

### MoreToken is strictly better at:

1. **Cost certainty** — mathematical guarantee to maximize free tier usage
2. **Maintainability** — single binary, no database, easy to audit and modify
3. **Ownership** — your cost profile is visible and predictable
4. **Deployment simplicity** — download binary, configure `config.json`, run

### FreeLLMAPI remains stronger at:

- High concurrency resilience (circuit breakers, model-level benchmarking, canary deployments)
- Full request logging to database
- Multi-protocol support (OpenAI/Anthropic/Responses/Gemini wire)
- Containerized multi-instance operations

**Bottom line**: MoreToken trades raw scale for cost certainty and simplicity. If you value knowing exactly when you're using free vs paid models, MoreToken is the right choice.

---

## API Endpoints

| Endpoint | Method | Description |
|------|------|------|
| `/v1/messages` | POST | Claude Code (Anthropic Messages protocol) |
| `/v1/chat/completions` | POST | OpenAI-compatible client |
| `/v1/models` | GET | Model listing (includes virtual models like `auto:reasoning`) |
| `/health` | GET | Pool status: `keys` (actually usable) / `unresolved` / `cooling` / `backed_off` |
| `/decisions` | GET | Recent routing decisions log (`?n=` controls count; no key values exposed) |
| `/doctor` | GET | Self-diagnosis: exit code 0 = healthy, 1 = degraded, 2 = unreachable |

---

## Project Structure

```
moretoken/
├── main.go                    # Entry: assemble config → router → proxy, start state machine
├── config/config.example.json # Template config (copy to config.json and edit)
├── internal/
│   ├── config/                # Config load + key pointer resolution (env:/vault:)
│   ├── provider/              # Upstream calls + failure categorization + URL assembly
│   ├── pool/                  # Multi-key pool: RR + 429 cooldown + 401/403 long ban
│   ├── router/                # Format isolation + free-first + same-format fallback
│   ├── state/                 # Free-tier observer: targeted probe + smooth reporting
│   ├── proxy/                 # Protocol endpoints + decision log
│   └── nats/                  # Status/event publish (exec nats.cli, missing degrades to no-op)
└── docs/
    ├── decisions/             # ADR-001(history) ADR-002(tech stack) ADR-003(positioning)
    ├── architecture/          # ARCHITECTURE.md
    ├── quality/               # TEST-MATRIX.md (invariants I1-I9)
    └── tasks/                 # TASKS.md + per-task definitions
```

---

## Documentation

- [Positioning Decision ADR-003](docs/decisions/ADR-003-positioning.md)
- [System Architecture](docs/architecture/ARCHITECTURE.md)
- [Test Matrix (invariants I1-I9)](docs/quality/TEST-MATRIX.md)
- [Task Overview](docs/tasks/TASKS.md)
- [Troubleshooting](docs/TROUBLESHOOTING.md)

---

**License:** MIT — see [LICENSE](LICENSE).
**Copyright:** © 2026 Ai-Kota
