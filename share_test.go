package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const host = "web-lp1.share.srv.nintendo.net"

type fakeSup struct {
	sup bool
	err error
}

func (f fakeSup) supervised(context.Context, string) (bool, error) { return f.sup, f.err }

type sent struct {
	p    packet
	body map[string]any
	hdr  map[string]string
}

func testServer(t *testing.T, verseAnswer func(p packet) (int, string)) (*server, *[]sent) {
	t.Helper()
	var log []sent
	vc := &verseClient{addr: "test"}
	vc.send = func(_ context.Context, p packet) (int, []byte, error) {
		s := sent{p: p}
		_ = json.Unmarshal([]byte(p.Payload), &s.body)
		_ = json.Unmarshal([]byte(p.Headers), &s.hdr)
		log = append(log, s)
		st, body := verseAnswer(p)
		return st, []byte(body), nil
	}
	s := &server{verse: vc, verseURL: "https://verse.openpak.org", titles: &titleCatalog{names: map[string]string{"0100F2C0115B6000": "Test Game"}, fetched: time.Now(), refresh: time.Hour},
		parental: fakeSup{}, limit: newRateLimit(2, time.Hour)}
	s.init()
	s.identity = func(_ context.Context, tok string) (*player, error) {
		switch tok {
		case "good":
			return &player{AccountID: "acc-123456789", Nickname: "Leia", PID: 77}, nil
		case "banned":
			return nil, errLocked
		case "orphan":
			return &player{}, nil
		}
		return nil, errors.New("bad token")
	}
	return s, &log
}

func okVerse(p packet) (int, string) {
	switch p.Path {
	case "/api/v1/sso/resolve":
		return 200, `{"pid":1800000042,"linked":false,"rulesAccepted":false}`
	case "/api/v1/console-share/posts":
		return 200, `{"id":"POST123"}`
	}
	return 404, `{"status":404,"errorCode":"not_found"}`
}

