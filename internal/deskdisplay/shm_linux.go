//go:build linux

package deskdisplay

import (
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"
)

// shmFrame reads the screen through the MIT-SHM extension (Link v0.68): the X server writes the picture straight into
// memory this process shares with it, instead of sending 4 MB over the socket (measured 11-14 on 1280×800: 0.44 ms
// against 3.2 ms). The desk's X server and this helper run in the same sandbox, so they share its IPC namespace;
// anywhere they do not, Open keeps plain GetImage.
type shmFrame struct {
	conn *xgb.Conn
	seg  shm.Seg
	data []byte
}

func openShm(c *xgb.Conn, width, height int) (*shmFrame, error) {
	if err := shm.Init(c); err != nil {
		return nil, err
	}
	if _, err := shm.QueryVersion(c).Reply(); err != nil {
		return nil, err
	}
	size := width * height * 4
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, size, unix.IPC_CREAT|0o600)
	if err != nil {
		return nil, err
	}
	// Marked for removal at once: the segment lives while it is attached, and goes when both sides let go or die.
	defer func() { _, _ = unix.SysvShmCtl(id, unix.IPC_RMID, nil) }()
	data, err := unix.SysvShmAttach(id, 0, 0)
	if err != nil {
		return nil, err
	}
	seg, err := shm.NewSegId(c)
	if err != nil {
		_ = unix.SysvShmDetach(data)
		return nil, err
	}
	if err := shm.AttachChecked(c, seg, uint32(id), false).Check(); err != nil {
		_ = unix.SysvShmDetach(data)
		return nil, err
	}
	return &shmFrame{conn: c, seg: seg, data: data}, nil
}

// read is the whole screen in the shared segment, as X's 32-bit pixels; valid until the next read.
func (f *shmFrame) read(root xproto.Window, width, height int) ([]byte, error) {
	if _, err := shm.GetImage(f.conn, xproto.Drawable(root), 0, 0, uint16(width), uint16(height), 0xffffffff, xproto.ImageFormatZPixmap, f.seg, 0).Reply(); err != nil {
		return nil, err
	}
	if len(f.data) < width*height*4 {
		return nil, errors.New("shared segment too small")
	}
	return f.data[:width*height*4], nil
}

func (f *shmFrame) close() {
	shm.Detach(f.conn, f.seg)
	_ = unix.SysvShmDetach(f.data)
}
