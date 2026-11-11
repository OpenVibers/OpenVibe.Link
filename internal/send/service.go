package send

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/lockeropen"
)

// maxClipboardText is what one clipboard item may carry. A copied paragraph is tiny; a whole document is not what
// clipboard sync is for.
const maxClipboardText = 64 << 10

// maxClipboardEnvelope bounds the base64 Locker envelope a clipboard item can have, with room for the header and
// padding. While auto-accept is off, the service only opens items below it, so a large file is never marked accepted
// just to learn that it is not clipboard text.
const maxClipboardEnvelope = 160 << 10

// Relay is the part of *Client the service uses, so the service can be tested without a network.
type Relay interface {
	Targets(ctx context.Context) ([]Machine, error)
	Inbox(ctx context.Context) ([]Item, error)
	Accept(ctx context.Context, id string) (string, error)
	Complete(ctx context.Context, id string) error
	Send(ctx context.Context, target, envelope string) error
}

// Direct is this machine's queue of items another of the owner's machines handed over on the local network. The
// service only opens items whose sender is one of this account's machines; a granted transfer from another account
// stays in the window for the person to answer.
type Direct interface {
	Items() []Incoming
	Accept(id string) (string, error)
	Complete(id string) error
}

type ServiceOptions struct {
	Relay      Relay
	Direct     Direct
	Key        JWK
	Principal  string
	Downloads  string
	Clipboard  Clipboard
	Settings   func() Settings
	Log        *slog.Logger
	OnActivity func(string)
	Now        func() time.Time
}

// Service is Link Send's background half: it receives items for this machine when the person turned auto-accept on,
// keeps the clipboard in step with their other machines when clipboard sync is on, and otherwise leaves everything
// in the window for a person to answer. It never sends in clear and never opens an item before deciding to keep it.
type Service struct {
	opt ServiceOptions

	mu         sync.Mutex
	handled    map[string]bool
	inspected  map[string]Settings
	failed     map[string]bool
	clipPrimed bool
	clipLast   string
	clipKnown  string
}

func NewService(opt ServiceOptions) *Service {
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Clipboard == nil {
		opt.Clipboard = SystemClipboard()
	}
	return &Service{opt: opt, handled: map[string]bool{}, inspected: map[string]Settings{}, failed: map[string]bool{}}
}

// Run polls the relay and the direct queue, and reads this machine's clipboard while the person has that switch on,
// until ctx ends. Registration and the direct listener belong to the window (internal/agent), not here.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	s.poll(ctx)
	s.syncClipboard(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.poll(ctx)
			s.syncClipboard(ctx)
		}
	}
}

func (s *Service) settings() Settings {
	if s.opt.Settings == nil {
		return DefaultSettings()
	}
	return s.opt.Settings()
}

func (s *Service) activity(text string) {
	if s.opt.OnActivity != nil && strings.TrimSpace(text) != "" {
		s.opt.OnActivity(text)
	}
}

type waiting struct {
	id        string
	sender    string
	bytes     int64
	transport string
}

func (s *Service) poll(ctx context.Context) {
	settings := s.settings()
	if !settings.AutoAccept && !settings.Clipboard {
		return
	}
	var items []waiting
	if s.opt.Relay != nil {
		relayItems, err := s.opt.Relay.Inbox(ctx)
		if err != nil {
			s.opt.Log.Debug("Link Send inbox could not be read", "err", err)
		}
		for _, item := range relayItems {
			items = append(items, waiting{id: item.ID, sender: item.SenderName, bytes: item.Bytes, transport: "relay"})
		}
	}
	var direct []Incoming
	if s.opt.Direct != nil {
		direct = s.opt.Direct.Items()
	}
	if len(direct) > 0 {
		own, err := s.ownMachines(ctx)
		if err != nil {
			s.opt.Log.Debug("Link Send could not check its own machines", "err", err)
			direct = nil
		}
		for _, item := range direct {
			if !own[item.Sender] {
				continue
			}
			items = append(items, waiting{id: item.ID, sender: item.SenderName, bytes: item.Bytes, transport: "direct"})
		}
	}
	for _, item := range items {
		if !s.shouldLook(settings, item.bytes) {
			continue
		}
		s.mu.Lock()
		done := s.handled[item.id] || s.failed[item.id]
		before, seen := s.inspected[item.id]
		s.mu.Unlock()
		if done || (seen && before == settings) {
			continue
		}
		s.handle(ctx, settings, item)
	}
}

// shouldLook says whether an item could be one this machine handles with the switches as they are. When auto-accept
// is off only a clipboard-sized item is opened, because only its kind can make it something to keep now.
func (s *Service) shouldLook(settings Settings, bytes int64) bool {
	if settings.AutoAccept {
		return true
	}
	return settings.Clipboard && bytes > 0 && bytes <= maxClipboardEnvelope
}

func (s *Service) ownMachines(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if s.opt.Relay == nil {
		return out, nil
	}
	machines, err := s.opt.Relay.Targets(ctx)
	if err != nil {
		return nil, err
	}
	for _, machine := range machines {
		out[machine.ID] = true
	}
	return out, nil
}

