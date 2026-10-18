// Package maintenance 记录“游戏更新/修复正在执行”的全局维护状态。
//
// 更新/修复期间 SteamCMD 正在修改游戏目录，此时必须禁止启动世界；
// 该状态是进程内全局的，可覆盖页面刷新、多标签、定时任务等无法共享前端内存的场景。
package maintenance

import (
	"sync"
	"time"
)

// Snapshot 维护状态快照。
type Snapshot struct {
	Active         bool
	Kind           string
	StartedAt      int64
	LastFinishedAt int64
	LastSuccess    bool
	LastRestarted  bool
}

var (
	mu           sync.Mutex
	active       bool
	kind         string
	startedAt    time.Time
	lastFinished time.Time
	lastSuccess  bool
	lastRestart  bool
)

// TryBegin 尝试进入维护状态；已有更新/修复任务在执行时返回 false。
func TryBegin(taskKind string) bool {
	mu.Lock()
	defer mu.Unlock()
	if active {
		return false
	}
	active = true
	kind = taskKind
	startedAt = time.Now()
	return true
}

// Finish 退出维护状态，并记录本次结果（success 任务成功；restarted 成功后世界已重新启动）。
func Finish(success, restarted bool) {
	mu.Lock()
	defer mu.Unlock()
	active = false
	kind = ""
	lastFinished = time.Now()
	lastSuccess = success
	lastRestart = restarted
}

// Current 返回当前维护状态快照。
func Current() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	snapshot := Snapshot{
		Active:        active,
		Kind:          kind,
		LastSuccess:   lastSuccess,
		LastRestarted: lastRestart,
	}
	if !startedAt.IsZero() {
		snapshot.StartedAt = startedAt.Unix()
	}
	if !lastFinished.IsZero() {
		snapshot.LastFinishedAt = lastFinished.Unix()
	}
	return snapshot
}
