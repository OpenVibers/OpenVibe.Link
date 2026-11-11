package send

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/lockeropen"
)

var (
	testMachine  = "lnk_" + "01JAB2C3D4E5F6G7H8J9K0MNPQ"
	peerMachine  = "lnk_" + "01JAB2C3D4E5F6G7H8J9K0MNPR"
	otherMachine = "lnk_" + "OTHER"
)

type fakeRelay struct {
	targets    []Machine
	items      []Item
	envelopes  map[string]string
	accepted   []string
	completed  []string
	sent       []sentItem
	targetsErr error
}

type sentItem struct{ target, envelope string }

func (f *fakeRelay) Targets(context.Context) ([]Machine, error) {
	if f.targetsErr != nil {
		return nil, f.targetsErr
	}
	return f.targets, nil
}
func (f *fakeRelay) Inbox(context.Context) ([]Item, error) { return f.items, nil }
func (f *fakeRelay) Accept(_ context.Context, id string) (string, error) {
	f.accepted = append(f.accepted, id)
	envelope, ok := f.envelopes[id]
	if !ok {
		return "", errors.New("not waiting")
	}
	return envelope, nil
}
func (f *fakeRelay) Complete(_ context.Context, id string) error {
	f.completed = append(f.completed, id)
	return nil
}
func (f *fakeRelay) Send(_ context.Context, target, envelope string) error {
	f.sent = append(f.sent, sentItem{target: target, envelope: envelope})
	return nil
}

type fakeDirect struct {
	items     []Incoming
	accepted  []string
	completed []string
}

func (f *fakeDirect) Items() []Incoming { return f.items }
func (f *fakeDirect) Accept(id string) (string, error) {
	f.accepted = append(f.accepted, id)
	for _, item := range f.items {
		if item.ID == id {
			return item.Envelope, nil
		}
	}
	return "", errors.New("not waiting")
}
func (f *fakeDirect) Complete(id string) error {
	f.completed = append(f.completed, id)
	return nil
}

type fakeClipboard struct {
	text   string
	writes []string
}

func (f *fakeClipboard) Read(context.Context) (string, error) { return f.text, nil }
func (f *fakeClipboard) Write(_ context.Context, text string) error {
	f.writes = append(f.writes, text)
	return nil
}

func lockerJWK(key JWK) lockeropen.JWK {
	return lockeropen.JWK{Kty: key.Kty, Crv: key.Crv, X: key.X, Y: key.Y, D: key.D}
}

func testSealed(t *testing.T, key JWK, machine, kind, name string, data []byte) string {
	t.Helper()
	envelope, err := lockeropen.Seal(PackKind(name, "text/plain;charset=utf-8", kind, data),
		lockeropen.Subject{Type: "link", ID: machine}, lockerJWK(key), lockeropen.SealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(envelope)
}

func serviceKey(t *testing.T) JWK {
	t.Helper()
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func serviceFor(t *testing.T, key JWK, relay Relay, direct Direct, settings Settings, clipboard Clipboard) (*Service, string) {
	t.Helper()
	downloads := t.TempDir()
	service := NewService(ServiceOptions{
		Relay: relay, Direct: direct, Key: key, Principal: testMachine,
		Downloads: downloads, Clipboard: clipboard,
		Settings: func() Settings { return settings },
	})
	return service, downloads
}

func TestAutoAcceptSavesAFileWithoutTheWindow(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindFile, "report.txt", []byte("quarterly numbers"))
	relay := &fakeRelay{
		items:     []Item{{ID: "snd_0123456789abcdef0123456789abcdef", SenderName: "Laptop", Bytes: int64(len(envelope))}},
		envelopes: map[string]string{"snd_0123456789abcdef0123456789abcdef": envelope},
	}
	downloads := t.TempDir()
	service := NewService(ServiceOptions{
		Relay: relay, Key: key, Principal: testMachine, Downloads: downloads,
		Settings: func() Settings { return Settings{AutoAccept: true} },
	})
	service.poll(context.Background())
	b, err := os.ReadFile(filepath.Join(downloads, "report.txt"))
	if err != nil || string(b) != "quarterly numbers" {
		t.Fatalf("saved file = %q, %v", b, err)
	}
	if len(relay.completed) != 1 {
		t.Fatalf("completed = %v", relay.completed)
	}
	service.poll(context.Background())
	entries, _ := os.ReadDir(downloads)
	if len(entries) != 1 || len(relay.completed) != 1 {
		t.Fatalf("a second poll saved the item again: %d files, completed %v", len(entries), relay.completed)
	}
}

func TestAutoAcceptOffLeavesAFileWaiting(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindFile, "report.txt", []byte("kept for later"))
	relay := &fakeRelay{
		items:     []Item{{ID: "snd_0123456789abcdef0123456789abcdef", SenderName: "Laptop", Bytes: int64(len(envelope))}},
		envelopes: map[string]string{"snd_0123456789abcdef0123456789abcdef": envelope},
	}
	settings := Settings{}
	service, downloads := serviceFor(t, serviceKey(t), relay, nil, settings, nil)
	service.opt.Settings = func() Settings { return settings }
	service.poll(context.Background())
	if len(relay.accepted) != 0 || len(relay.completed) != 0 {
		t.Fatalf("auto-accept off accepted %v, completed %v", relay.accepted, relay.completed)
	}
	if entries, _ := os.ReadDir(downloads); len(entries) != 0 {
		t.Fatalf("auto-accept off saved %d files", len(entries))
	}
}

