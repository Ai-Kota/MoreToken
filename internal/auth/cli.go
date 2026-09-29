package auth

import (
	"fmt"
	"io"
	"time"
)

// VaultPathFor 由 token name 推导默认 vault 指针路径。
// 集中一处，CLI 与文档（QUICKSTART 的 vault env 示例）说的是同一个地址。
func VaultPathFor(name string) string {
	return "moretoken/tokens/" + name
}

// CLI token 管理子命令的入参（main.go 从 flag 组装；测试直构）。
type CLI struct {
	Command    string        // gen | list | revoke | rotate
	Name       string        // -token-name
	TTL        time.Duration // -token-ttl（0 = 永不过期，须显式）
	Plain      bool          // -token-plain：无墙模式，回显一次明文（自担保管）
	TokensFile string        // -tokens-file
	Now        time.Time     // 时钟注入（零值 → time.Now）
	VW         VaultWriter   // 墙实现注入（nil → CliVaultWriter{}；测试给 mock）
	Out        io.Writer     // 输出（nil → io.Discard）
}

// Run 执行子命令，返回进程退出码（0 成功；1 失败——对齐 -check 的语义）。
//
// 发放面的失败必须**响**：这里所有错误都打印并返回 1，绝不静默降级
// （一个悄悄没发出去的 token 会让接入方对着 401 排查半天客户端配置）。
func (c CLI) Run() int {
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	out := c.Out
	if out == nil {
		out = io.Discard
	}
	vw := c.VW
	if vw == nil {
		vw = CliVaultWriter{}
	}

	switch c.Command {
	case "gen":
		return c.runGen(out, vw, now)
	case "list":
		return c.runList(out, now)
	case "revoke":
		return c.runRevoke(out, now)
	case "rotate":
		return c.runRotate(out, vw, now)
	default:
		fmt.Fprintf(out, "未知 token 子命令 %q（gen|list|revoke|rotate）\n", c.Command)
		return 1
	}
}

func (c CLI) runGen(out io.Writer, vw VaultWriter, now time.Time) int {
	if c.Name == "" {
		fmt.Fprintln(out, "错误：-token-gen 需要 -token-name（每客户端独立身份，审计的根基）")
		return 1
	}
	if c.Plain {
		// 无墙模式：明文回显**一次**，此后系统里任何地方都不再有它（只存哈希）。
		// 这是对"机器上没有 vault"的显式妥协，风险由操作者自担（TH3 的例外通道）。
		plain, err := GenToken()
		if err != nil {
			fmt.Fprintf(out, "生成失败：%v\n", err)
			return 1
		}
		e := Entry{
			Name: c.Name, Prefix: DisplayPrefix(plain), Hash: Hash(plain), Created: now,
		}
		if c.TTL > 0 {
			exp := now.Add(c.TTL)
			e.Expires = &exp
		}
		if err := Register(c.TokensFile, e, now); err != nil {
			fmt.Fprintf(out, "登记失败：%v\n", err)
			return 1
		}
		fmt.Fprintf(out, "已发放（-token-plain 无墙模式，明文只显示这一次，请立即妥善保管）：\n")
		fmt.Fprintf(out, "  NAME   %s\n  TOKEN  %s\n", c.Name, plain)
		printExpiry(out, e, now)
		return 0
	}

	// 默认（vault 模式）：明文直入墙，**不回显**——终端、shell history、录屏都拿不到。
	e, err := Issue(c.TokensFile, c.Name, VaultPathFor(c.Name), c.TTL, vw, now)
	if err != nil {
		fmt.Fprintf(out, "发放失败：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已发放（明文只存在于 vault，本终端不回显）：\n")
	fmt.Fprintf(out, "  NAME   %s\n  VAULT  %s\n  PREFIX %s\n", e.Name, e.VaultPath, e.Prefix)
	printExpiry(out, *e, now)
	fmt.Fprintf(out, "\n客户端接入（指针注入，配置文件零明文）：\n")
	fmt.Fprintf(out, "  vault env OPENAI_API_KEY=%s -- <你的智能体命令>\n", e.VaultPath)
	fmt.Fprintf(out, "  vault env ANTHROPIC_AUTH_TOKEN=%s -- claude\n", e.VaultPath)
	fmt.Fprintf(out, "  UI 平台接入时取一次：vault get %s\n", e.VaultPath)
	return 0
}

func printExpiry(out io.Writer, e Entry, now time.Time) {
	if e.Expires == nil {
		fmt.Fprintf(out, "  过期   永不过期（显式选择）\n")
		return
	}
	fmt.Fprintf(out, "  过期   %s（剩余 %s）\n",
		e.Expires.Local().Format("2006-01-02 15:04"), humanDuration(e.Expires.Sub(now)))
}

func (c CLI) runList(out io.Writer, now time.Time) int {
	entries, err := List(c.TokensFile)
	if err != nil {
		fmt.Fprintf(out, "读取失败：%v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintf(out, "（无已登记 token —— 网关当前不鉴权）\n")
		return 0
	}
	fmt.Fprintf(out, "%-16s %-10s %-17s %-17s %-17s %-9s %s\n",
		"NAME", "PREFIX", "CREATED", "LAST-USED", "EXPIRES", "STATUS", "VAULT")
	for i := range entries {
		e := &entries[i]
		fmt.Fprintf(out, "%-16s %-10s %-17s %-17s %-17s %-9s %s\n",
			e.Name, e.Prefix, fmtTime(e.Created), fmtTimePtr(e.LastUsed),
			fmtTimePtr(e.Expires), e.Status(now), orNone(e.VaultPath))
	}
	return 0
}

func (c CLI) runRevoke(out io.Writer, now time.Time) int {
	if c.Name == "" {
		fmt.Fprintln(out, "错误：-token-revoke 需要 -token-name")
		return 1
	}
	if err := Revoke(c.TokensFile, c.Name, now); err != nil {
		fmt.Fprintf(out, "吊销失败：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已吊销 %q。运行中的网关经 mtime 热加载，下一请求即 401（无需重启）。\n", c.Name)
	return 0
}

func (c CLI) runRotate(out io.Writer, vw VaultWriter, now time.Time) int {
	if c.Name == "" {
		fmt.Fprintln(out, "错误：-token-rotate 需要 -token-name")
		return 1
	}
	e, err := Rotate(c.TokensFile, c.Name, c.TTL, vw, now)
	if err != nil {
		fmt.Fprintf(out, "轮换失败：%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已轮换 %q：旧 token 即刻失效，新明文已入墙（指针不变，客户端零动作）。\n", c.Name)
	if e.VaultPath != "" {
		fmt.Fprintf(out, "  VAULT  %s（旧值可用 vault history/undo 找回）\n", e.VaultPath)
	}
	printExpiry(out, *e, now)
	return 0
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return fmtTime(*t)
}

func orNone(s string) string {
	if s == "" {
		return "(plain)"
	}
	return s
}

// humanDuration 粗粒度人话时长（"29d" / "3h" / "45m"）。
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0m"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
