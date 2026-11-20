package ui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTheWindowAnswersOnlyToItsOwnHostAndPath(t *testing.T) {
	s, url, err := New(t.TempDir()+"/log", true)
	if err != nil {
		t.Fatal(err)
	}
	s.SetApps([]string{"VLC"})
	s.Update(0, "done", "Installed 3.0.21", "")
	s.Finish(1, 0)

	res, err := http.Get(url + "state")
	if err != nil {
		t.Fatal(err)
	}
	var st State
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if len(st.Rows) != 1 || st.Rows[0].Status != "done" || !st.Finished || st.OK != 1 {
		t.Fatalf("state = %+v", st)
	}

	req, _ := http.NewRequest("GET", url+"state", nil)
	req.Host = "evil.example"
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != http.StatusNotFound {
		t.Errorf("a foreign Host got %d", res.StatusCode)
	}
	other := strings.Replace(url, "/t/", "/x/", 1)
	if res, _ := http.Get(other + "state"); res.StatusCode != http.StatusNotFound {
		t.Errorf("a wrong path got %d", res.StatusCode)
	}

	if res, _ := http.Get(url + "close"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET close got %d", res.StatusCode)
	}
	http.Post(url+"close", "text/plain", nil)
	select {
	case <-s.Closed:
	default:
		t.Error("Close did not close")
	}
}

func TestSanitizeKeepsOneShortLine(t *testing.T) {
	if got := Sanitize("a\nb\x00c"); got != "a b c" {
		t.Errorf("got %q", got)
	}
	if got := Sanitize(strings.Repeat("x", 500)); len(got) != 200 {
		t.Errorf("len %d", len(got))
	}
}

func TestTheCountIsSentUnlessDontSendIsTicked(t *testing.T) {
	s, url, err := New(t.TempDir()+"/log", false)
	if err != nil {
		t.Fatal(err)
	}
	if !s.SendReport() {
		t.Fatal("the default must be to send")
	}
	if res, _ := http.Get(url + "report?send=0"); res.StatusCode != http.StatusMethodNotAllowed || !s.SendReport() {
		t.Errorf("GET changed the choice (%d)", res.StatusCode)
	}
	http.Post(url+"report?send=0", "text/plain", nil)
	if s.SendReport() {
		t.Error("ticking Don't send must stop the count")
	}
	http.Post(url+"report?send=1", "text/plain", nil)
	if !s.SendReport() {
		t.Error("unticking must send again")
	}
}

func TestTheNoCodeWindowShowsTheMessageAndTheSetupLink(t *testing.T) {
	s, url, err := New(t.TempDir()+"/log", false)
	if err != nil {
		t.Fatal(err)
	}
	s.NoCode("This file has no app list. Get your installer from software.fileion.com/setup")
	res, _ := http.Get(url + "state")
	var st State
	json.NewDecoder(res.Body).Decode(&st)
	res.Body.Close()
	if !st.NoCode || !st.Finished || !strings.Contains(st.Note, "no app list") || len(st.Rows) != 0 {
		t.Fatalf("state = %+v", st)
	}
	page, _ := http.Get(url)
	body := new(strings.Builder)
	buf := make([]byte, 1<<16)
	n, _ := page.Body.Read(buf)
	body.Write(buf[:n])
	page.Body.Close()
	if !strings.Contains(body.String(), `href="https://software.fileion.com/setup"`) {
		t.Error("the page links to software.fileion.com/setup")
	}
}