func (s *Service) handle(ctx context.Context, settings Settings, item waiting) {
	envelope, err := s.envelope(ctx, item)
	if err != nil {
		s.opt.Log.Debug("a waiting item could not be fetched", "id", item.id, "err", err)
		return
	}
	raw, err := lockeropen.DecodeEnvelope(envelope)
	if err != nil {
		s.fail(item, "A transfer could not be opened; ask the other machine to send it again.")
		return
	}
	plain, err := lockeropen.Open(raw, lockeropen.JWK{Kty: s.opt.Key.Kty, Crv: s.opt.Key.Crv, X: s.opt.Key.X, Y: s.opt.Key.Y, D: s.opt.Key.D},
		lockeropen.Subject{Type: "link", ID: s.opt.Principal})
	if err != nil {
		s.fail(item, "A transfer was not encrypted for this machine, so Link could not open it.")
		return
	}
	packed, err := Unpack(plain)
	if err != nil {
		s.fail(item, "A transfer did not arrive in full; ask the other machine to send it again.")
		return
	}
	switch packed.Kind {
	case KindClipboard:
		if !settings.Clipboard {
			s.inspect(item, settings)
			return
		}
		if s.opt.Clipboard == nil {
			s.inspect(item, settings)
			return
		}
		if len(packed.Data) > maxClipboardText {
			s.fail(item, "Copied text from another machine was too large to put on this clipboard.")
			return
		}
		text := string(packed.Data)
		if err := s.opt.Clipboard.Write(ctx, text); err != nil {
			s.opt.Log.Warn("copied text could not be put on the clipboard", "err", err)
			s.inspect(item, settings)
			return
		}
		s.mu.Lock()
		s.clipKnown, s.clipLast = text, text
		s.mu.Unlock()
		s.complete(ctx, item)
		s.activity("Put copied text from " + orMachine(item.sender) + " on this computer's clipboard")
	default:
		if !settings.AutoAccept {
			s.inspect(item, settings)
			return
		}
		path, err := Save(s.opt.Downloads, packed.Name, packed.Data)
		if err != nil {
			s.fail(item, err.Error())
			return
		}
		s.complete(ctx, item)
		s.activity("Saved " + baseName(path) + " from " + orMachine(item.sender) + " to Downloads")
	}
}

func (s *Service) envelope(ctx context.Context, item waiting) (string, error) {
	if item.transport == "direct" {
		if s.opt.Direct == nil {
			return "", errors.New("the direct queue is not available")
		}
		return s.opt.Direct.Accept(item.id)
	}
	if s.opt.Relay == nil {
		return "", errors.New("this machine is not paired for Link Send")
	}
	return s.opt.Relay.Accept(ctx, item.id)
}

func (s *Service) complete(ctx context.Context, item waiting) {
	var err error
	if item.transport == "direct" {
		err = s.opt.Direct.Complete(item.id)
	} else {
		err = s.opt.Relay.Complete(ctx, item.id)
	}
	if err != nil {
		// The item is already saved here; a lost completion only leaves the sender waiting until the relay expires it.
		s.opt.Log.Warn("the relay did not learn an item was saved", "id", item.id, "err", err)
	}
	s.mu.Lock()
	s.handled[item.id] = true
	delete(s.inspected, item.id)
	s.mu.Unlock()
}

// inspect remembers that this item was opened under these switches and left for the person. It is looked at again
// only after a switch changes, so a file that is waiting does not get accepted on every poll.
func (s *Service) inspect(item waiting, settings Settings) {
	s.mu.Lock()
	s.inspected[item.id] = settings
	s.mu.Unlock()
}

func (s *Service) fail(item waiting, text string) {
	s.mu.Lock()
	s.failed[item.id] = true
	s.mu.Unlock()
	s.opt.Log.Warn("a Link Send item could not be received", "id", item.id)
	s.activity(text)
}

// syncClipboard sends copied text to the person's other machines while the switch is on. Text only, and the first
// thing it sees after the switch is turned on is only remembered, never sent.
func (s *Service) syncClipboard(ctx context.Context) {
	settings := s.settings()
	if !settings.Clipboard || s.opt.Clipboard == nil || s.opt.Relay == nil {
		s.mu.Lock()
		s.clipPrimed = false
		s.mu.Unlock()
		return
	}
	text, err := s.opt.Clipboard.Read(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	if !s.clipPrimed {
		s.clipPrimed, s.clipLast = true, text
		s.mu.Unlock()
		return
	}
	changed := text != s.clipLast
	echo := text == s.clipKnown
	if changed && !echo {
		s.clipKnown = ""
	}
	s.clipLast = text
	s.mu.Unlock()
	if !changed || echo || strings.TrimSpace(text) == "" {
		return
	}
	if len(text) > maxClipboardText {
		s.activity("A copied text was larger than 64 KB, so it was not synced")
		return
	}
	machines, err := s.opt.Relay.Targets(ctx)
	if err != nil {
		s.opt.Log.Debug("Link Send could not list this account's machines", "err", err)
		return
	}
	sent := 0
	for _, machine := range machines {
		if machine.PublicKey.Kty != "EC" || machine.PublicKey.Crv != "P-256" || machine.PublicKey.X == "" || machine.PublicKey.Y == "" {
			continue
		}
		sealed, err := lockeropen.Seal(PackKind("clipboard.txt", "text/plain;charset=utf-8", KindClipboard, []byte(text)),
			lockeropen.Subject{Type: "link", ID: machine.ID},
			lockeropen.JWK{Kty: machine.PublicKey.Kty, Crv: machine.PublicKey.Crv, X: machine.PublicKey.X, Y: machine.PublicKey.Y}, lockeropen.SealOptions{})
		if err != nil {
			s.opt.Log.Debug("a clipboard item could not be sealed", "machine", machine.ID, "err", err)
			continue
		}
		if err := s.opt.Relay.Send(ctx, machine.ID, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			s.opt.Log.Debug("a clipboard item was not sent", "machine", machine.ID, "err", err)
			continue
		}
		sent++
	}
	if sent > 0 {
		s.activity("Synced this computer's clipboard to " + plural(sent, "other machine", "other machines"))
	}
}

func orMachine(name string) string {
	if strings.TrimSpace(name) == "" {
		return "your other machine"
	}
	return name
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
