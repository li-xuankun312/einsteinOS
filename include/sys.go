package include

const (
	RLIMIT_CPU    = 0
	RLIMIT_FSIZE  = 1
	RLIMIT_DATA   = 2
	RLIMIT_STACK  = 3
	RLIMIT_CORE   = 4
	RLIMIT_RSS    = 5
	RLIMIT_NPROC  = 6
	RLIMIT_NOFILE = 7
	RLIM_NLIMITS  = 8
	RLIM_INFINITY = 0x7FFFFFFF
)

type Rlimit struct {
	RlimCur int32
	RlimMax int32
}

const (
	RUSAGE_SELF     = 0
	RUSAGE_CHILDREN = -1
)

type Rusage struct {
	RuUtime    Timeval
	RuStime    Timeval
	RuMaxrss   int32
	RuIxrss    int32
	RuIdrss    int32
	RuIsrss    int32
	RuMinflt   int32
	RuMajflt   int32
	RuNswap    int32
	RuInblock  int32
	RuOublock  int32
	RuMsgsnd   int32
	RuMsgrcv   int32
	RuNsignals int32
	RuNvcsw    int32
	RuNivcsw   int32
}

type Timeval struct {
	TvSec  int32
	TvUsec int32
}

type Timezone struct {
	TzMinuteswest int32
	TzDsttime     int32
}

const (
	ITIMER_REAL    = 0
	ITIMER_VIRTUAL = 1
	ITIMER_PROF    = 2
)

type Itimerval struct {
	ItInterval Timeval
	ItValue    Timeval
}

const (
	PRIO_PROCESS = 0
	PRIO_PGRP    = 1
	PRIO_USER    = 2
)

const (
	F_OK = 0
	R_OK = 4
	W_OK = 2
	X_OK = 1
)

const (
	SEEK_SET = 0
	SEEK_CUR = 1
	SEEK_END = 2
)

const (
	O_NDELAY   = 04000
	O_NOCTTY   = 0400
	O_DIRECTORY = 0200000
)

const (
	S_IFMT   = 0xF000
	S_IFSOCK = 0xC000
	S_IFLNK  = 0xA000
	S_IFREG  = 0x8000
	S_IFBLK  = 0x6000
	S_IFDIR  = 0x4000
	S_IFCHR  = 0x2000
	S_IFIFO  = 0x1000

	S_IRWXU  = 00700
	S_IRUSR  = 00400
	S_IWUSR  = 00200
	S_IXUSR  = 00100
	S_IRWXG  = 00070
	S_IRGRP  = 00040
	S_IWGRP  = 00020
	S_IXGRP  = 00010
	S_IRWXO  = 00007
	S_IROTH  = 00004
	S_IWOTH  = 00002
	S_IXOTH  = 00001
)

const (
	CLOCK_REALTIME  = 0
	CLOCK_MONOTONIC = 1
)

type Timespec struct {
	TvSec  int32
	TvNsec int32
}
