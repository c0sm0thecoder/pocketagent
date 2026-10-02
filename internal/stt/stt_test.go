package stt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad request "+r.URL.Path, 400)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil || r.FormValue("model") != "m" || filepath.Ext(hdr.Filename) != ".ogg" {
			http.Error(w, "bad form", 400)
			return
		}
		data, _ := io.ReadAll(f)
		w.Write([]byte(`{"text":" heard ` + string(data) + ` "}`))
	}))
	defer srv.Close()

	tr, err := NewHTTP(config.OptionsFrom(map[string]any{"base_url": srv.URL + "/v1/", "api_key": "k", "model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "voice.oga")
	os.WriteFile(p, []byte("bytes"), 0o600)
	got, err := tr.Transcribe(context.Background(), p)
	if err != nil || got != "heard bytes" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCommand(t *testing.T) {
	tr, _ := NewCommand(config.OptionsFrom(map[string]any{"command": []string{"sh", "-c", "echo transcript of $(basename $0)", "{file}"}}))
	got, err := tr.Transcribe(context.Background(), "/tmp/x.ogg")
	if err != nil || got != "transcript of x.ogg" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestRequiredOptions(t *testing.T) {
	if _, err := NewHTTP(config.OptionsFrom(map[string]any{"model": "m"})); err == nil {
		t.Error("http without base_url accepted")
	}
	if _, err := NewWhisperCpp(config.OptionsFrom(map[string]any{})); err == nil {
		t.Error("whisper-cpp without model accepted")
	}
	if _, err := NewHTTP(config.OptionsFrom(map[string]any{"base_url": "u", "model": "m", "voice": "v"})); err == nil {
		t.Error("unknown option accepted")
	}
}
