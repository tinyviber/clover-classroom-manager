package castview

// lockView 是 syncState 的纯决策：广播、教师锁、守护锁三组输入
// 推出窗口是否显示、是否全屏、是否锁键鼠、是否给悬浮条。
// 窗口和钩子的 Win32 调用在 Windows 文件里按这个结果执行。
type lockView struct {
	visible    bool
	fullscreen bool
	inputLock  bool
	bar        bool
}

func deriveLockView(broadcastVisible, teachLock, guardLock bool, mode string) lockView {
	locked := teachLock || guardLock
	return lockView{
		visible:    broadcastVisible || locked,
		fullscreen: locked || mode == "fullscreen",
		inputLock:  locked || (broadcastVisible && mode == "fullscreen"),
		bar:        broadcastVisible && !locked,
	}
}
