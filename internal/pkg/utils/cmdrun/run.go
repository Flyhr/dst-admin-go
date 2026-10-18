// Package cmdrun 执行外部命令，并保证超时后不会残留仍在写文件的子进程。
//
// 背景：SteamCMD 在 Linux 上由 steamcmd.sh 启动，真正干活的 steamcmd 是 sh 的孙进程。
// 只用 exec.CommandContext + CombinedOutput 时，ctx 到期只会 kill 直接子进程 sh：
//   - 孙进程仍然存活，继续修改游戏目录；
//   - 孙进程继承了 stdout/stderr 管道，Wait 会一直等管道 EOF，导致“超时”实际不返回。
//
// 这里统一处理：独立进程组 + 超时杀整个进程组 + WaitDelay 兜底 + 输出只保留尾部。
package cmdrun

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	// 输出只保留尾部，避免长时间 SteamCMD 输出全部驻留内存。
	defaultOutputLimit = 512 << 10
	// 子进程退出/被杀后，最多再等这么久让 I/O 管道关闭，防止孙进程持有管道导致 Wait 永久阻塞。
	defaultWaitDelay = 10 * time.Second
)

// ErrTimeout 表示命令因超时被终止。
var ErrTimeout = errors.New("命令执行超时")

// tailBuffer 只保留最后 max 字节的输出。
type tailBuffer struct {
	max  int
	data []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return len(p), nil
	}
	if len(p) >= b.max {
		b.data = append(b.data[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - b.max; overflow > 0 {
		b.data = append(b.data[:0], b.data[overflow:]...)
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	return string(b.data)
}

// Run 在 dir 下执行 name args，timeout 为总时限（<=0 表示不限时）。
// 返回合并后的输出尾部；超时返回包装了 ErrTimeout 的错误，且会终止整个进程组。
func Run(ctx context.Context, timeout time.Duration, dir, name string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	out := newTailBuffer(defaultOutputLimit)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	// 即使有孙进程持有输出管道，Wait 也不会无限等待。
	cmd.WaitDelay = defaultWaitDelay
	configureProcessGroup(cmd)
	// 超时/取消时终止整个进程组，而不只是直接子进程。
	cmd.Cancel = func() error {
		return terminateProcess(cmd)
	}

	startedAt := time.Now()
	err := cmd.Run()
	if err == nil {
		return out.String(), nil
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out.String(), fmt.Errorf("%w（限制 %s，已执行 %s）", ErrTimeout, timeout, time.Since(startedAt).Round(time.Second))
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return out.String(), ctx.Err()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return out.String(), fmt.Errorf("进程已结束但输出管道仍未关闭（可能有残留子进程）: %w", err)
	}
	return out.String(), err
}

// Tail 返回 text 的尾部 limit 字节，便于把错误输出裁剪到合适长度。
func Tail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
}
