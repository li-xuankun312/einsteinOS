package lib

func WIFEXITED(status int32) bool {
	return (status & 0x7F) == 0
}

func WEXITSTATUS(status int32) int32 {
	return (status >> 8) & 0xFF
}

func WIFSIGNALED(status int32) bool {
	return (status&0x7F) != 0 && (status&0x7F) != 0x7F
}

func WTERMSIG(status int32) int32 {
	return status & 0x7F
}

func WIFSTOPPED(status int32) bool {
	return (status & 0xFF) == 0x7F
}

func WSTOPSIG(status int32) int32 {
	return (status >> 8) & 0xFF
}

func WCOREDUMP(status int32) bool {
	return (status & 0x80) != 0
}

func W_EXITCODE(retCode, sig int32) int32 {
	return (retCode << 8) | sig
}

func W_STOPCODE(sig int32) int32 {
	return (sig << 8) | 0x7F
}
