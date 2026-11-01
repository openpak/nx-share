package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/openpak/nx-common/internalkey"
	"github.com/openpak/nx-common/nxhttp"
)

//go:embed page.html
var pageHTML string

func getenv(k, d string) string { return nxhttp.Getenv(k, d) }

// server holds what a post needs: who the console user is (nx-baas), whether they may post
// (the parental controls core), the game's name (the website's catalog) and OpenVerse itself.
type server struct {
	coreURL   string
	verse     *verseClient
	verseURL  string
	titles    *titleCatalog
	parental  supervision
	limit     *rateLimit
	http      *http.Client
	identity  func(ctx context.Context, idToken string) (*player, error) // nx-baas; tests replace it
	maxBody   int64
	nowFn     func() time.Time
	shareHost *regexp.Regexp
}

func newServerFromEnv() *server {
	s := &server{
		coreURL:  strings.TrimRight(getenv("NX_CORE_URL", "http://openpak-nx-baas:20070"), "/"),
		verseURL: strings.TrimRight(getenv("NX_VERSE_URL", "https://verse.openpak.org"), "/"),
		verse: newVerseClient(getenv("NX_VERSE_GRPC", "openpak-nn-juxtaposition-api:20091"),
			firstEnv("NX_VERSE_API_KEY", "PN_MIIVERSE_API_GRPC_SERVER_API_KEY", "PN_MIIVERSE_API_GRPC_SERVER_APIKEY")),
		titles:   newTitleCatalog(getenv("NX_TITLES_URL", "http://openpak-website:20010/api/v1/titles")),
		parental: newParentalFromEnv(),
		limit:    newRateLimit(atoiDefault(getenv("NX_SHARE_MAX_PER_HOUR", "10"), 10), time.Hour),
	}
	s.init()
	s.identity = s.coreIdentity
	return s
}

func (s *server) init() {
	if s.http == nil {
		s.http = &http.Client{Timeout: 10 * time.Second}
	}
	if s.maxBody == 0 {
		s.maxBody = 8 << 20
	}
	if s.nowFn == nil {
		s.nowFn = time.Now
	}
	s.shareHost = regexp.MustCompile(`^web-[a-z0-9]+\.share\.srv\.nintendo\.net$`)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func atoiDefault(v string, d int) int {
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return d
	}
	return n
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		nxhttp.WriteJSON(w, 200, map[string]any{"status": "ok", "service": "nx-share"})
		return
	}
	if !s.shareHost.MatchString(nxhttp.HostOf(r)) {
		nxhttp.Misrouted(logPrefix, w, r)
		return
	}
	switch {
	case r.URL.Path == "/_openpak/whoami" && r.Method == http.MethodPost:
		s.whoami(w, r)
	case r.URL.Path == "/_openpak/post" && r.Method == http.MethodPost:
		s.post(w, r)
	case r.URL.Path == "/favicon.ico":
		http.NotFound(w, r)
	case r.URL.Path == "/_openpak/diag" && r.Method == http.MethodPost:
		// What the page saw (the nx API it found, the files it was handed): logged, nothing kept.
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		log.Printf("%s page diag: %s", logPrefix, sanitizeLog(string(b)))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/_openpak/"):
		// The share applet's start page (/) and its settings page (/settings/). Every query
		// parameter name is logged (values only for the three known ones), so a console run
		// records exactly what the applet sends.
		q := r.URL.Query()
		names := make([]string, 0, len(q))
		for k := range q {
			names = append(names, k)
		}
		log.Printf("%s page %s app_id=%q file_count=%q file_type=%q params=%v ua=%q", logPrefix, r.URL.Path,
			q.Get("app_id"), q.Get("file_count"), q.Get("file_type"), names, r.UserAgent())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, pageHTML)
	default:
		nxhttp.Dump(logPrefix, r, nil, "!! UNHANDLED")
		http.NotFound(w, r)
	}
}

