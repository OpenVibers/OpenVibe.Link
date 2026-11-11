package send

// Settings are the two per-machine choices Link Send needs while it runs. Both start off. The daemon keeps them in
// panel.json beside the capability switches (internal/agent), so this package only carries the snapshot and never
// writes a second settings file.
type Settings struct {
	// AutoAccept saves items from the person's own machines without asking first.
	AutoAccept bool
	// Clipboard syncs copied text with the person's other machines. Text only, and off until turned on.
	Clipboard bool
}

func DefaultSettings() Settings { return Settings{} }
