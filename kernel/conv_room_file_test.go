package kernel

// the kernel's tests keep no room across runs: each test's refusals are its own
func init() { convRoomFile = func() string { return "" } }
