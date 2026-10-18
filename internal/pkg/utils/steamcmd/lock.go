package steamcmd

import "sync"

// 进程内全局 SteamCMD 锁：版本查询（app_info_print）与更新/修复（app_update）互斥，
// 避免两个 SteamCMD 实例同时操作同一个客户端目录导致失败。
var mutex sync.Mutex

func Lock() {
	mutex.Lock()
}

func TryLock() bool {
	return mutex.TryLock()
}

func Unlock() {
	mutex.Unlock()
}
