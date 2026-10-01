package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func TestSpeakable(t *testing.T) {
	got := Speakable("## Done\n**Fixed** the `bug`:\n```go\nx := 1\n```\nAll good.")
	if strings.ContainsAny(got, "*#`") || strings.Contains(got, "x := 1") || !strings.Contains(got, "All good") {
		t.Errorf("got %q", got)
	}
}

func TestOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/audio/speech" || body["response_format"] != "opus" || body["input"] != "hi" || body["voice"] != "v" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte("OggS..."))
	}))
	defer srv.Close()
	sp, _ := New(config.TTS{Type: "openai", BaseURL: srv.URL, Voice: "v"})
	got, err := sp.Speak(context.Background(), "hi")
	if err != nil || string(got) != "OggS..." {
		t.Errorf("got %q, %v", got, err)
	}
}
