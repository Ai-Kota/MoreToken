// Package auth 网关入站 token 鉴权（T-024）。
//
// 两面分离是本包的第一设计原则：
//
//	发放面（低频，CLI）：生成明文 → vault put（stdin 管道）→ tokens.json 只落哈希+指针
//	校验面（每请求，网关内）：SHA-256 + ConstantTimeCompare，**零 vault 依赖**
//
// 校验面不碰 vault 是刻意的（T-024 §2 TH7）：vault 挂了不得影响已接入智能体，
// 鉴权路径上多一个外部进程 = 多一个故障面。vault 只是发放渠道。
//
// 铁律：明文 token 只存在于三处——crypto/rand、vault 墙内、客户端进程内存。
// 本包任何函数都不把明文写进 tokens.json、日志或命令行参数。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
)

// TokenPrefix 明文 token 的固定前缀。
// 对齐 GitHub secret scanning 惯例：泄露扫描器可据此识别，人眼也能一眼认出"这是网关 token"。
const TokenPrefix = "mt_"

// b62Alphabet base62 字符集。刻意不含 `+`/`=`/`-` 之外的易混字符？——不，
// base62 本身就是 [0-9A-Za-z]，没有 shell/JSON 转义地雷，这才是选它的原因。
const b62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// tokenRandBytes 明文熵源字节数。32 字节 = 256 bit CSPRNG。
const tokenRandBytes = 32

// tokenBodyLen base62(32 字节) 的定长正文长度。
// 62^43 > 2^256 > 62^42，所以 43 字符是 256 bit 无损容纳的最短定长。
const tokenBodyLen = 43

// GenToken 生成一个新明文 token（mt_ + 43 字符 base62）。
//
// 用 big.Int 做进制转换而不是"每字节 mod 62"——后者有 modulo bias
// （256 % 62 = 8，前 8 个字符出现概率偏高）。偏差虽小，但鉴权 token
// 的字符分布应当无可指摘，big.Int 转换无偏且同样只用标准库。
func GenToken() (string, error) {
	buf := make([]byte, tokenRandBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: crypto/rand: %w", err)
	}
	n := new(big.Int).SetBytes(buf)
	base := big.NewInt(int64(len(b62Alphabet)))
	var mod big.Int
	body := make([]byte, tokenBodyLen)
	for i := tokenBodyLen - 1; i >= 0; i-- {
		n.QuoRem(n, base, &mod)
		body[i] = b62Alphabet[mod.Int64()]
	}
	return TokenPrefix + string(body), nil
}

// Hash 明文的存储/校验形态：sha256 hex。
// tokens.json 只存这个——文件泄露 ≠ token 泄露（可进 git、进备份）。
func Hash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// DisplayPrefix 人眼识别用的短前缀（mt_ + 前 5 字符正文，共 8 字符）。
// 只用于 -token-list 对齐"这是哪个 token"，不可反推明文。
func DisplayPrefix(plain string) string {
	if len(plain) > 8 {
		return plain[:8]
	}
	return plain
}
