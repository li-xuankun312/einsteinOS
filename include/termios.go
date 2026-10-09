package include

type Winsize struct {
	WsRow    uint16
	WsCol    uint16
	WsXpixel uint16
	WsYpixel uint16
}

const (
	IGNBRK  = 0x0001
	BRKINT  = 0x0002
	IGNPAR  = 0x0004
	PARMRK  = 0x0008
	INPCK   = 0x0010
	ISTRIP  = 0x0020
	IXON    = 0x0400
	IXANY   = 0x0800
	IXOFF   = 0x1000
	IMAXBEL = 0x2000
)

const (
	OFILL   = 0x0040
	OFDEL   = 0x0080
	NLDLY   = 0x0100
	CRDLY   = 0x0600
	TABDLY  = 0x1800
	XTABS   = 0x1800
	BSDLY   = 0x2000
	VTDLY   = 0x4000
	FFDLY   = 0x8000
)

const (
	CSTOPB  = 0x0040
	CREAD   = 0x0080
	PARENB  = 0x0100
	PARODD  = 0x0200
	HUPCL   = 0x0400
	CLOCAL  = 0x0800
)

const (
	XCASE   = 0x0004
	ECHONL  = 0x0040
	NOFLSH  = 0x0080
	TOSTOP  = 0x0100
	ECHOPRT = 0x0400
	FLUSHO  = 0x1000
	PENDIN  = 0x4000
	IEXTEN  = 0x8000
)

const (
	VINTR    = 0
	VQUIT    = 1
	VERASE   = 2
	VKILL    = 3
	VEOF     = 4
	VSWTC    = 7
	VSTART   = 8
	VSTOP    = 9
	VSUSP    = 10
	VEOL     = 11
	VREPRINT = 12
	VDISCARD = 13
	VWERASE  = 14
	VLNEXT   = 15
	VEOL2    = 16
)
