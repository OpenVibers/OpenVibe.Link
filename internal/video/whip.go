// Package video publishes the device camera to OpenRestream over WHIP (RFC 9725) with pion/webrtc: H.264 from a test
// pattern, from a child process (rpicam-vid, ffmpeg), or from JPEG frames a plugin writes. Video runs on its own
// goroutines and drops frames rather than wait, so it never blocks control.
package video

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

// Options configure the publisher.
type Options struct {
	WHIPURL    string
	PublishKey credentials.Secret
	ICEServers []protocol.ICEServer
	Log        *slog.Logger
	HTTPClient *http.Client
	// IncludeLoopback offers loopback ICE candidates (tests on one machine).
	IncludeLoopback bool
	ConnectTimeout  time.Duration
	// Audio adds a silent Opus track (Agent Streams S1): S4's voice then needs no renegotiation. The packets are the
	// 20 ms silence frame from pion's own e2e test, sent whether or not video is flowing.
	Audio bool
	// UserAgent names the Link on the WHIP request.
	UserAgent string
}

// States reported by Publisher.State.
const (
	StateOff        = "off"
	StateConnecting = "connecting"
	StateLive       = "live"
	StateRetrying   = "retrying"
)

// Publisher keeps one WHIP session up and feeds it samples from a Source.
type Publisher struct {
	opt    Options
	log    *slog.Logger
	state  atomic.Value
	frames atomic.Int64
	lastEr atomic.Value

	mu    sync.Mutex
	track *webrtc.TrackLocalStaticSample
	needK bool
	src   Source
	pause atomic.Bool
	mute  atomic.Bool
	key   atomic.Value // credentials.Secret: the publish ticket, replaceable by renewal

	// voice holds the agent voice sink (S4): the Opus frames it hands over replace the silence frames.
	voice atomic.Pointer[voiceSinkHolder]
}

func NewPublisher(opt Options) *Publisher {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.HTTPClient == nil {
		opt.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if opt.ConnectTimeout == 0 {
		opt.ConnectTimeout = 20 * time.Second
	}
	p := &Publisher{opt: opt, log: opt.Log.With("component", "video")}
	p.state.Store(StateOff)
	p.lastEr.Store("")
	p.key.Store(opt.PublishKey)
	return p
}

// State is off, connecting, live or retrying.
func (p *Publisher) State() string { return p.state.Load().(string) }

// Frames counts video samples written to a live session.
func (p *Publisher) Frames() int64 { return p.frames.Load() }

func (p *Publisher) LastError() string { return p.lastEr.Load().(string) }

// SetPaused stops sending video samples while keeping the WHIP session and its audio track up. Snapshot mode uses it:
// Live asked for no encoding, but the stream must not look dropped.
func (p *Publisher) SetPaused(paused bool) { p.pause.Store(paused) }

// SetMuted stops the audio track's packets. The curtain calls it: a desk whose screen is blanked must not carry the
// desk's sound either (S1's track is silent, but S4's voice uses the same switch).
func (p *Publisher) SetMuted(muted bool) { p.mute.Store(muted) }

// SetPublishKey replaces the ticket a renewed stream reconnects with. It is never logged.
func (p *Publisher) SetPublishKey(k credentials.Secret) {
	if !k.IsZero() {
		p.key.Store(k)
	}
}

func (p *Publisher) publishKey() credentials.Secret { return p.key.Load().(credentials.Secret) }

// Run publishes src until ctx ends, reconnecting with backoff.
func (p *Publisher) Run(ctx context.Context, src Source) {
	p.mu.Lock()
	p.src = src
	p.mu.Unlock()
	srcCtx, cancelSrc := context.WithCancel(ctx)
	defer cancelSrc()
	go func() {
		if err := src.Run(srcCtx, p.write); err != nil && srcCtx.Err() == nil {
			p.log.Error("video source failed", "source", src.Name(), "err", err)
			p.lastEr.Store("source: " + err.Error())
		}
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := p.session(ctx)
		if ctx.Err() != nil {
			break
		}
		msg := "ended"
		if err != nil {
			msg = scrub(err.Error(), p.publishKey())
		}
		p.lastEr.Store(msg)
		p.state.Store(StateRetrying)
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		p.log.Warn("video session down; retrying", "err", msg, "in", backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	p.state.Store(StateOff)
}

// write is the source's sink: it never blocks on the network (pion queues packets and drops on congestion).
func (p *Publisher) write(s Sample) {
	p.mu.Lock()
	track, needK := p.track, p.needK
	if track != nil && needK && s.Key {
		p.needK = false
		needK = false
	}
	p.mu.Unlock()
	if track == nil || needK || p.pause.Load() {
		return
	}
	if err := track.WriteSample(media.Sample{Data: s.Data, Duration: s.Duration}); err == nil {
		p.frames.Add(1)
	}
}

// SetVoiceSink gives the publisher the agent's voice (S4): the frames it hands over are played in the audio track.
func (p *Publisher) SetVoiceSink(sink func() []byte) {
	p.voice.Store(&voiceSinkHolder{sink: sink})
}

// voiceFrame returns the next voice frame, or nil when the agent is not speaking.
func (p *Publisher) voiceFrame() []byte {
	holder := p.voice.Load()
	if holder == nil || holder.sink == nil {
		return nil
	}
	return holder.sink()
}

type voiceSinkHolder struct{ sink func() []byte }

// OpusSilence is one 20 ms silence frame (pion/webrtc e2e_test.go's silentOpusFrame: 8 kHz mono, 20 ms).
var OpusSilence = []byte{0xf8, 0xff, 0xfe}

func (p *Publisher) sendSilence(ctx context.Context, track *webrtc.TrackLocalStaticSample) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if p.mute.Load() {
				continue
			}
			// S4: while the agent speaks, its Opus frames go out instead of the silence frames; the voice is
			// mixed into the same track, so the viewers hear one stream of sound.
			if frame := p.voiceFrame(); frame != nil {
				_ = track.WriteSample(media.Sample{Data: frame, Duration: 20 * time.Millisecond})
				continue
			}
			_ = track.WriteSample(media.Sample{Data: OpusSilence, Duration: 20 * time.Millisecond})
		}
	}
}

