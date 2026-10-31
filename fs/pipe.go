package fs

import (
	"errors"
	"sync"

	"google.golang.org/adk/v2/include"
)

var (
	errBrokenPipe = errors.New("broken pipe")
)

type Pipe struct {
	mu   sync.Mutex
	cond *sync.Cond

	buf  []byte
	head int
	tail int

	readers int
	writers int
}

var pipes sync.Map

func newPipe(inode *include.MInode) *Pipe {
	p := &Pipe{
		buf:     make([]byte, include.PAGE_SIZE),
		readers: 1,
		writers: 1,
	}
	p.cond = sync.NewCond(&p.mu)
	pipes.Store(inode, p)
	return p
}

func getPipe(inode *include.MInode) *Pipe {
	v, ok := pipes.Load(inode)
	if !ok {
		return nil
	}
	return v.(*Pipe)
}

func (p *Pipe) size() int {
	return (p.head - p.tail) & (include.PAGE_SIZE - 1)
}

func (p *Pipe) space() int {
	return (include.PAGE_SIZE - 1) - p.size()
}

func (p *Pipe) Read(dst []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := 0
	count := len(dst)

	for count > 0 {
		for p.size() == 0 {
			if p.writers < 1 {
				return total, nil
			}
			p.cond.Wait()
		}

		n := include.PAGE_SIZE - p.tail
		if n > count {
			n = count
		}
		if n > p.size() {
			n = p.size()
		}

		copy(dst[total:total+n], p.buf[p.tail:p.tail+n])
		p.tail = (p.tail + n) & (include.PAGE_SIZE - 1)
		total += n
		count -= n
	}

	p.cond.Broadcast()
	return total, nil
}

func (p *Pipe) Write(src []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := 0
	count := len(src)

	for count > 0 {
		for p.space() == 0 {
			if p.readers < 1 {
				include.Current.Signal |= 1 << (include.SIGPIPE - 1)
				if total > 0 {
					return total, errBrokenPipe
				}
				return 0, errBrokenPipe
			}
			p.cond.Broadcast()
			p.cond.Wait()
		}

		n := include.PAGE_SIZE - p.head
		if n > count {
			n = count
		}
		if n > p.space() {
			n = p.space()
		}

		copy(p.buf[p.head:p.head+n], src[total:total+n])
		p.head = (p.head + n) & (include.PAGE_SIZE - 1)
		total += n
		count -= n
	}

	p.cond.Broadcast()
	return total, nil
}

func ReadPipe(inode *include.MInode, buf []byte, count int) int {
	p := getPipe(inode)
	if p == nil {
		return 0
	}
	n, _ := p.Read(buf[:count])
	return n
}

func WritePipe(inode *include.MInode, buf []byte, count int) int {
	p := getPipe(inode)
	if p == nil {
		return -1
	}
	n, err := p.Write(buf[:count])
	if err != nil && n == 0 {
		return -1
	}
	return n
}

func SysPipe(fildes []int) int {
	var f [2]*include.File
	var fd [2]int

	j := 0
	for i := 0; j < 2 && i < include.NR_FILE; i++ {
		if FileTable[i].FCount == 0 {
			f[j] = &FileTable[i]
			f[j].FCount++
			j++
		}
	}
	if j == 1 {
		f[0].FCount = 0
	}
	if j < 2 {
		return -1
	}

	j = 0
	for i := 0; j < 2 && i < include.NR_OPEN; i++ {
		if include.Current.Filp[i] == nil {
			fd[j] = i
			include.Current.Filp[i] = f[j]
			j++
		}
	}
	if j == 1 {
		include.Current.Filp[fd[0]] = nil
	}
	if j < 2 {
		f[0].FCount = 0
		f[1].FCount = 0
		return -1
	}

	inode := GetPipeInode()
	if inode == nil {
		include.Current.Filp[fd[0]] = nil
		include.Current.Filp[fd[1]] = nil
		f[0].FCount = 0
		f[1].FCount = 0
		return -1
	}
	newPipe(inode)

	f[0].FInode = inode
	f[1].FInode = inode
	f[0].FPos = 0
	f[1].FPos = 0
	f[0].FMode = 1
	f[1].FMode = 2

	if len(fildes) >= 2 {
		fildes[0] = fd[0]
		fildes[1] = fd[1]
	}
	return 0
}
