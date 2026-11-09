package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/mono"
	"github.com/OpenVibers/OpenVibe.Link/internal/plugins"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Link/internal/safety"
	"github.com/OpenVibers/OpenVibe.Link/internal/stream"
	"github.com/OpenVibers/OpenVibe.Link/internal/voice"
)

// The agent desk stream (Agent Streams S1, live.agent-stream@1): the core mints the stream and its wpt_ ticket on
// OpenVibe.Live with this machine's node token, asks the computer plugin for the desk's capture source, then publishes
// over WHIP. The ticket never goes to the plugin, into a log line or into status.
//
// Commands:
//
//	{"action":"start","title":…,"task":{source,ref,title,url?},"visibility"?,"allow_sites"?,"desk"?}
//	{"action":"stop"}
//	{"action":"curtain","up":true|false,"reason"?:…}
//	{"action":"status"}
type deskStreamValue struct {
	Action     string      `json:"action"`
	Title      string      `json:"title"`
	Task       stream.Task `json:"task"`
	Visibility string      `json:"visibility"`
	AllowSites []string    `json:"allow_sites"`
	Desk       string      `json:"desk"`
	Up         bool        `json:"up"`
	Reason     string      `json:"reason"`
}

// deskStreamCommand answers one desk.stream command. start creates the stream on Live and publishes; stop ends it;
// curtain blanks it; status reads it. None of them ever carries the ticket.
func (n *Agent) deskStreamCommand(c protocol.Command, entry *safety.Entry, received mono.Instant, nack func(code, msg string)) {
	var v deskStreamValue
	if err := json.Unmarshal(c.Value, &v); err != nil {
		nack(protocol.FaultBadValue, "value must be {action, …}")
		return
	}
	switch v.Action {
	case "status":
		var data []byte
		if st := n.deskStreamStatus(); st != nil {
			data, _ = json.Marshal(map[string]any{"running": true, "stream": st})
		} else {
			data, _ = json.Marshal(map[string]any{"running": false})
		}
		r := protocol.AckData{ID: c.ID, LatencyMS: mono.Since(received).Milliseconds(), Data: data}
		n.dedup.Complete(entry, r)
		n.send(r)
		return
	case "stop":
		stopped := n.stopDeskStream(false)
		p := n.mgr.Route(protocol.KindDeskStream, c.Target, "")
		if p != nil {
			// Best effort: the core's answer does not depend on the plugin.
			wait := p.Begin(n.ctx, c.ID+":stop", protocol.KindDeskStream, json.RawMessage(`{"action":"stop"}`), 10000)
			go wait()
		}
		data, _ := json.Marshal(map[string]any{"stopping": stopped})
		r := protocol.AckData{ID: c.ID, LatencyMS: mono.Since(received).Milliseconds(), Data: data}
		n.dedup.Complete(entry, r)
		n.send(r)
		return
	case "curtain":
		n.deskMu.Lock()
		a := n.deskStream
		n.deskMu.Unlock()
		if a == nil {
			nack(protocol.FaultNotReady, "no desk stream is running")
			return
		}
		reason := v.Reason
		if reason == "" {
			reason = "manual"
		}
		a.SetCurtain(v.Up, reason)
		p := n.mgr.Route(protocol.KindDeskStream, c.Target, "")
		if p != nil {
			body, _ := json.Marshal(map[string]any{"action": "curtain", "up": v.Up, "reason": reason})
			wait := p.Begin(n.ctx, c.ID, protocol.KindDeskStream, body, 10000)
			go wait()
		}
		r := protocol.AckData{ID: c.ID, LatencyMS: mono.Since(received).Milliseconds()}
		n.dedup.Complete(entry, r)
		n.send(r)
		return
	case "start":
	default:
		nack(protocol.FaultBadValue, "action is start, stop, curtain or status")
		return
	}

	st := n.latch.State()
	if st.Stopped() || st.Computer {
		nack(protocol.FaultLocalStop, "stopped on the machine: `openvibe-link resume` on this machine is required")
		return
	}
	if n.liveTokens == nil {
		nack(protocol.FaultUnsupported, "an agent stream needs this machine paired through OpenVibe.Network (pair again with --network)")
		return
	}
	if p := n.mgr.Route(protocol.KindDeskStream, c.Target, ""); p == nil {
		nack(protocol.FaultUnsupported, "the computer plugin is off: no desk to stream")
		return
	}
	if v.Visibility == "" {
		v.Visibility = "private"
	}
	if v.Title == "" {
		v.Title = "AI agent"
	}
	if v.Task.Source == "" {
		v.Task.Source = "desk"
	}
	if v.Task.Ref == "" {
		v.Task.Ref = v.Desk
		if v.Task.Ref == "" {
			v.Task.Ref = "desk"
		}
	}
	if v.Task.Title == "" {
		v.Task.Title = v.Title
	}
	ua := "openvibe-link/" + n.opt.Version
	client, err := stream.NewClient(stream.ClientOptions{BaseURL: n.opt.Config.Stream.LiveOrigin(),
		Token: n.liveTokens.Token, UserAgent: ua})
	if err != nil {
		nack(protocol.FaultBadValue, err.Error())
		return
	}
	created, err := client.Create(n.ctx, stream.CreateRequest{Title: v.Title, Task: v.Task,
		Visibility: v.Visibility, AllowSites: v.AllowSites})
	if err != nil {
		n.log.Warn("desk stream create failed", "err", err)
		nack(protocol.FaultHardware, err.Error())
		return
	}
	// The plugin is asked after Live minted the ticket: if it cannot start, the stream is ended at once.
	p := n.mgr.Route(protocol.KindDeskStream, c.Target, "")
	toPlugin, _ := json.Marshal(map[string]any{"action": "start", "desk": v.Desk, "allow_sites": v.AllowSites,
		"visibility": v.Visibility, "title": v.Title})
	go func() {
		wait := p.Begin(n.ctx, c.ID, protocol.KindDeskStream, toPlugin, 120_000)
		r := wait()
		if !r.OK {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = client.End(ctx, created.Stream.ID, created.Publish.Ticket)
			cancel()
			nack(r.FaultCode, r.Message)
			return
		}
		var got struct {
			Capture stream.Capture `json:"capture"`
			Curtain struct {
				Up     bool   `json:"up"`
				Reason string `json:"reason"`
			} `json:"curtain"`
		}
		_ = json.Unmarshal(r.Data, &got)
		if got.Capture.Kind == "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = client.End(ctx, created.Stream.ID, created.Publish.Ticket)
			cancel()
			nack(protocol.FaultHardware, "the desk plugin returned no capture source")
			return
		}
		enc := stream.DefaultEncode()
		if got.Capture.Width > 0 && got.Capture.Height > 0 {
			enc.Width, enc.Height = got.Capture.Width, got.Capture.Height
		}
		agent, err := stream.NewAgent(stream.AgentOptions{
			// S4: the desk speaks the lines Live wrote, in the person's own voice (Chat's TTS).
			Voice:  n.deskVoice(),
			Client: client, ID: created.Stream.ID, Publish: created.Publish, Capture: got.Capture, Encode: enc,
			FFmpeg: n.opt.Config.Video.FFmpeg, Log: n.log, Plugin: p.Name(),
			Snapshot: n.deskSnapshotFunc(p, v.Desk),
		})
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = client.End(ctx, created.Stream.ID, created.Publish.Ticket)
			cancel()
			nack(protocol.FaultHardware, err.Error())
			return
		}
		// The plugin starts its curtain up (fail closed) before it answers; its change event can arrive before this
		// agent exists, so take the initial state from the ack and never show the desk unblanked.
		if got.Curtain.Up {
			reason := got.Curtain.Reason
			if reason == "" {
				reason = "detector"
			}
			agent.SetCurtain(true, reason)
		}
		n.stopDeskStream(false)
		n.deskMu.Lock()
		n.deskStream = agent
		n.deskMu.Unlock()
		go func() {
			if err := agent.Run(n.ctx); err != nil {
				n.log.Warn("desk stream ended", "stream", created.Stream.ID, "err", err)
			}
		}()
		data, _ := json.Marshal(map[string]any{"streaming": true, "id": created.Stream.ID,
			"page_url": created.Stream.PageURL, "visibility": created.Stream.Visibility,
			"expires_at": created.Publish.ExpiresAt.UTC().Format(time.RFC3339), "capture": got.Capture.Kind})
		r2 := protocol.AckData{ID: c.ID, LatencyMS: mono.Since(received).Milliseconds(), Data: data}
		n.dedup.Complete(entry, r2)
		n.send(r2)
	}()
}

