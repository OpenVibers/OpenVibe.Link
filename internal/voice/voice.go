// Package voice is the desk's half of the agent's voice (Agent Streams S4, design 2026-11-06).
//
// Live writes the lines; the desk makes the sound. The desk reads the lines with its state poll, asks OpenVibe.Chat
// for the audio of a line with the person's own token, and plays it into the stream's Opus track. While the voice
// plays, the desk's own sound is ducked (the design's -12 dB), and it comes back 300 ms after the voice stops.
//
// The rules the desk keeps:
//
//   - a line is never spoken while the curtain is up for the password-field reason: the screen may be showing a
//     password, and a line would carry the work's words over it;
//   - the mute is a kill switch: it takes effect within a second, and the audio already queued is dropped;
//   - the next line is asked for while the previous one is still playing, so there is no gap between them.
package voice

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Line is one line the agent said, as the desk reads it in Live's state.
type Line struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Text        string    `json:"text"`
	Kind        string    `json:"kind"`
	Personality string    `json:"personality"`
	Voice       string    `json:"voice"`
}

// State is the voice part of Live's stream state.
type State struct {
	Mode        string `json:"mode"`
	Personality string `json:"personality"`
	Voice       string `json:"voice"`
	Muted       bool   `json:"muted"`
	Own         bool   `json:"own"`
	Paused      string `json:"paused"`
	Message     string `json:"message"`
}

// Curtain is the desk's privacy screen, as Live records it. The password-field reason is the one that silences the
// voice: a line spoken over a password field would put words where a secret is.
type Curtain struct {
	Up     bool   `json:"up"`
	Reason string `json:"reason"`
}

// ErrMuted is returned when a line was refused because the voice is muted.
var ErrMuted = errors.New("voice: the voice is muted")

// ErrCurtain is returned when a line was refused because the curtain hides a password field.
var ErrCurtain = errors.New("voice: the curtain is up for the password-field reason")

// TTSClient asks OpenVibe.Chat for the sound of a line, with the person's own token.
type TTSClient interface {
	// Speak returns the audio of the line: PCM (16-bit little-endian) at the rate the client names.
	Speak(ctx context.Context, text string, voiceID string) (pcm []int16, rate int, err error)
}

// QueueSize is how many Opus frames may wait for the publisher. A line is a few seconds long, so the queue is
// comfortably longer than one line; when it is full the oldest frame is dropped (the voice falls behind, it never
// blocks the desk).
const QueueSize = 512

// Options configure one desk voice.
type Options struct {
	TTS     TTSClient
	Encoder Encoder
	Log     *slog.Logger
	Now     func() time.Time
	Mixer   *Mixer
}

// Player speaks the lines the desk reads.
type Player struct {
	opt Options
	log *slog.Logger

	mu      sync.Mutex
	muted   bool
	lastID  int64
	pending []Line
	frames  chan []byte
	wake    chan struct{}
}

// New returns a player.
func New(o Options) *Player {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Mixer == nil {
		o.Mixer = NewMixer()
	}
	return &Player{opt: o, log: o.Log.With("component", "desk-voice"),
		frames: make(chan []byte, QueueSize), wake: make(chan struct{}, 1)}
}

// Next hands the publisher the next Opus frame of the voice, or nil when the agent is not speaking. The publisher's
// audio track calls it every 20 ms.
func (p *Player) Next() []byte {
	select {
	case frame := <-p.frames:
		return frame
	default:
		return nil
	}
}

// push queues one Opus frame. A full queue drops the oldest frame: the voice falls behind, it never blocks the desk.
func (p *Player) push(frame []byte) {
	select {
	case p.frames <- frame:
	default:
		select {
		case <-p.frames:
		default:
		}
		select {
		case p.frames <- frame:
		default:
		}
	}
}

// Muted is the kill switch's state.
func (p *Player) Muted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.muted
}

// SetMuted mutes or unmutes the voice. It takes effect at once: the audio already queued is dropped, so the voice is
// silent within a second.
func (p *Player) SetMuted(on bool) {
	p.mu.Lock()
	p.muted = on
	if on {
		p.pending = nil
		// The frames already queued are dropped: the mute is a kill switch, not a fade.
		for {
			select {
			case <-p.frames:
				continue
			default:
			}
			break
		}
	}
	p.mu.Unlock()
	if on {
		p.opt.Mixer.SetVoice(false)
	}
}

// Poll takes the lines of a state poll and queues the ones that are new. The desk keeps the last id it spoke, so a
// poll that repeats a line says it once.
func (p *Player) Poll(ctx context.Context, lines []Line, state State, curtain Curtain) error {
	p.mu.Lock()
	if state.Muted {
		p.muted = true
	}
	muted := p.muted
	var fresh []Line
	for _, line := range lines {
		if line.ID <= p.lastID {
			continue
		}
		fresh = append(fresh, line)
		p.lastID = line.ID
	}
	p.pending = append(p.pending, fresh...)
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()

	if muted {
		return ErrMuted
	}
	// The password-field reason is the one that silences the voice.
	if curtain.Up && curtain.Reason == "password_field" {
		return ErrCurtain
	}
	if len(pending) == 0 {
		return nil
	}
	// The lines are spoken in the background: the poll must not wait for the voice, and the next line is asked for
	// while the previous one is still playing.
	go p.speak(ctx, pending)
	return nil
}

// speak asks Chat for each line's audio and queues the Opus frames. The frames are queued as they are made, so the
// publisher plays the first line while the second one is being asked for.
func (p *Player) speak(ctx context.Context, lines []Line) {
	if p.opt.TTS == nil {
		p.log.Warn("desk voice: no TTS client")
		return
	}
	for _, line := range lines {
		if p.Muted() {
			return
		}
		pcm, rate, err := p.opt.TTS.Speak(ctx, line.Text, line.Voice)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Warn("desk voice line failed", "err", err)
			continue
		}
		if len(pcm) == 0 {
			continue
		}
		if p.Muted() {
			return
		}
		p.opt.Mixer.SetVoice(true)
		frames := p.opt.Mixer.Frames(pcm, rate)
		if len(frames) == 0 {
			p.opt.Mixer.SetVoice(false)
			continue
		}
		flat := make([]int16, 0, len(frames)*len(frames[0]))
		for _, frame := range frames {
			flat = append(flat, frame...)
		}
		opus, err := p.encode(flat)
		p.opt.Mixer.SetVoice(false)
		if err != nil {
			p.log.Warn("desk voice encoding failed", "err", err)
			continue
		}
		for _, frame := range opus {
			if p.Muted() {
				return
			}
			p.push(frame)
		}
	}
}

// encode turns the mixed PCM into Opus frames.
func (p *Player) encode(pcm []int16) ([][]byte, error) {
	if p.opt.Encoder == nil {
		return nil, errors.New("voice: no Opus encoder")
	}
	return p.opt.Encoder.Encode(pcm)
}