// player is the console user behind an NSA id token, as nx-baas proves it.
type player struct {
	AccountID string `json:"account_id"`
	Nickname  string `json:"nickname"`
	PID       uint64 `json:"pid"`
}

var errLocked = errors.New("account locked")

// coreIdentity asks nx-baas who an NSA id token belongs to (POST /internal/switch/identity,
// the title-server surface): it verifies the token's signature and expiry, answers 403 for a
// banned account and names the core account.
func (s *server) coreIdentity(ctx context.Context, idToken string) (*player, error) {
	body, _ := json.Marshal(map[string]string{"id_token": idToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.coreURL+"/internal/switch/identity", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(internalkey.Header, internalkey.Key())
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return nil, errLocked
	default:
		return nil, fmt.Errorf("identity: core answered %d", res.StatusCode)
	}
	var p player
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// refusal is a reason the page shows instead of the post screen.
type refusal struct {
	status int
	code   string
}

// eligible proves the token and decides whether this player may post at all.
func (s *server) eligible(ctx context.Context, idToken string) (*player, *refusal) {
	if idToken == "" {
		return nil, &refusal{401, "no_token"}
	}
	p, err := s.identity(ctx, idToken)
	switch {
	case errors.Is(err, errLocked):
		return nil, &refusal{403, "banned"}
	case err != nil:
		log.Printf("%s identity: %v", logPrefix, err)
		return nil, &refusal{401, "unknown_console"}
	case p.AccountID == "":
		// A genuine token for a user with no OpenPak account behind it.
		return nil, &refusal{403, "not_linked"}
	}
	supervised, err := s.parental.supervised(ctx, p.AccountID)
	if err != nil {
		// Checking is on and the core cannot answer: treat the account as supervised, never
		// the other way round (the rule OpenVerse's web sign-in follows too).
		log.Printf("%s parental controls lookup failed, refusing: %v", logPrefix, err)
		return nil, &refusal{403, "supervised"}
	}
	if supervised {
		return nil, &refusal{403, "supervised"}
	}
	return p, nil
}

type whoamiIn struct {
	IDToken string `json:"id_token"`
	AppID   string `json:"app_id"`
}

func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	var in whoamiIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		nxhttp.WriteJSON(w, 400, map[string]any{"error": "bad_request"})
		return
	}
	p, ref := s.eligible(r.Context(), in.IDToken)
	if ref != nil {
		log.Printf("%s whoami refused: %s", logPrefix, ref.code)
		nxhttp.WriteJSON(w, ref.status, map[string]any{"error": ref.code})
		return
	}
	log.Printf("%s whoami account=%s app_id=%s", logPrefix, tag(p.AccountID), cleanAppID(in.AppID))
	nxhttp.WriteJSON(w, 200, map[string]any{
		"nickname": p.Nickname,
		"game":     s.titles.name(r.Context(), cleanAppID(in.AppID)),
		"max":      maxMessage,
	})
}

const maxMessage = 280

type postIn struct {
	IDToken string `json:"id_token"`
	AppID   string `json:"app_id"`
	Message string `json:"message"`
	Image   string `json:"image"` // data:image/jpeg;base64,... as FileReader.readAsDataURL gives it
	Spoiler bool   `json:"spoiler"`
}

