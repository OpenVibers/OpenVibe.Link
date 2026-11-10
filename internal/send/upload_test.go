package send

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A dropped part must not restart the transfer: Link asks the relay which part it holds and continues there. The
// finished item is the exact ciphertext the sender began with.
func TestSendResumesAfterDroppedPart(t *testing.T) {
	const id = "upl_0123456789abcdef0123456789abcdef"
	envelope := strings.Repeat("QUJD", 300000) // ~1.2 MiB: two parts
	var mu sync.Mutex
	parts := map[int]string{}
	next := 0
	dropped := false
	statusReads := 0
	var finished string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/link/self/send/upload":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "bytes": len(envelope), "received": 0, "seq": 0})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/link/self/send/upload/"+id+"/"):
			var seq int
			if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/v1/link/self/send/upload/"+id+"/"), "%d", &seq); err != nil {
				t.Fatalf("bad part path %s", r.URL.Path)
			}
			var body struct {
				Chunk string `json:"chunk"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if seq == 1 && !dropped {
				dropped = true
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if seq != next {
				t.Fatalf("part %d arrived while the relay held %d", seq, next)
			}
			parts[seq] = body.Chunk
			next++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "bytes": len(envelope), "received": 1, "seq": next})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/link/self/send/upload/"+id:
			statusReads++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "bytes": len(envelope), "received": 1, "seq": next})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/link/self/send/upload/"+id+"/finish":
			var b strings.Builder
			for i := 0; i < next; i++ {
				b.WriteString(parts[i])
			}
			finished = b.String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "snd_0123456789abcdef0123456789abcdef"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{BaseURL: server.URL, Tokens: staticToken("node-token"), HTTP: server.Client()}
	if err := c.Send(context.Background(), "lnk_01JAB2C3D4E5F6G7H8J9K0MNPQ", envelope); err != nil {
		t.Fatal(err)
	}
	if !dropped || statusReads == 0 {
		t.Fatalf("the test did not drop a part (dropped=%v statusReads=%d)", dropped, statusReads)
	}
	if finished != envelope {
		t.Fatalf("the relay assembled %d bytes, want %d", len(finished), len(envelope))
	}
}
