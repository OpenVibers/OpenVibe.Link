//go:build linux && (amd64 || arm64)

package deskdisplay

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// libjpeg-turbo's TurboJPEG API, loaded at run time with purego (Link v0.68): the C library with hand-written SIMD
// encodes a desktop frame about eight times faster than image/jpeg (measured 11-14 on a 1280×800 frame: 2.9 ms against
// 21.5 ms, and it reads X's BGRX as it is). The Link stays one static binary built without cgo: where the library is
// not installed (a desk host's installer adds libturbojpeg0), the Go encoder runs as before.

const (
	tjpfBGRX    = 3    // TJPF_BGRX: X's 32-bit pixels, blue first
	tjsamp420   = 2    // TJSAMP_420
	tjflagFast  = 2048 // TJFLAG_FASTDCT
	turboLibEnv = "OPENVIBE_TURBOJPEG"
)

// turboNames are where the library is looked for, in order (OPENVIBE_TURBOJPEG first: a path, for tests).
var turboNames = []string{"libturbojpeg.so.0", "libturbojpeg.so", "/usr/lib/x86_64-linux-gnu/libturbojpeg.so.0", "/usr/lib/aarch64-linux-gnu/libturbojpeg.so.0"}

type turbo struct {
	mu       sync.Mutex
	handle   uintptr
	compress func(handle uintptr, src unsafe.Pointer, width, pitch, height, format int32, out *unsafe.Pointer, size *uint64, subsamp, quality, flags int32) int32
	free     func(p unsafe.Pointer)
}

var (
	turboOnce sync.Once
	turboLib  *turbo
	turboErr  error
)

// loadTurbo opens the library once; nil and the reason when it is not there.
func loadTurbo() (*turbo, error) {
	turboOnce.Do(func() {
		names := turboNames
		if p := os.Getenv(turboLibEnv); p != "" {
			names = append([]string{p}, names...)
		}
		var lib uintptr
		var err error
		for _, n := range names {
			if lib, err = purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_LOCAL); err == nil {
				break
			}
		}
		if lib == 0 {
			turboErr = errors.New("libturbojpeg is not installed")
			return
		}
		t := &turbo{}
		var initCompress func() uintptr
		purego.RegisterLibFunc(&initCompress, lib, "tjInitCompress")
		purego.RegisterLibFunc(&t.compress, lib, "tjCompress2")
		purego.RegisterLibFunc(&t.free, lib, "tjFree")
		if t.handle = initCompress(); t.handle == 0 {
			turboErr = errors.New("tjInitCompress failed")
			return
		}
		turboLib = t
	})
	return turboLib, turboErr
}

// encode is one BGRX frame (w×h, pitch bytes a row) as a JPEG at quality.
func (t *turbo) encode(bgrx []byte, w, h, pitch, quality int) ([]byte, error) {
	if len(bgrx) < pitch*h {
		return nil, errors.New("frame too short")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out unsafe.Pointer
	var size uint64
	if t.compress(t.handle, unsafe.Pointer(&bgrx[0]), int32(w), int32(pitch), int32(h), tjpfBGRX, &out, &size, tjsamp420, int32(quality), tjflagFast) != 0 || out == nil {
		if out != nil {
			t.free(out)
		}
		return nil, errors.New("tjCompress2 failed")
	}
	pic := make([]byte, size)
	copy(pic, unsafe.Slice((*byte)(out), size))
	t.free(out)
	runtime.KeepAlive(bgrx)
	return pic, nil
}

// TurboJPEG says whether the fast encoder is in use here, and why not.
func TurboJPEG() (bool, string) {
	t, err := loadTurbo()
	if t == nil {
		return false, err.Error()
	}
	return true, "libturbojpeg"
}
