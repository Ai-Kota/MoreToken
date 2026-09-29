# Quick Start Guide — MoreToken

Get a working gateway running in 5 minutes. No vault, no NATS, no database required.

---

## Prerequisites

- **Go 1.25+** installed ([download](https://go.dev/dl/))
- A terminal (PowerShell, bash, etc.)
- (Optional) One or more API keys from LLM providers

---

## 1. Install

```bash
# Clone the repository
git clone https://github.com/Ai-Kota/MoreToken.git
cd MoreToken

# Build the binary
go build -o bin/moretoken .
```

You now have `bin/moretoken` (or `moretoken.exe` on Windows).

---

## 2. Create Configuration

Create a minimal config file:

```bash
cat > config.json << 'EOF'
{
  "listen": ":8462",
  "providers": [
    {
      "id": "my-free",
      "base_url": "https://api.example.com/v1",
      "format": "openai",
      "tier": "free",
      "keys": [
        "sk-your-free-key-here"
      ],
      "models": [
        {
          "id": "gpt-4o-mini",
          "name": "GPT-4o Mini (Free)",
          "kinds": ["reasoning", "coding", "general", "fast"],
          "context_length": 128000
        }
      ]
    },
    {
      "id": "my-paid",
      "base_url": "https://api.example.com/v1",
      "format": "openai",
      "tier": "paid",
      "keys": [
        "sk-your-paid-key-here"
      ],
      "models": [
        {
          "id": "gpt-4o",
          "name": "GPT-4o",
          "kinds": ["reasoning", "coding", "general", "fast"],
          "context_length": 128000
        }
      ]
    }
  ]
}
EOF
```

**Key formats supported:**

| Format | Example | Notes |
|--------|---------|-------|
| Plain string | `"sk-abc123"` | Simple but insecure for committed configs |
| Environment variable | `"env:MY_API_KEY"` | **Recommended** — key lives in your shell |
| Vault store | `"vault:provider/example"` | Requires `vault.exe` on PATH |

---

## 3. Run

```bash
# Start the gateway
./bin/moretoken -config config.json
```

You should see:

```
2026/09/23 12:00:00 Config loaded: 2 providers, 2 keys resolved
2026/09/23 12:00:00 NATS: CLI not found → status publishing disabled
2026/09/23 12:00:00 MoreToken listening on :8462
```

---

## 4. Connect Your Client

### For Claude Code (Anthropic protocol)

```powershell
# Set environment variables
$env:ANTHROPIC_BASE_URL = "http://localhost:8462"
$env:ANTHROPIC_MODEL = "auto:reasoning"

# Launch Claude Code
cc-ft
```

### For OpenAI-compatible clients

```bash
export OPENAI_BASE_URL=http://localhost:8462
export OPENAI_MODEL="auto:coding"
```

**Virtual models you can use:**

| Model Name | Meaning |
|------------|---------|
| `auto` | Any available model |
| `auto:reasoning` | Reasoning-capable models |
| `auto:coding` | Coding-optimized models |
| `auto:fast` | Lowest-latency models |
| `auto:general` | General-purpose models |

---

## 5. Verify It Works

### Check health endpoint

```bash
curl http://localhost:8462/health
```

Expected response:

```json
{
  "tier": "free",
  "pools": [
    {
      "provider": "my-free",
      "format": "openai",
      "tier": "free",
      "keys": 1,
      "unresolved": 0,
      "cooling": 0,
      "backed_off": 0
    }
  ]
}
```

### List available models

```bash
curl http://localhost:8462/v1/models
```

### View routing decisions

```bash
curl http://localhost:8462/decisions?n=10
```

---

## 6. Test a Real Request

```bash
curl -X POST http://localhost:8462/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto:general",
    "messages": [{"role": "user", "content": "Say hello"}],
    "max_tokens": 100
  }'
```

If everything works, you'll get a proper chat completion response routed through one of your configured providers.

---

## 7. Secure the Gateway (Token Auth)

By default the gateway accepts all requests — fine for localhost-only use.
If the port is reachable from your LAN, issue tokens (one per client) so that
strangers can't spend your upstream keys:

```bash
# Issue a token. The plaintext goes straight into the vault (never echoed,
# never touches disk); config/tokens.json only stores a SHA-256 hash + pointer.
./bin/moretoken -token-gen -token-name my-laptop

# Every client gets its own token (independent revocation + audit trail):
./bin/moretoken -token-gen -token-name dify-box -token-ttl 720h

# No vault on this machine? Explicit fallback — plaintext is shown ONCE:
./bin/moretoken -token-gen -token-name guest -token-plain
```

Restart the gateway once to pick up auth (from then on, revoke/rotate
hot-reload — no restart needed):

```bash
./bin/moretoken -config config.json
# → auth: 入站 token 校验已启用（/health 豁免）
```

**Connect agents via the vault pointer** (zero plaintext in any config file):

```bash
vault env ANTHROPIC_AUTH_TOKEN=moretoken/tokens/my-laptop -- claude
vault env OPENAI_API_KEY=moretoken/tokens/dify-box -- python my_agent.py

# UI platforms (Dify/n8n): fetch once and paste into their own secret store
vault get moretoken/tokens/dify-box
```

**Lifecycle management:**

```bash
./bin/moretoken -token-list                      # who has a token, status, last used
./bin/moretoken -token-revoke -token-name guest  # takes effect on the NEXT request
./bin/moretoken -token-rotate -token-name my-laptop  # new secret into vault, pointer unchanged
```

Notes:
- `/health` is always exempt (liveness probes must never be blocked); everything else including `/v1/models` and `/decisions` requires a token.
- Both header styles work: `Authorization: Bearer mt_…` (OpenAI clients) and `x-api-key: mt_…` (Anthropic clients).
- Tokens expire after 30 days by default (`-token-ttl 0` = never, explicit choice only).
- The single-token `auth_token` field in config.json (supports `env:`/`vault:` prefixes) is a compatibility mode; tokens.json takes precedence.

---

## Common Scenarios

### Using environment variables for keys

```bash
# Set your key in the shell
export MY_FREE_KEY="sk-xxx"
export MY_PAID_KEY="sk-yyy"

# In config.json, reference them:
"keys": ["env:MY_FREE_KEY", "env:MY_PAID_KEY"]
```

### Running without any external dependencies

MoreToken works completely standalone. If you don't have vault or NATS:

- Keys with `vault:` prefix → show as "unresolved" in `/health`, excluded from routing
- NATS status publishing → silently disabled
- **All core routing still works normally**

### Checking supply coverage before starting

```bash
./bin/moretoken -check -config config.json
```

Returns exit code 0 if all needed models are covered, non-zero otherwise.

---

## Next Steps

1. **Add more providers** — copy the provider blocks in `config.json` and adjust `base_url`, `id`, and `keys`
2. **Enable monitoring** — install vault and NATS CLIs for full observability
3. **Read the docs** — check [docs/](docs/) for architecture details and troubleshooting

---

## Need Help?

- Check [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for common issues
- Open an issue on GitHub: https://github.com/Ai-Kota/MoreToken/issues
- See [CONTRIBUTING.md](CONTRIBUTING.md) for how to help improve MoreToken