// stopDeskStream ends the running desk stream, if any → whether one ran. revoke is the kill switch: Live ends the WHIP
// session at once. Otherwise the stream ends and its card stays.
func (n *Agent) stopDeskStream(revoke bool) bool {
	n.deskMu.Lock()
	a := n.deskStream
	n.deskStream = nil
	n.deskMu.Unlock()
	if a == nil {
		return false
	}
	if revoke {
		a.Revoke()
	} else {
		a.Stop()
	}
	return true
}

func (n *Agent) deskStreamStatus() *stream.Status {
	n.deskMu.Lock()
	a := n.deskStream
	n.deskMu.Unlock()
	if a == nil {
		return nil
	}
	st := a.Status()
	return &st
}

// deskSnapshotFunc takes a picture for the card while nobody watches: the computer plugin's own screen command, which
// works the same for a desk's display and a browser desk's page (and never touches the owner's screen).
func (n *Agent) deskSnapshotFunc(p *plugins.Plugin, desk string) func(context.Context) ([]byte, error) {
	return func(ctx context.Context) ([]byte, error) {
		value := map[string]any{"max_width": 1280}
		if desk != "" {
			value["desk"] = desk
		}
		body, _ := json.Marshal(value)
		id := fmt.Sprintf("desk-stream-frame-%d", time.Now().UnixNano())
		r := p.Begin(ctx, id, protocol.KindScreen, body, 20_000)()
		if !r.OK {
			return nil, fmt.Errorf("the desk plugin could not take a snapshot: %s %s", r.FaultCode, r.Message)
		}
		var got struct {
			Format     string `json:"format"`
			ContentB64 string `json:"content_b64"`
		}
		if err := json.Unmarshal(r.Data, &got); err != nil || got.ContentB64 == "" {
			return nil, errors.New("the desk plugin returned no picture")
		}
		raw, err := base64.StdEncoding.DecodeString(got.ContentB64)
		if err != nil {
			return nil, errors.New("the desk plugin returned a bad picture")
		}
		if strings.EqualFold(got.Format, "png") {
			return pngToJPEG(raw)
		}
		if len(raw) > stream.MaxFrameBytes {
			return nil, fmt.Errorf("the snapshot is larger than %d bytes", stream.MaxFrameBytes)
		}
		return raw, nil
	}
}

