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

func TestOpenAICompatible(t *testing.T) {
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

	tr, err := New(config.Transcriber{Type: "openai", BaseURL: srv.URL + "/v1/", APIKey: "k", Model: "m"})
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
	tr, _ := New(config.Transcriber{Type: "command", Command: config.Command{"sh", "-c", "echo transcript of $(basename $0)", "{file}"}})
	got, err := tr.Transcribe(context.Background(), "/tmp/x.ogg")
	if err != nil || got != "transcript of x.ogg" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestNone(t *testing.T) {
	if tr, err := New(config.Transcriber{Type: "none"}); tr != nil || err != nil {
		t.Errorf("none: %v %v", tr, err)
	}
	if _, err := New(config.Transcriber{Type: "nope"}); err == nil {
		t.Error("unknown type accepted")
	}
}
