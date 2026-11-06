package voice

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
)

// The desk's Opus track. The frames are encoded with ffmpeg (the desk already runs it for the capture) and the Ogg
// pages it writes are read here, so the release build stays pure Go.

// Encoder turns PCM frames into Opus frames.
type Encoder interface {
	// Encode returns the Opus frames of the given 48 kHz stereo PCM, in order.
	Encode(pcm []int16) ([][]byte, error)
}

// FFmpegEncoder encodes with ffmpeg's libopus.
type FFmpegEncoder struct {
	// Binary is the ffmpeg to run (the desk's own, so the codecs match).
	Binary string
}

// Encode runs ffmpeg over the PCM and returns its Opus packets.
func (e *FFmpegEncoder) Encode(pcm []int16) ([][]byte, error) {
	tool := e.Binary
	if tool == "" {
		tool = "ffmpeg"
	}
	if len(pcm) == 0 {
		return nil, nil
	}
	cmd := exec.Command(tool,
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:0",
		"-c:a", "libopus", "-b:a", "48k", "-vbr", "on", "-application", "voip",
		"-f", "ogg", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	raw := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
	}
	if _, err := stdin.Write(raw); err != nil {
		_ = cmd.Wait()
		return nil, err
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("voice: ffmpeg failed: %w", err)
	}
	return oggOpusPackets(out.Bytes())
}

// oggOpusPackets reads the Opus packets out of an Ogg Opus stream: the pages are read, the header pages (the ones
// with a granule position of zero) are skipped, and the packets are the page's segments.
func oggOpusPackets(data []byte) ([][]byte, error) {
	var packets [][]byte
	pos := 0
	for pos+27 <= len(data) {
		if string(data[pos:pos+4]) != "OggS" {
			return nil, errors.New("voice: the Opus stream is not Ogg")
		}
		granule := binary.LittleEndian.Uint64(data[pos+6 : pos+14])
		segments := int(data[pos+26])
		segStart := pos + 27
		segEnd := segStart + segments
		if segEnd > len(data) {
			return nil, errors.New("voice: the Opus stream is cut short")
		}
		table := data[segStart:segEnd]
		payload := segEnd
		var current []byte
		for _, lacing := range table {
			if payload+int(lacing) > len(data) {
				return nil, errors.New("voice: the Opus stream is cut short")
			}
			current = append(current, data[payload:payload+int(lacing)]...)
			payload += int(lacing)
			if lacing < 255 {
				// A complete packet. The header pages carry the codec identification, not audio.
				if granule > 0 {
					packets = append(packets, current)
				}
				current = nil
			}
		}
		pos = payload
	}
	return packets, nil
}
