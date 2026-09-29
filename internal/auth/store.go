package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 一个已发放 token 的元数据。**没有明文字段**——这不是省略，是结构保证：
// 类型系统里就不存在能让明文落进 tokens.json 的路径。
type Entry struct {
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Hash      string     `json:"hash"`
	VaultPath string     `json:"vault_path,omitempty"`
	Created   time.Time  `json:"created"`
	Expires   *time.Time `json:"expires,omitempty"` // nil = 永不过期（须显式选择）
	Revoked   bool       `json:"revoked"`
	LastUsed  *time.Time `json:"last_used"`
}

// Status 条目当前状态（-token-list 用）。
func (e *Entry) Status(now time.Time) string {
	switch {
	case e.Revoked:
		return "revoked"
	case e.Expires != nil && !now.Before(*e.Expires):
		return "expired" // now >= expires 判死：到期瞬间即失效，不留模糊带（T-024 §10）
	default:
		return "active"
	}
}

// Active 是否可通过校验（未吊销且未过期）。
func (e *Entry) Active(now time.Time) bool { return e.Status(now) == "active" }

type tokenFile struct {
	Tokens []Entry `json:"tokens"`
}

// Store 校验面的全部。长生命周期（网关进程内），零 vault 依赖（TH7）。
//
// 并发模型：entries 整体替换、永不原地改——Verify 持读锁拿到切片后即使
// ReloadIfChanged 并发换页，旧切片依然完整可读（copy-on-write）。
type Store struct {
	path string

	mu      sync.RWMutex
	entries []Entry
	mtime   time.Time // 已加载快照的文件 mtime；零值 = 文件不存在

	now  func() time.Time        // 时钟注入（测试给 fake clock）
	logf func(string, ...any)    // 热加载失败/回写失败的留痕出口

	// touchDisabled=true → TouchAsync 是 no-op（T-026 容器形态）。
	// 容器把 tokens 目录挂成 ro 且宿主 CLI 是唯一写者；容器再回写 last_used
	// 会 EROFS 每 60s/token 刷 WARN 污染日志，且与宿主写者构成"吊销复活"竞态
	// （评审 S2）。关掉它：last_used 审计改由 NATS status 的内存态承担。
	touchDisabled bool

	touchMu       sync.Mutex
	lastTouch     map[string]time.Time // last_used 落盘节流：每 name 至多 60s 一次
	touchInflight map[string]bool
}

// StoreOption Store 的可注入项。
type StoreOption func(*Store)

// WithClock 注入时钟（过期判定的测试入口）。
func WithClock(now func() time.Time) StoreOption {
	return func(s *Store) { s.now = now }
}

// WithLogger 注入日志出口（默认丢弃——校验面不因日志缺失而报错）。
func WithLogger(logf func(string, ...any)) StoreOption {
	return func(s *Store) { s.logf = logf }
}

// WithTouchDisabled 关闭 last_used 回写（T-026 容器形态：ro 挂载 + 宿主单写者）。
// 见 Store.touchDisabled 字段注释——关掉它同时消灭 EROFS 刷屏与双写者竞态。
func WithTouchDisabled(disabled bool) StoreOption {
	return func(s *Store) { s.touchDisabled = disabled }
}

// DefaultTTL -token-gen 不指定 ttl 时的默认过期时长（30 天）。
// 泄露的失血窗口必须有上限（TH4）；永不过期须 -token-ttl 0 显式选择。
const DefaultTTL = 30 * 24 * time.Hour

// touchThrottle last_used 落盘的最小间隔。
// 每请求同步写盘会把审计字段变成性能税+文件竞争源；60s 粒度对
// "这个 token 大概什么时候还在用"这个问题完全够用。
const touchThrottle = 60 * time.Second