func (s *server) post(w http.ResponseWriter, r *http.Request) {
	var in postIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxBody)).Decode(&in); err != nil {
		nxhttp.WriteJSON(w, 400, map[string]any{"error": "bad_request"})
		return
	}
	p, ref := s.eligible(r.Context(), in.IDToken)
	if ref != nil {
		log.Printf("%s post refused: %s", logPrefix, ref.code)
		nxhttp.WriteJSON(w, ref.status, map[string]any{"error": ref.code})
		return
	}
	// No (or a malformed) app_id is not fatal: the post goes to the Switch room without a game.
	appID := cleanAppID(in.AppID)
	msg, ok := cleanMessage(in.Message)
	if !ok {
		nxhttp.WriteJSON(w, 400, map[string]any{"error": "message_too_long"})
		return
	}
	shot, err := screenshotFromDataURL(in.Image)
	if err != nil {
		log.Printf("%s post: image refused: %v", logPrefix, err)
		nxhttp.WriteJSON(w, 400, map[string]any{"error": "bad_image"})
		return
	}
	if !s.limit.allow(p.AccountID, s.nowFn()) {
		nxhttp.WriteJSON(w, 429, map[string]any{"error": "too_many_posts"})
		return
	}
	game := s.titles.name(r.Context(), appID)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	pid, err := s.verse.resolve(ctx, p.AccountID, p.Nickname)
	if err != nil {
		log.Printf("%s post: OpenVerse identity: %v", logPrefix, err)
		nxhttp.WriteJSON(w, 502, map[string]any{"error": "verse_unavailable"})
		return
	}
	postID, err := s.verse.postScreenshot(ctx, pid, shareRequest{TitleID: appID, TitleName: game, Body: msg, Screenshot: shot, Spoiler: in.Spoiler})
	if err != nil {
		var ve *verseError
		if errors.As(err, &ve) {
			log.Printf("%s post: OpenVerse refused account=%s: %d %s", logPrefix, tag(p.AccountID), ve.status, ve.code)
			nxhttp.WriteJSON(w, verseStatus(ve.status), map[string]any{"error": verseRefusal(ve)})
			return
		}
		log.Printf("%s post: OpenVerse: %v", logPrefix, err)
		nxhttp.WriteJSON(w, 502, map[string]any{"error": "verse_unavailable"})
		return
	}
	log.Printf("%s posted account=%s pid=%d app_id=%s post=%s message(%d)", logPrefix, tag(p.AccountID), pid, appID, postID, len([]rune(msg)))
	nxhttp.WriteJSON(w, 200, map[string]any{"post_id": postID, "url": s.verseURL + "/posts/" + postID})
}

func verseStatus(st int) int {
	if st >= 400 && st < 500 {
		return 403
	}
	return 502
}

// verseRefusal names an OpenVerse answer for the page.
func verseRefusal(ve *verseError) string {
	switch {
	case ve.code == "auth_read_only":
		return "supervised"
	case strings.HasPrefix(ve.code, "auth_account"):
		return "banned"
	case ve.code == "automod_prevented":
		return "rules"
	case ve.status == 403:
		return "not_allowed"
	}
	return "verse_refused"
}

var appIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{16}$`)

// cleanAppID is the application id in the canonical upper-case form, "" when it is not one.
func cleanAppID(v string) string {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "0x"), "0X"))
	if !appIDRe.MatchString(v) {
		return ""
	}
	return strings.ToUpper(v)
}

// cleanMessage strips control characters (keeping line breaks) and enforces the length
// OpenVerse accepts.
func cleanMessage(v string) (string, bool) {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	var b strings.Builder
	for _, c := range v {
		if c == '\n' || !unicode.IsControl(c) {
			b.WriteRune(c)
		}
	}
	out := strings.TrimSpace(b.String())
	return out, len([]rune(out)) <= maxMessage
}

// tag stands in for an account id in the log.
func tag(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6] + "…"
}

func sanitizeLog(v string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, v)
}

// rateLimit is a sliding window per account, in memory: a restart forgets it, which is fine
// for a limit meant to stop a runaway client, not a determined one (OpenVerse refuses
// duplicates on its own).
type rateLimit struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string][]time.Time
}

func newRateLimit(max int, window time.Duration) *rateLimit {
	return &rateLimit{max: max, window: window, seen: map[string][]time.Time{}}
}

func (l *rateLimit) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.seen[key] = kept
		return false
	}
	l.seen[key] = append(kept, now)
	return true
}
