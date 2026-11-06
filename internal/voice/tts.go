package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

// ChatClient asks OpenVibe.Chat for the sound of a line, with the person's own token. The desk holds a node token for
// Chat, so the request is the person's: the voice they picked and the allowance they have.
type ChatClient struct {
	// URL is Chat's TTS route for machines (the internal route, guarded by the service token).
	URL string
	// Token returns the person's node token for Chat.
	Token func(ctx context.Context) (string, error)
	// HTTP is the client to use (nil means the default).
	HTTP *http.Client
	// FFMPEG decodes the audio to PCM (nil means "ffmpeg" on the PATH).
	FFMPEG string
}

// Speak asks Chat for the line and returns its audio as 16-bit PCM at the rate Chat used.
func (c *ChatClient) Speak(ctx context.Context, text, voiceID string) ([]int16, int, error) {
	if c.Token == nil {
		return nil, 0, errors.New("voice: no Chat token")
	}
	token, err := c.Token(ctx)
	if err != nil {
		return nil, 0, err
	}
	body, err := json.Marshal(map[string]string{"text": text, "voice": voiceID})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, 0, fmt.Errorf("voice: Chat answered %d: %s", res.StatusCode, strings.TrimSpace(string(detail)))
	}
	var payload struct {
		Audio    string `json:"audio"`
		MIMEType string `json:"mime_type"`
		Rate     int    `json:"rate"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, 0, err
	}
	raw, err := base64.StdEncoding.DecodeString(payload.Audio)
	if err != nil {
		return nil, 0, err
	}
	return decodePCM(ctx, c.FFMPEG, raw)
}

// decodePCM turns whatever Chat sent (mp3 or wav) into mono 16-bit PCM at 48 kHz, the rate the Opus track wants.
func decodePCM(ctx context.Context, ffmpeg string, raw []byte) ([]int16, int, error) {
	if len(raw) == 0 {
		return nil, 0, nil
	}
	binary := ffmpeg
	if binary == "" {
		binary = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, binary,
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-f", "s16le", "-acodec", "pcm_s16le", "-ar", "48000", "-ac", "1", "pipe:1")
	cmd.Stdin = bytes.NewReader(raw)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return nil, 0, fmt.Errorf("voice: the audio could not be decoded: %w", err)
	}
	data := out.Bytes()
	mono := make([]int16, len(data)/2)
	for i := range mono {
		mono[i] = int16(uint16(data[i*2]) | uint16(data[i*2+1])<<8)
	}
	// The Opus track is stereo, so the mono speech is laid out as interleaved stereo.
	pcm := make([]int16, len(mono)*2)
	for i, v := range mono {
		pcm[i*2] = v
		pcm[i*2+1] = v
	}
	return pcm, 48000, nil
}