func TestClipboardOffDoesNotConsumeAClipboardItem(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindClipboard, "clipboard.txt", []byte("copied text"))
	relay := &fakeRelay{
		items:     []Item{{ID: "snd_0123456789abcdef0123456789abcdef", SenderName: "Laptop", Bytes: int64(len(envelope))}},
		envelopes: map[string]string{"snd_0123456789abcdef0123456789abcdef": envelope},
	}
	clip := &fakeClipboard{}
	service, _ := serviceFor(t, key, relay, nil, Settings{AutoAccept: true}, clip)
	service.poll(context.Background())
	if len(clip.writes) != 0 {
		t.Fatalf("clipboard switch off wrote %q", clip.writes)
	}
	if len(relay.completed) != 0 {
		t.Fatalf("clipboard switch off completed the item: %v", relay.completed)
	}
}

func TestClipboardOnPutsReceivedTextOnTheClipboard(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindClipboard, "clipboard.txt", []byte("from the desktop"))
	relay := &fakeRelay{
		items:     []Item{{ID: "snd_0123456789abcdef0123456789abcdef", SenderName: "Desktop", Bytes: int64(len(envelope))}},
		envelopes: map[string]string{"snd_0123456789abcdef0123456789abcdef": envelope},
	}
	clip := &fakeClipboard{}
	service, _ := serviceFor(t, key, relay, nil, Settings{AutoAccept: true, Clipboard: true}, clip)
	service.poll(context.Background())
	if len(clip.writes) != 1 || clip.writes[0] != "from the desktop" {
		t.Fatalf("clipboard writes = %v", clip.writes)
	}
	if len(relay.completed) != 1 {
		t.Fatalf("completed = %v", relay.completed)
	}
}

func TestClipboardSyncSealsCopiedTextForEachMachine(t *testing.T) {
	targetKey, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	relay := &fakeRelay{targets: []Machine{{
		ID: peerMachine, Name: "Desktop", PublicKey: Public(targetKey),
	}}}
	clip := &fakeClipboard{text: "before"}
	service, _ := serviceFor(t, serviceKey(t), relay, nil, Settings{Clipboard: true}, clip)
	service.syncClipboard(context.Background()) // prime: the first text is never sent
	clip.text = "copy this"
	service.syncClipboard(context.Background())
	if len(relay.sent) != 1 {
		t.Fatalf("sent = %v", relay.sent)
	}
	raw, err := base64.StdEncoding.DecodeString(relay.sent[0].envelope)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := lockeropen.Open(raw, lockerJWK(targetKey), lockeropen.Subject{Type: "link", ID: peerMachine})
	if err != nil {
		t.Fatal(err)
	}
	packed, err := Unpack(plain)
	if err != nil || packed.Kind != KindClipboard || string(packed.Data) != "copy this" {
		t.Fatalf("packed = %#v, %v", packed, err)
	}
}

func TestClipboardEchoFromAnotherMachineIsNotSentBack(t *testing.T) {
	relay := &fakeRelay{targets: []Machine{{
		ID: peerMachine, Name: "Desktop",
		PublicKey: JWK{Kty: "EC", Crv: "P-256", X: "x", Y: "y"},
	}}}
	clip := &fakeClipboard{text: "same"}
	service, _ := serviceFor(t, serviceKey(t), relay, nil, Settings{Clipboard: true}, clip)
	service.syncClipboard(context.Background())
	service.mu.Lock()
	service.clipKnown = "same"
	service.mu.Unlock()
	service.syncClipboard(context.Background())
	if len(relay.sent) != 0 {
		t.Fatalf("an echoed clipboard was sent back: %v", relay.sent)
	}
}

