package tracing

import (
	"github.com/mediocregopher/radix/v3/trace"
)

func LightstepPoolTrace() trace.PoolTrace {
	return trace.PoolTrace{
		ConnCreated:   nil,
		ConnClosed:    nil,
		DoCompleted:   nil,
		InitCompleted: nil,
	}
}