// LoadStore 读 tokens.json。
//
//   - 文件不存在 → 空 Store，**不报错**（未配置鉴权 = 兼容模式，现状零变化）
//   - 文件存在但坏了 → 报错（启动时就必须炸，别把"鉴权从未生效"藏起来——
//     与 config.LoadAuto 对 models.auto.json 的态度同款：静默降级只许给"能力缺失"，
//     不许给"配置已声明但损坏"）
func LoadStore(path string, opts ...StoreOption) (*Store, error) {
	s := &Store{
		path:          path,
		now:           time.Now,
		logf:          func(string, ...any) {},
		lastTouch:     make(map[string]time.Time),
		touchInflight: make(map[string]bool),
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// InMemoryStore 不落盘的 Store（config.auth_token 单 token 兼容模式用）。
func InMemoryStore(entries []Entry, opts ...StoreOption) *Store {
	s := &Store{
		now:           time.Now,
		logf:          func(string, ...any) {},
		entries:       entries,
		lastTouch:     make(map[string]time.Time),
		touchInflight: make(map[string]bool),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// reload 从磁盘加载快照。文件不存在 = 空快照（mtime 归零）。
func (s *Store) reload() error {
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.mu.Lock()
		s.entries, s.mtime = nil, time.Time{}
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: stat %s: %w", s.path, err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("auth: read %s: %w", s.path, err)
	}
	var tf tokenFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return fmt.Errorf("auth: parse %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.entries, s.mtime = tf.Tokens, fi.ModTime()
	s.mu.Unlock()
	return nil
}

// ReloadIfChanged mtime 变了才重读（热加载，无 fsnotify，Windows 友好）。
//
// **坏文件保旧快照**：重读失败只记日志、不清空 entries——一个写坏的
// tokens.json 不得把在线网关的鉴权打瞎（打瞎 = 全员 401 = 自造事故）。
// 修好文件后下一次变更自然恢复。
func (s *Store) ReloadIfChanged() {
	if s.path == "" {
		return // InMemoryStore：没有磁盘可热加载
	}
	fi, err := os.Stat(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("auth: stat tokens: %v", err)
		}
		return // 文件消失/不可达 → 保留旧快照（同"坏文件"逻辑）
	}
	s.mu.RLock()
	same := fi.ModTime().Equal(s.mtime)
	s.mu.RUnlock()
	if same {
		return
	}
	if err := s.reload(); err != nil {
		s.logf("auth: reload tokens (keeping previous snapshot): %v", err)
	}
}

// HasEntries 是否登记了至少一个条目（不论状态）。
// 接线层用它决定"挂不挂中间件"：一个条目都没有 = 未配置鉴权 = 不挂（零行为变化）。
func (s *Store) HasEntries() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries) > 0
}

// Verify 校验明文 token，返回命中的条目 name。
//
// 顺序规矩（T-024 §5.1）：**先哈希比对、命中后才查 revoked/expires**——
// 状态检查若放在比对前，"吊销的 token"与"不存在的 token"响应时间不同，
// 状态就成了时序 oracle。哈希定长，ConstantTimeCompare 消除内容侧信道。
//
// 空 Store（无任何条目）→ 恒通过、name 返回 ""：这是"未配置鉴权"的兼容
// 模式。是否启用鉴权由接线层决定（proxy 收到 nil Store = 不挂中间件），
// 不靠 Verify 的返回值猜。
func (s *Store) Verify(plain string) (string, bool) {
	s.mu.RLock()
	entries := s.entries
	s.mu.RUnlock()

	if len(entries) == 0 {
		return "", true // 兼容模式：一个 token 都没配置 = 不鉴权
	}
	if plain == "" {
		return "", false // 配置了鉴权还送空串 → 拒（矩阵"空串→401"格）
	}

	sum := sha256.Sum256([]byte(plain))
	h := hex.EncodeToString(sum[:])
	now := s.now()
	for i := range entries {
		e := &entries[i]
		if subtle.ConstantTimeCompare([]byte(e.Hash), []byte(h)) != 1 {
			continue
		}
		if !e.Active(now) {
			return e.Name, false // 命中但已吊销/过期：拒（name 供调用方留痕）
		}
		return e.Name, true
	}
	return "", false
}

// TouchAsync 异步回写 last_used（审计字段不得反噬可用性，T-024 §10）。
//
// 三重保护：①节流（每 name 60s 至多一次落盘）②去重（同名 inflight 不叠加）
// ③写前重读（缩小与 CLI revoke/rotate 并发写同一文件的竞争窗口——
// 只更新 last_used 字段，不整页覆盖别人的改动）。
// 任何失败只记日志，**永不影响请求**。
func (s *Store) TouchAsync(name string) {
	if s.path == "" || name == "" || s.touchDisabled {
		return
	}
	s.touchMu.Lock()
	if last, ok := s.lastTouch[name]; ok && s.now().Sub(last) < touchThrottle {
		s.touchMu.Unlock()
		return
	}
	if s.touchInflight[name] {
		s.touchMu.Unlock()
		return
	}
	s.touchInflight[name] = true
	s.lastTouch[name] = s.now()
	s.touchMu.Unlock()

	go func() {
		defer func() {
			s.touchMu.Lock()
			delete(s.touchInflight, name)
			s.touchMu.Unlock()
		}()
		if err := touchFile(s.path, name, s.now()); err != nil {
			s.logf("auth: touch last_used(%s): %v", name, err)
		}
	}()
}

// touchFile 重读→只改命中条目的 last_used→原子落盘。
func touchFile(path, name string, now time.Time) error {
	tf, err := readTokenFile(path)
	if err != nil {
		return err
	}
	idx := -1
	for i := range tf.Tokens {
		// 同名多条（revoke 后重发）时取**最后**一条未吊销的——追加序在后，最后即最新。
		if tf.Tokens[i].Name == name && !tf.Tokens[i].Revoked {
			idx = i
		}
	}
	if idx < 0 {
		return nil
	}
	t := now
	tf.Tokens[idx].LastUsed = &t
	return writeTokenFile(path, tf)
}

// ---- 发放面：一次性文件操作（CLI 进程内跑完即退，不共享 Store） ----

// List 读全部条目（-token-list）。文件不存在 → 空表不报错。
func List(path string) ([]Entry, error) {
	tf, err := readTokenFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return tf.Tokens, nil
}

// Issue 生成新 token 并登记（-token-gen）。返回条目——**不返回明文**：
// 发放面的职责就是把明文送进墙里，调用方（CLI）拿不到明文也就无从回显（TH3）。
//
// 事务语义：vault 写成功才落 tokens.json；tokens.json 写失败 → vault delete 回滚。
// 顺序不可反：先落 json 后写 vault 的失败态是"名单上有、墙里没有"——
// 客户端永远取不到 token，且这种失败**没有可见症状**（401 会被误判为网关坏）。
// 反过来失败态是"墙里有、名单上没有"——token 无法使用但可重新生成，症状明确。
//
// 同名规矩：已有 **active** 同名条目 → 拒绝（防止两个客户端共享一个身份，
// 审计就废了）；同名条目全部 revoked/expired → 允许追加（矩阵"吊销后同名→独立条目"）。
func Issue(path, name, vaultPath string, ttl time.Duration, vw VaultWriter, now time.Time) (*Entry, error) {
	if name == "" {
		return nil, errors.New("auth: -token-name required")
	}
	tf, err := readTokenFileOrEmpty(path)
	if err != nil {
		return nil, err
	}
	for i := range tf.Tokens {
		if tf.Tokens[i].Name == name && tf.Tokens[i].Active(now) {
			return nil, fmt.Errorf("auth: token %q already active (revoke or rotate it first)", name)
		}
	}

	plain, err := GenToken()
	if err != nil {
		return nil, err
	}
	if vaultPath != "" {
		if vw == nil {
			return nil, errors.New("auth: vault writer unavailable")
		}
		if err := vw.Put(vaultPath, plain); err != nil {
			return nil, fmt.Errorf("auth: vault put %s: %w", vaultPath, err)
		}
	}

	e := Entry{
		Name:     name,
		Prefix:   DisplayPrefix(plain),
		Hash:     Hash(plain),
		Created:  now,
		LastUsed: nil,
	}
	if vaultPath != "" {
		e.VaultPath = vaultPath
	}
	if ttl > 0 {
		exp := now.Add(ttl)
		e.Expires = &exp
	}

	if err := Register(path, e, now); err != nil {
		if vaultPath != "" && vw != nil {
			// 回滚失败也无非是"墙里多一条没人知道的 token"——它不在名单上，
			// 校验面必拒。记进错误信息让人手动 vault delete 即可。
			if rbErr := vw.Delete(vaultPath); rbErr != nil {
				err = fmt.Errorf("%w (rollback vault delete also failed: %v — run: vault delete %s)",
					err, rbErr, vaultPath)
			}
		}
		return nil, err
	}
	return &e, nil
}

// Register 追加一个已构造好的条目并原子落盘（同名 active 冲突 → 拒）。
//
// 与 Issue 的分工：Issue 管"生成 + 入墙 + 登记"整条发放事务；Register 只管
// "登记"这最后一步，供 -token-plain 模式（调用方自持明文、不经墙）复用同一套
// 冲突检查与原子写。本函数**不接触明文**——条目里只有哈希与前缀。
func Register(path string, e Entry, now time.Time) error {
	tf, err := readTokenFileOrEmpty(path)
	if err != nil {
		return err
	}
	for i := range tf.Tokens {
		if tf.Tokens[i].Name == e.Name && tf.Tokens[i].Active(now) {
			return fmt.Errorf("auth: token %q already active (revoke or rotate it first)", e.Name)
		}
	}
	tf.Tokens = append(tf.Tokens, e)
	if err := writeTokenFile(path, &tf); err != nil {
		return fmt.Errorf("auth: write %s: %w", path, err)
	}
	return nil
}

// Revoke 吊销（-token-revoke）：同名**所有 active** 条目标 revoked。
// 网关侧经 mtime 热加载，下一请求即拒。name 不存在或已全 revoked → 报错（CLI 要可见）。
func Revoke(path, name string, now time.Time) error {
	tf, err := readTokenFile(path)
	if err != nil {
		return err
	}
	n := 0
	for i := range tf.Tokens {
		if tf.Tokens[i].Name == name && tf.Tokens[i].Active(now) {
			tf.Tokens[i].Revoked = true
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("auth: no active token named %q", name)
	}
	return writeTokenFile(path, tf)
}

// Rotate 轮换（-token-rotate）：新 token 入墙（vault 自动留 history，可 undo）
// + 旧条目标 revoked，一个原子文件写完成名单侧变更。
//
// 客户端拿的是指针（vault path 不变）——下次 `vault env` 启动自动取到新值，
// **轮换零客户端动作**。这是指针模式相对裸 token 的核心红利。
func Rotate(path, name string, ttl time.Duration, vw VaultWriter, now time.Time) (*Entry, error) {
	tf, err := readTokenFile(path)
	if err != nil {
		return nil, err
	}
	var old *Entry
	for i := range tf.Tokens {
		if tf.Tokens[i].Name == name && tf.Tokens[i].Active(now) {
			old = &tf.Tokens[i] // 取最后一个 active（正常只有一个）
		}
	}
	if old == nil {
		return nil, fmt.Errorf("auth: no active token named %q", name)
	}
	vaultPath := old.VaultPath // 指针不变

	plain, err := GenToken()
	if err != nil {
		return nil, err
	}
	if vaultPath != "" {
		if vw == nil {
			return nil, errors.New("auth: vault writer unavailable")
		}
		if err := vw.Put(vaultPath, plain); err != nil {
			return nil, fmt.Errorf("auth: vault put %s: %w", vaultPath, err)
		}
		// 墙里已是新值、名单还没改——此刻旧 token 仍 active 可用（宽限窗口）。
		// 这是刻意的顺序：反过来（先废旧再发新）会出现"新旧都不可用"的窗口，
		// 正在跑的客户端全部 401。宽限窗口内旧 token 多活几毫秒，风险可忽略。
	}

	old.Revoked = true
	e := Entry{
		Name: name, Prefix: DisplayPrefix(plain), Hash: Hash(plain),
		VaultPath: vaultPath, Created: now,
	}
	if ttl > 0 {
		exp := now.Add(ttl)
		e.Expires = &exp
	}
	tf.Tokens = append(tf.Tokens, e)
	if err := writeTokenFile(path, tf); err != nil {
		return nil, fmt.Errorf("auth: write %s: %w (vault already rotated; old token still active in store — retry the store write or reconcile manually)", path, err)
	}
	return &e, nil
}

// readTokenFile 读并解析；文件不存在按调用方语义处理（各调用点自行区分）。
func readTokenFile(path string) (*tokenFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tf tokenFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", path, err)
	}
	return &tf, nil
}

func readTokenFileOrEmpty(path string) (tokenFile, error) {
	tf, err := readTokenFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return tokenFile{}, nil
	}
	if err != nil {
		return tokenFile{}, err
	}
	return *tf, nil
}

// writeTokenFile 原子落盘：同目录 tmp + rename。
// 直接覆盖写会把"写一半被杀"变成坏 JSON → 网关热加载失败 → 保旧快照能用，
// 但 CLI 侧下次操作直接报错。tmp+rename 让文件任何时刻都完整。
func writeTokenFile(path string, tf *tokenFile) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tokens-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后是 no-op
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
