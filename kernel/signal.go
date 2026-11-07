package kernel

import (
	. "google.golang.org/adk/v2/include"
)

func SysSgetmask() int32 {
	return Current.Blocked
}

func SysSsetmask(newmask int32) int32 {
	old := Current.Blocked
	Current.Blocked = newmask & ^(1 << (SIGKILL - 1))
	return old
}

func SysSignal(signum int32, handler func(int), restorer func()) int32 {
	if signum < 1 || signum > 32 || signum == SIGKILL {
		return -1
	}
	handlerType := SIG_HANDLER_CUSTOM
	if handler == nil {
		handlerType = SIG_HANDLER_DEFAULT
	}
	tmp := Sigaction{
		SaHandler:     handler,
		SaHandlerType: handlerType,
		SaMask:        0,
		SaFlags:       SA_ONESHOT | SA_NOMASK,
		SaRestorer:    restorer,
	}
	Current.Sigaction[signum-1] = tmp
	return 0
}

func SysSigaction(signum int32, action *Sigaction, oldaction *Sigaction) int32 {
	if signum < 1 || signum > 32 || signum == SIGKILL {
		return -1
	}

	tmp := Current.Sigaction[signum-1]

	if action != nil {
		Current.Sigaction[signum-1] = *action
	}

	if oldaction != nil {
		*oldaction = tmp
	}

	if Current.Sigaction[signum-1].SaFlags&SA_NOMASK != 0 {
		Current.Sigaction[signum-1].SaMask = 0
	} else {
		Current.Sigaction[signum-1].SaMask |= uint32(1 << (signum - 1))
	}
	return 0
}

func DoSignal(signr int32) {
	if signr < 1 || signr > 32 {
		return
	}
	sa := &Current.Sigaction[signr-1]

	if sa.SaHandlerType == SIG_HANDLER_IGNORE {
		return
	}

	if sa.SaHandlerType == SIG_HANDLER_DEFAULT {
		if signr == SIGCHLD {
			return
		}
		DoExit(1 << (signr - 1))
		return
	}

	if sa.SaFlags&SA_ONESHOT != 0 {
		sa.SaHandler = nil
		sa.SaHandlerType = SIG_HANDLER_DEFAULT
	}

	if sa.SaHandler != nil {
		sa.SaHandler(int(signr))
	}

	Current.Blocked |= int32(sa.SaMask)
}
