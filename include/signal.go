package include

const (
	NSIG    = 32
	SIGHUP  = 1
	SIGINT  = 2
	SIGQUIT = 3
	SIGILL  = 4
	SIGTRAP = 5
	SIGABRT = 6
	SIGIOT  = 6
	SIGUNUSED = 7
	SIGFPE  = 8
	SIGKILL = 9
	SIGUSR1 = 10
	SIGSEGV = 11
	SIGUSR2 = 12
	SIGPIPE = 13
	SIGALRM = 14
	SIGTERM = 15
	SIGSTKFLT = 16
	SIGCHLD = 17
	SIGCONT = 18
	SIGSTOP = 19
	SIGTSTP = 20
	SIGTTIN = 21
	SIGTTOU  = 22
	SIGURG   = 23
	SIGXCPU  = 24
	SIGXFSZ  = 25
	SIGVTALRM = 26
	SIGPROF  = 27
	SIGWINCH = 28
	SIGIO    = 29
	SIGPWR   = 30
)

const (
	SA_NOCLDSTOP = 1
	SA_NOMASK    = 0x40000000
	SA_ONESHOT   = 0x80000000
)

const (
	SIG_BLOCK   = 0
	SIG_UNBLOCK = 1
	SIG_SETMASK = 2
)

type Sigaction struct {
	SaHandler     func(int)
	SaHandlerType int
	SaMask        uint32
	SaFlags       uint32
	SaRestorer    func()
}

const (
	SIG_HANDLER_DEFAULT = 0
	SIG_HANDLER_IGNORE  = 1
	SIG_HANDLER_CUSTOM  = 2
)