// pngToJPEG turns a capture tool's PNG into the JPEG the frame endpoint takes.
func pngToJPEG(raw []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("the desk plugin returned a bad PNG")
	}
	// Downscale to at most 1280 wide with a simple nearest-neighbour pass; snapshots are a card, not the stream.
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > 1280 {
		h = h * 1280 / w
		w = 1280
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	if buf.Len() > stream.MaxFrameBytes {
		return nil, fmt.Errorf("the snapshot is larger than %d bytes", stream.MaxFrameBytes)
	}
	return buf.Bytes(), nil
}

// deskCurtainEvent is the computer plugin's own curtain (detectors, hotkey, Curtain now): the desk reports it to Live
// and blanks the picture. A detector that failed already raised it in the plugin.
func (n *Agent) deskCurtainEvent(plugin string, data map[string]any) {
	n.deskMu.Lock()
	a := n.deskStream
	n.deskMu.Unlock()
	if a == nil || (a.PluginName() != "" && a.PluginName() != plugin) {
		return
	}
	up, _ := data["up"].(bool)
	reason, _ := data["reason"].(string)
	if reason == "" {
		reason = "detector"
	}
	a.ObserveCurtain(up, reason)
}

// deskPluginState is the supervisor's state for the plugin that owns the desk's detectors: anything but ready is a
// lost plugin, so the curtain goes up and the audio mutes at once (fail closed).
func (n *Agent) deskPluginState(o plugins.Output) {
	a := n.currentDeskStream()
	if a == nil || a.PluginName() == "" || a.PluginName() != o.Plugin {
		return
	}
	state, _ := o.Data["state"].(string)
	if state == plugins.StateReady {
		a.PluginBack()
		return
	}
	a.PluginLost()
}

// deskPluginFault is the plugin going down between state reports (plugin_down): the same fail-closed curtain.
func (n *Agent) deskPluginFault(o plugins.Output) {
	a := n.currentDeskStream()
	if a == nil || a.PluginName() != o.Plugin {
		return
	}
	if code, _ := o.Data["fault_code"].(string); code == protocol.FaultPluginDown {
		a.PluginLost()
	}
}

func (n *Agent) currentDeskStream() *stream.Agent {
	n.deskMu.Lock()
	defer n.deskMu.Unlock()
	return n.deskStream
}

// deskVoice returns the desk's voice (Agent Streams S4): the player that asks Chat for the sound of a line with
// the person's own token and plays it into the stream's Opus track. Nil when the machine has no Chat token (the
// desk stays silent, which is the S1 behaviour).
func (n *Agent) deskVoice() *voice.Player {
	if n.chatTokens == nil {
		return nil
	}
	return voice.New(voice.Options{
		TTS: &voice.ChatClient{
			URL:   n.opt.Config.Voice.ChatSpeakURL(),
			Token: n.chatToken,
		},
		Encoder: &voice.FFmpegEncoder{Binary: n.opt.Config.Video.FFmpeg},
		Log:     n.log,
	})
}

// chatToken is the person's own node token for Chat.
func (n *Agent) chatToken(ctx context.Context) (string, error) {
	tok, err := n.chatTokens.Token(ctx)
	if err != nil {
		return "", err
	}
	return tok.Reveal(), nil
}
