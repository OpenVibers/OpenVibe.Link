package voice

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

// fakeTTS is Chat, reduced to what the desk needs: it hands back a tone of the requested length.
type fakeTTS struct {
	mu       sync.Mutex
	calls    []string
	voices   []string
	block    chan struct{}
	failWith error
}

func (f *fakeTTS) Speak(ctx context.Context, text, voiceID string) ([]int16, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, text)
	f.voices = append(f.voices, voiceID)
	block := f.block
	err := f.failWith
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	if err != nil {
		return nil, 0, err
	}
	// 100 ms of a quiet tone at 48 kHz, interleaved stereo (the Opus track is stereo).
	pcm := make([]int16, 9600)
	for i := 0; i < len(pcm); i += 2 {
		v := int16(2000 * math.Sin(float64(i/2)/20))
		pcm[i] = v
		pcm[i+1] = v
	}
	return pcm, 48000, nil
}

// fakeEncoder hands back one frame per 20 ms of PCM, so the test can count the frames.
type fakeEncoder struct{}

func (fakeEncoder) Encode(pcm []int16) ([][]byte, error) {
	frames := len(pcm) / 1920
	out := make([][]byte, 0, frames)
	for i := 0; i < frames; i++ {
		out = append(out, []byte{0x01, 0x02})
	}
	return out, nil
}

func testPlayer(t *testing.T, tts TTSClient) *Player {
	t.Helper()
	return New(Options{TTS: tts, Encoder: fakeEncoder{}})
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The design: the desk reads the lines with its state poll and asks Chat for the audio with the person own token.
func TestPlayerSpeaksTheLinesItReads(t *testing.T) {
	tts := &fakeTTS{}
	p := testPlayer(t, tts)
	ctx := context.Background()
	err := p.Poll(ctx, []Line{{ID: 1, Text: "The checkout tests are green.", Voice: "gary"}}, State{}, Curtain{})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	waitFor(t, "the line to be spoken", func() bool { return len(p.Next()) > 0 })
	tts.mu.Lock()
	calls := append([]string(nil), tts.calls...)
	voices := append([]string(nil), tts.voices...)
	tts.mu.Unlock()
	if len(calls) != 1 || calls[0] != "The checkout tests are green." {
		t.Fatalf("Chat was asked %v", calls)
	}
	if len(voices) != 1 || voices[0] != "gary" {
		t.Fatalf("Chat was asked for the voice %v", voices)
	}
}

// The design: the audio track carries the voice.
func TestTheAudioTrackCarriesTheTTS(t *testing.T) {
	p := testPlayer(t, &fakeTTS{})
	_ = p.Poll(context.Background(), []Line{{ID: 1, Text: "A line."}}, State{}, Curtain{})
	frames := 0
	waitFor(t, "the frames", func() bool {
		for p.Next() != nil {
			frames++
		}
		return frames >= 4
	})
}

// The design: no line is spoken while the curtain is up for the password-field reason.
func TestNoLineWhileTheCurtainHidesAPasswordField(t *testing.T) {
	tts := &fakeTTS{}
	p := testPlayer(t, tts)
	err := p.Poll(context.Background(), []Line{{ID: 1, Text: "A secret line."}}, State{}, Curtain{Up: true, Reason: "password_field"})
	if !errors.Is(err, ErrCurtain) {
		t.Fatalf("the poll returned %v, want the curtain refusal", err)
	}
	time.Sleep(100 * time.Millisecond)
	tts.mu.Lock()
	calls := len(tts.calls)
	tts.mu.Unlock()
	if calls != 0 {
		t.Fatalf("Chat was asked %d times while the curtain was up", calls)
	}
	if p.Next() != nil {
		t.Fatal("a frame was queued while the curtain was up")
	}
}

// The design: the mute takes effect within a second.
func TestTheMuteIsEffectiveWithinASecond(t *testing.T) {
	tts := &fakeTTS{}
	p := testPlayer(t, tts)
	_ = p.Poll(context.Background(), []Line{{ID: 1, Text: "A line."}}, State{}, Curtain{})
	waitFor(t, "the line to start", func() bool { return len(tts.calls) > 0 })
	p.SetMuted(true)
	if !p.Muted() {
		t.Fatal("the player is not muted")
	}
	// The frames already queued are dropped: the mute is a kill switch.
	if p.Next() != nil {
		t.Fatal("a frame survived the mute")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p.Next() == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the voice was still audible a second after the mute")
}

// The design: the mute in the state stops the voice too (the owner kills it on the page).
func TestTheStateMuteStopsTheVoice(t *testing.T) {
	tts := &fakeTTS{}
	p := testPlayer(t, tts)
	err := p.Poll(context.Background(), []Line{{ID: 1, Text: "A line."}}, State{Muted: true}, Curtain{})
	if !errors.Is(err, ErrMuted) {
		t.Fatalf("the poll returned %v, want the mute refusal", err)
	}
	time.Sleep(100 * time.Millisecond)
	tts.mu.Lock()
	calls := len(tts.calls)
	tts.mu.Unlock()
	if calls != 0 {
		t.Fatalf("Chat was asked %d times while muted", calls)
	}
}

// The design: the next line is asked for while the previous one is still playing.
func TestTheNextLineIsAskedForWhileThePreviousPlays(t *testing.T) {
	tts := &fakeTTS{}
	p := testPlayer(t, tts)
	ctx := context.Background()
	_ = p.Poll(ctx, []Line{{ID: 1, Text: "First."}, {ID: 2, Text: "Second."}}, State{}, Curtain{})
	waitFor(t, "both lines to be asked for", func() bool {
		tts.mu.Lock()
		defer tts.mu.Unlock()
		return len(tts.calls) == 2
	})
}

// The design: the desk asks each line once, even when the poll repeats it.
func TestALineIsSpokenOnce(t *testing.T) {
	tts := &fakeTTS{}
	p := testPlayer(t, tts)
	ctx := context.Background()
	lines := []Line{{ID: 7, Text: "Once."}}
	_ = p.Poll(ctx, lines, State{}, Curtain{})
	_ = p.Poll(ctx, lines, State{}, Curtain{})
	time.Sleep(150 * time.Millisecond)
	tts.mu.Lock()
	calls := len(tts.calls)
	tts.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Chat was asked %d times for one line", calls)
	}
}
