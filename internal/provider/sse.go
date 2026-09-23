package provider

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// sse 从上游 body 逐块产出 SSE data 行（已剥 "data:" 前缀、去尾部空白）。
// 两种格式的 SSE 线格式一致（event: X\ndata: {...}\n\n），data 行即载荷。
// 上游提前断流 → 读满 buf 得短读/EOF 时 next() 结束并记 err。
type sse struct {
	sc   *bufio.Scanner
	payload []byte
	terr error // 非 EOF 的读错误（断流判据）
}

func newSSE(r io.Reader) *sse {
	s := &sse{sc: bufio.NewScanner(r)}
	// 单行可能很长（大 tool_use 输入），放宽到 1MB。
	s.sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return s
}

// next 读下一条 data 行；返回 false = 流结束（正常 EOF 或断流）。
func (s *sse) next() bool {
	if !s.sc.Scan() {
		if s.sc.Err() == nil {
			s.terr = io.EOF // 正常结束
		} else {
			s.terr = s.sc.Err() // 断流（上游中途关闭）
		}
		// 处理跨包未完成的最后一行（无结尾换行就 EOF 的情况）：不产出，只收尾。
		return false
	}
	line := bytes.TrimSpace(s.sc.Bytes())
	if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte(":")) {
		// 空行 / event: / 注释行，载荷都在 data: 行。
		return s.next()
	}
	if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		s.payload = bytes.TrimSpace(rest)
		return true
	}
	// 非 data 行（未知格式）跳过，不当载荷。
	return s.next()
}

// data 返回当前载荷（next()==true 之后有效）。
func (s *sse) data() []byte { return s.payload }

// err 返回读错误：nil=正常结束，io.EOF=正常，其它=上游断流。
func (s *sse) err() error {
	if s.terr == io.EOF {
		return nil
	}
	return s.terr
}

// 字符串辅助：部分调用方拿到的是字符串（如客户端侧拼接）。
func TrimDataPrefix(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
	return strings.TrimSpace(rest), ok
}