func TestDirectItemFromAnotherAccountStaysForAPerson(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindFile, "granted.txt", []byte("from someone else"))
	direct := &fakeDirect{items: []Incoming{{ID: "lan_0123456789abcdef0123456789abcdef", Sender: otherMachine, SenderName: "Jo's laptop", Envelope: envelope}}}
	relay := &fakeRelay{} // the sender is not one of this account's own machines
	service, downloads := serviceFor(t, key, relay, direct, Settings{AutoAccept: true}, nil)
	service.poll(context.Background())
	if len(direct.accepted) != 0 || len(direct.completed) != 0 {
		t.Fatalf("a granted item was auto-accepted: %v", direct.accepted)
	}
	if entries, _ := os.ReadDir(downloads); len(entries) != 0 {
		t.Fatalf("a granted item was saved: %d files", len(entries))
	}
}

func TestOwnDirectItemIsAutoAccepted(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindFile, "from-phone.txt", []byte("hello"))
	direct := &fakeDirect{items: []Incoming{{ID: "lan_0123456789abcdef0123456789abcdef", Sender: peerMachine, SenderName: "Phone", Envelope: envelope}}}
	relay := &fakeRelay{targets: []Machine{{ID: peerMachine, Name: "Phone"}}}
	service, downloads := serviceFor(t, key, relay, direct, Settings{AutoAccept: true}, nil)
	service.poll(context.Background())
	b, err := os.ReadFile(filepath.Join(downloads, "from-phone.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("saved = %q, %v", b, err)
	}
	if len(direct.completed) != 1 {
		t.Fatalf("completed = %v", direct.completed)
	}
}

func TestServiceWithoutSwitchesDoesNothing(t *testing.T) {
	relay := &fakeRelay{items: []Item{{ID: "snd_0123456789abcdef0123456789abcdef", Bytes: 10}}}
	service, _ := serviceFor(t, serviceKey(t), relay, nil, Settings{}, nil)
	service.poll(context.Background())
	if len(relay.accepted) != 0 || len(relay.completed) != 0 {
		t.Fatalf("nothing should be touched with both switches off: %v %v", relay.accepted, relay.completed)
	}
}

func TestClipboardSettingsStartOff(t *testing.T) {
	if DefaultSettings() != (Settings{}) {
		t.Fatalf("default settings = %#v, want both off", DefaultSettings())
	}
}

func TestSealedItemKeepsItsKind(t *testing.T) {
	if got := strings.TrimSpace(KindClipboard); got != "clipboard" {
		t.Fatalf("kind = %q", got)
	}
}

func TestClipboardOnlyOpensASmallItemOnceAndLeavesAFile(t *testing.T) {
	key, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envelope := testSealed(t, key, testMachine, KindFile, "tiny.txt", []byte("small file"))
	relay := &fakeRelay{
		items:     []Item{{ID: "snd_0123456789abcdef0123456789abcdef", SenderName: "Laptop", Bytes: int64(len(envelope))}},
		envelopes: map[string]string{"snd_0123456789abcdef0123456789abcdef": envelope},
	}
	settings := Settings{Clipboard: true}
	service, downloads := serviceFor(t, key, relay, nil, settings, &fakeClipboard{})
	service.opt.Settings = func() Settings { return settings }
	service.poll(context.Background())
	service.poll(context.Background())
	if len(relay.accepted) != 1 {
		t.Fatalf("a small file was accepted %d times, want once", len(relay.accepted))
	}
	if len(relay.completed) != 0 {
		t.Fatalf("auto-accept off completed %v", relay.completed)
	}
	if entries, _ := os.ReadDir(downloads); len(entries) != 0 {
		t.Fatalf("auto-accept off saved %d files", len(entries))
	}
	settings.AutoAccept = true
	service.poll(context.Background())
	if b, err := os.ReadFile(filepath.Join(downloads, "tiny.txt")); err != nil || string(b) != "small file" {
		t.Fatalf("after auto-accept was turned on, saved = %q, %v", b, err)
	}
}

func TestLargeItemIsNotOpenedWhileOnlyClipboardIsOn(t *testing.T) {
	relay := &fakeRelay{
		items:     []Item{{ID: "snd_0123456789abcdef0123456789abcdef", SenderName: "Laptop", Bytes: int64(maxClipboardEnvelope + 1)}},
		envelopes: map[string]string{"snd_0123456789abcdef0123456789abcdef": "not fetched"},
	}
	service, _ := serviceFor(t, serviceKey(t), relay, nil, Settings{Clipboard: true}, &fakeClipboard{})
	service.poll(context.Background())
	if len(relay.accepted) != 0 {
		t.Fatalf("a large item was accepted just to check its kind: %v", relay.accepted)
	}
}
