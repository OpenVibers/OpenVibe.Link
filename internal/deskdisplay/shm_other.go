//go:build !linux

package deskdisplay

import (
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// shmFrame is MIT-SHM's shared picture, read on Linux only; elsewhere the screen comes over the socket.
type shmFrame struct{}

func openShm(*xgb.Conn, int, int) (*shmFrame, error) {
	return nil, errors.New("MIT-SHM is read on Linux only")
}

func (f *shmFrame) read(xproto.Window, int, int) ([]byte, error) {
	return nil, errors.New("no MIT-SHM")
}

func (f *shmFrame) close() {}