func (p *Publisher) requestKeyframe() {
	p.mu.Lock()
	src := p.src
	p.mu.Unlock()
	if src != nil {
		src.RequestKeyframe()
	}
}

func (p *Publisher) newAPI() (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}},
		PayloadType: 102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if p.opt.Audio {
		if err := m.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
				SDPFmtpLine: "minptime=10;useinbandfec=1"},
			PayloadType: 111,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			return nil, err
		}
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	if p.opt.IncludeLoopback {
		se.SetIncludeLoopbackCandidate(true)
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)), nil
}

func (p *Publisher) session(ctx context.Context) error {
	p.state.Store(StateConnecting)
	api, err := p.newAPI()
	if err != nil {
		return err
	}
	var ice []webrtc.ICEServer
	for _, s := range p.opt.ICEServers {
		ice = append(ice, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return err
	}
	defer pc.Close()
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "openvibe-link")
	if err != nil {
		return err
	}
	tr, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return err
	}
	var audio *webrtc.TrackLocalStaticSample
	var silenceOnce sync.Once
	if p.opt.Audio {
		audio, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus,
			ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}, "audio", "openvibe-link")
		if err != nil {
			return err
		}
		if _, err := pc.AddTransceiverFromTrack(audio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
			return err
		}
	}
	go func() { // RTCP: a viewer's PLI or FIR asks for a keyframe.
		for {
			pkts, _, err := tr.Sender().ReadRTCP()
			if err != nil {
				return
			}
			for _, pk := range pkts {
				switch pk.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					p.requestKeyframe()
				}
			}
		}
	}()
	state := make(chan webrtc.PeerConnectionState, 8)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		select {
		case state <- s:
		default:
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return nil
	}
	answer, resource, err := p.post(ctx, pc.LocalDescription().SDP)
	if err != nil {
		return err
	}
	defer p.delete(resource)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return fmt.Errorf("whip answer: %w", err)
	}
	connectBy := time.NewTimer(p.opt.ConnectTimeout)
	defer connectBy.Stop()
	var disconnectedAt time.Time
	check := time.NewTicker(time.Second)
	defer check.Stop()
	defer func() {
		p.mu.Lock()
		p.track = nil
		p.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-connectBy.C:
			if p.State() != StateLive {
				return errors.New("whip: media did not connect in time")
			}
		case s := <-state:
			switch s {
			case webrtc.PeerConnectionStateConnected:
				disconnectedAt = time.Time{}
				p.mu.Lock()
				p.track, p.needK = track, true
				p.mu.Unlock()
				p.requestKeyframe()
				if audio != nil {
					silenceOnce.Do(func() { go p.sendSilence(ctx, audio) })
				}
				p.state.Store(StateLive)
				p.log.Info("video live", "whip", scrub(redactURL(p.opt.WHIPURL), p.publishKey()))
			case webrtc.PeerConnectionStateDisconnected:
				disconnectedAt = time.Now()
			case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
				return fmt.Errorf("whip: connection %s", s)
			}
		case <-check.C:
			if !disconnectedAt.IsZero() && time.Since(disconnectedAt) > 5*time.Second {
				return errors.New("whip: disconnected")
			}
		}
	}
}

func (p *Publisher) post(ctx context.Context, sdp string) (answer, resource string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.opt.WHIPURL, strings.NewReader(sdp))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/sdp")
	key := p.publishKey()
	if !key.IsZero() {
		req.Header.Set("Authorization", "Bearer "+key.Reveal())
	}
	if p.opt.UserAgent != "" {
		req.Header.Set("User-Agent", p.opt.UserAgent)
	}
	resp, err := p.opt.HTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("whip: %s", scrub(err.Error(), key))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("whip: HTTP %d %s", resp.StatusCode, scrub(string(bytes.TrimSpace(body[:min(len(body), 200)])), key))
	}
	loc := resp.Header.Get("Location")
	if loc != "" {
		if base, err := url.Parse(p.opt.WHIPURL); err == nil {
			if u, err := base.Parse(loc); err == nil {
				loc = u.String()
			}
		}
	}
	return string(body), loc, nil
}

func (p *Publisher) delete(resource string) {
	if resource == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, resource, nil)
	if err != nil {
		return
	}
	// The ticket goes only to the WHIP endpoint's own origin, whatever Location the server returned.
	key := p.publishKey()
	if !key.IsZero() && sameOrigin(resource, p.opt.WHIPURL) {
		req.Header.Set("Authorization", "Bearer "+key.Reveal())
	}
	if resp, err := p.opt.HTTPClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host)
}

func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "(invalid url)"
	}
	u.User, u.RawQuery = nil, ""
	return u.String()
}

func scrub(s string, secret credentials.Secret) string {
	if v := secret.Reveal(); v != "" {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}