func do(t *testing.T, s *server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, "https://"+host+path, rd)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func capture(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestPageIsServedOnTheShareHostOnly(t *testing.T) {
	s, _ := testServer(t, okVerse)
	r := httptest.NewRequest("GET", "https://"+host+"/?app_id=0100F2C0115B6000&file_count=1&file_type=image", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "setUploadFile") || !strings.Contains(w.Body.String(), "https://localhost/share/result=") {
		t.Fatalf("page: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "https://web-lp1.znc.srv.nintendo.net/", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host: %d", w.Code)
	}
}

func TestWhoamiNamesThePlayerAndTheGame(t *testing.T) {
	s, _ := testServer(t, okVerse)
	st, out := do(t, s, "POST", "/_openpak/whoami", map[string]string{"id_token": "good", "app_id": "0100f2c0115b6000"})
	if st != 200 || out["nickname"] != "Leia" || out["game"] != "Test Game" {
		t.Fatalf("%d %v", st, out)
	}
}

func TestRefusals(t *testing.T) {
	s, _ := testServer(t, okVerse)
	for tok, want := range map[string]string{"": "no_token", "banned": "banned", "orphan": "not_linked", "forged": "unknown_console"} {
		_, out := do(t, s, "POST", "/_openpak/whoami", map[string]string{"id_token": tok})
		if out["error"] != want {
			t.Errorf("token %q: %v, want %s", tok, out, want)
		}
	}
	s.parental = fakeSup{sup: true}
	if st, out := do(t, s, "POST", "/_openpak/post", map[string]any{"id_token": "good", "image": capture(t, 1280, 720)}); st != 403 || out["error"] != "supervised" {
		t.Fatalf("supervised: %d %v", st, out)
	}
	// Checking is on and the core cannot answer: refused, never let through.
	s.parental = fakeSup{err: errors.New("down")}
	if _, out := do(t, s, "POST", "/_openpak/whoami", map[string]string{"id_token": "good"}); out["error"] != "supervised" {
		t.Fatalf("parental down: %v", out)
	}
}

func TestPostCreatesAScreenshotPostAsTheConsoleUser(t *testing.T) {
	s, log := testServer(t, okVerse)
	st, out := do(t, s, "POST", "/_openpak/post", map[string]any{
		"id_token": "good", "app_id": "0100F2C0115B6000", "message": "  hello\r\nworld\x07 ", "image": capture(t, 1280, 720), "spoiler": true,
	})
	if st != 200 || out["post_id"] != "POST123" || out["url"] != "https://verse.openpak.org/posts/POST123" {
		t.Fatalf("%d %v", st, out)
	}
	if len(*log) != 2 {
		t.Fatalf("calls: %d", len(*log))
	}
	res := (*log)[0]
	if res.p.Path != "/api/v1/sso/resolve" || res.body["accountId"] != "acc-123456789" || res.body["keepName"] != true {
		t.Fatalf("resolve: %+v", res)
	}
	p := (*log)[1]
	if p.p.Method != "POST" || p.hdr["x-trusted-pid"] != "1800000042" {
		t.Fatalf("post headers: %+v", p.hdr)
	}
	if p.body["titleId"] != "0100F2C0115B6000" || p.body["titleName"] != "Test Game" || p.body["body"] != "hello\nworld" || p.body["isSpoiler"] != true {
		t.Fatalf("post body: %v", p.body)
	}
	raw, _ := base64.StdEncoding.DecodeString(p.body["screenshot"].(string))
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "jpeg" || cfg.Width != 800 || cfg.Height != 450 {
		t.Fatalf("screenshot %s %dx%d %v", format, cfg.Width, cfg.Height, err)
	}
}

func TestPostWithoutAGameStillPosts(t *testing.T) {
	s, log := testServer(t, okVerse)
	if st, _ := do(t, s, "POST", "/_openpak/post", map[string]any{"id_token": "good", "image": capture(t, 1280, 720)}); st != 200 {
		t.Fatalf("status %d", st)
	}
	if b := (*log)[1].body; b["titleId"] != "" || b["titleName"] != "" {
		t.Fatalf("no game: %v", b)
	}
}

func TestPostRefusesWhatIsNotAScreenshot(t *testing.T) {
	s, log := testServer(t, okVerse)
	for name, img := range map[string]string{
		"square":  capture(t, 900, 900),
		"tiny":    capture(t, 320, 180),
		"huge":    capture(t, 2560, 1440),
		"not b64": "data:image/jpeg;base64,@@@",
		"png":     "data:image/png;base64,iVBORw0KGgo=",
		"text":    "hello",
	} {
		if st, out := do(t, s, "POST", "/_openpak/post", map[string]any{"id_token": "good", "image": img}); st != 400 || out["error"] != "bad_image" {
			t.Errorf("%s: %d %v", name, st, out)
		}
	}
	if st, out := do(t, s, "POST", "/_openpak/post", map[string]any{"id_token": "good", "image": capture(t, 1280, 720), "message": strings.Repeat("a", 281)}); st != 400 || out["error"] != "message_too_long" {
		t.Errorf("long: %d %v", st, out)
	}
	if len(*log) != 0 {
		t.Fatalf("OpenVerse was called for a refused post: %d", len(*log))
	}
}

func TestOpenVerseRefusalsReachThePage(t *testing.T) {
	for code, want := range map[string]string{"automod_prevented": "rules", "auth_read_only": "supervised", "auth_account_juxt_banned": "banned", "forbidden": "not_allowed"} {
		code := code
		s, _ := testServer(t, func(p packet) (int, string) {
			if p.Path == "/api/v1/console-share/posts" {
				st := 400
				if code == "forbidden" || strings.HasPrefix(code, "auth_") {
					st = 403
				}
				return st, `{"status":400,"errorCode":"` + code + `"}`
			}
			return okVerse(p)
		})
		if st, out := do(t, s, "POST", "/_openpak/post", map[string]any{"id_token": "good", "image": capture(t, 1280, 720)}); st != 403 || out["error"] != want {
			t.Errorf("%s: %d %v want %s", code, st, out, want)
		}
	}
}

func TestRateLimit(t *testing.T) {
	s, _ := testServer(t, okVerse)
	img := capture(t, 1280, 720)
	for i, want := range []int{200, 200, 429} {
		if st, _ := do(t, s, "POST", "/_openpak/post", map[string]any{"id_token": "good", "image": img}); st != want {
			t.Fatalf("post %d: %d want %d", i, st, want)
		}
	}
	l := newRateLimit(1, time.Hour)
	now := time.Now()
	if !l.allow("a", now) || l.allow("a", now.Add(time.Minute)) || !l.allow("a", now.Add(2*time.Hour)) {
		t.Fatal("window")
	}
}

func TestPacketCodec(t *testing.T) {
	b := encodePacket(packet{Path: "/api/v1/x", Method: "POST", Headers: "{}", Payload: strings.Repeat("p", 300)})
	got := map[int]string{}
	if err := fields(b, func(n int, _ uint64, d []byte) { got[n] = string(d) }); err != nil {
		t.Fatal(err)
	}
	if got[1] != "/api/v1/x" || got[2] != "POST" || got[3] != "{}" || len(got[4]) != 300 {
		t.Fatalf("%v", got)
	}
	// PacketResponse{status=403, payload="{}"}
	res := appendString(appendVarint(appendVarint(nil, 1<<3), 403), 2, "{}")
	st, payload, err := decodePacketResponse(res)
	if err != nil || st != 403 || payload != "{}" {
		t.Fatalf("%d %q %v", st, payload, err)
	}
	if _, _, err := decodePacketResponse([]byte{0x12, 0x05, 'a'}); err == nil {
		t.Fatal("truncated message accepted")
	}
}

func TestAppIDAndMessage(t *testing.T) {
	for in, want := range map[string]string{"0100f2c0115b6000": "0100F2C0115B6000", "0x0100F2C0115B6000": "0100F2C0115B6000", "0100": "", "zz00f2c0115b6000": ""} {
		if got := cleanAppID(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
	if m, ok := cleanMessage(strings.Repeat("é", 280)); !ok || len([]rune(m)) != 280 {
		t.Error("280 runes must fit")
	}
}
