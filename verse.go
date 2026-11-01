package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// OpenVerse is reached the way its own web UI reaches it: the miiverse-api internal API
// tunnelled through gRPC MiiverseInternalService.SendPacket (nn-juxtaposition
// protobufs/miiverse_internal/v2), authenticated with the server's API key. That channel is
// private to the box, which is what lets a caller name the user with x-trusted-pid.
//
// Two calls make a post:
//   - POST /api/v1/sso/resolve finds or creates the OpenVerse user of the OpenPak account
//     (keepName: a console share never renames a person; their web sign-in owns the name);
//   - POST /api/v1/console-share/posts creates the screenshot post as that user, in the game's
//     community or the Switch room, through OpenVerse's own checks (bans, read-only accounts,
//     community permissions, automod, duplicates).
//
// The two messages are four strings and an int; they are encoded by hand rather than
// generated, so this service carries no copy of the .proto.

type verseClient struct {
	addr string
	key  string

	once sync.Once
	conn *grpc.ClientConn
	err  error

	// send is the transport; tests replace it.
	send func(ctx context.Context, p packet) (int, []byte, error)
}

type packet struct {
	Path, Method, Headers, Payload string
}

type verseError struct {
	status int
	code   string
}

func (e *verseError) Error() string { return fmt.Sprintf("OpenVerse %d %s", e.status, e.code) }

func newVerseClient(addr, key string) *verseClient {
	c := &verseClient{addr: addr, key: key}
	c.send = c.grpcSend
	return c
}

type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) { return *(v.(*[]byte)), nil }
func (rawCodec) Unmarshal(d []byte, v any) error {
	*(v.(*[]byte)) = append([]byte(nil), d...)
	return nil
}
func (rawCodec) Name() string { return "proto" }

func (c *verseClient) grpcSend(ctx context.Context, p packet) (int, []byte, error) {
	if c.key == "" {
		return 0, nil, errors.New("no OpenVerse API key configured")
	}
	c.once.Do(func() {
		c.conn, c.err = grpc.NewClient(c.addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20), grpc.MaxCallSendMsgSize(16<<20)))
	})
	if c.err != nil {
		return 0, nil, c.err
	}
	req := encodePacket(p)
	var res []byte
	ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", c.key)
	if err := c.conn.Invoke(ctx, "/miiverse_internal.v2.MiiverseInternalService/SendPacket", &req, &res, grpc.ForceCodec(rawCodec{})); err != nil {
		return 0, nil, err
	}
	st, payload, err := decodePacketResponse(res)
	return st, []byte(payload), err
}

// call sends one internal API request and decodes a 2xx answer into out.
func (c *verseClient) call(ctx context.Context, method, path string, headers map[string]string, body, out any) error {
	h := map[string]string{"content-type": "application/json"}
	for k, v := range headers {
		h[k] = v
	}
	hj, _ := json.Marshal(h)
	bj, _ := json.Marshal(body)
	st, payload, err := c.send(ctx, packet{Path: path, Method: method, Headers: string(hj), Payload: string(bj)})
	if err != nil {
		return err
	}
	if st < 200 || st > 299 {
		var e struct {
			ErrorCode string `json:"errorCode"`
		}
		_ = json.Unmarshal(payload, &e)
		return &verseError{status: st, code: e.ErrorCode}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// resolve is the OpenVerse user (its pid) of an OpenPak account, created on first use.
func (c *verseClient) resolve(ctx context.Context, accountID, nickname string) (int64, error) {
	var out struct {
		PID int64 `json:"pid"`
	}
	err := c.call(ctx, "POST", "/api/v1/sso/resolve", nil, map[string]any{
		"accountId": accountID, "displayName": nickname, "wiiuPid": nil, "keepName": true,
	}, &out)
	if err != nil {
		return 0, err
	}
	if out.PID <= 0 {
		return 0, errors.New("OpenVerse resolved no pid")
	}
	return out.PID, nil
}

type shareRequest struct {
	TitleID    string // 16 hex digits
	TitleName  string
	Body       string
	Screenshot []byte // 800x450 JPEG
	Spoiler    bool
}

// postScreenshot creates the post and returns its id.
func (c *verseClient) postScreenshot(ctx context.Context, pid int64, r shareRequest) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.call(ctx, "POST", "/api/v1/console-share/posts", map[string]string{"x-trusted-pid": strconv.FormatInt(pid, 10)}, map[string]any{
		"titleId":    r.TitleID,
		"titleName":  r.TitleName,
		"body":       r.Body,
		"isSpoiler":  r.Spoiler,
		"screenshot": base64.StdEncoding.EncodeToString(r.Screenshot),
	}, &out)
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("OpenVerse returned no post id")
	}
	return out.ID, nil
}

// ---- protobuf by hand ----

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendString(b []byte, field int, s string) []byte {
	if s == "" {
		return b
	}
	b = appendVarint(b, uint64(field<<3|2))
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

func encodePacket(p packet) []byte {
	var b []byte
	b = appendString(b, 1, p.Path)
	b = appendString(b, 2, p.Method)
	b = appendString(b, 3, p.Headers)
	b = appendString(b, 4, p.Payload)
	return b
}

func readVarint(b []byte, i int) (uint64, int, error) {
	var v uint64
	for shift := 0; shift < 64; shift += 7 {
		if i >= len(b) {
			return 0, 0, errors.New("truncated varint")
		}
		c := b[i]
		i++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, i, nil
		}
	}
	return 0, 0, errors.New("varint too long")
}

// fields walks a message, calling f for each varint (wire 0) and length-delimited (wire 2)
// field. Other wire types are not used by the two messages read here.
func fields(b []byte, f func(num int, v uint64, data []byte)) error {
	for i := 0; i < len(b); {
		key, n, err := readVarint(b, i)
		if err != nil {
			return err
		}
		i = n
		num, wire := int(key>>3), key&7
		switch wire {
		case 0:
			v, n, err := readVarint(b, i)
			if err != nil {
				return err
			}
			i = n
			f(num, v, nil)
		case 2:
			l, n, err := readVarint(b, i)
			if err != nil {
				return err
			}
			if uint64(len(b)-n) < l {
				return errors.New("truncated field")
			}
			f(num, 0, b[n:n+int(l)])
			i = n + int(l)
		default:
			return fmt.Errorf("unexpected wire type %d", wire)
		}
	}
	return nil
}

func decodePacketResponse(b []byte) (int, string, error) {
	var status int
	var payload string
	err := fields(b, func(num int, v uint64, data []byte) {
		switch num {
		case 1:
			status = int(int32(v))
		case 2:
			payload = string(data)
		}
	})
	return status, payload, err
}

// ---- parental controls ----

// supervision answers whether an OpenPak account is supervised (a parent or guardian
// manages it). A supervised account cannot post.
type supervision interface {
	supervised(ctx context.Context, accountID string) (bool, error)
}

// noParental is the deployment without the parental controls core: nobody is supervised on
// the server side. The console's own parental controls still apply on the console.
type noParental struct{}

func (noParental) supervised(context.Context, string) (bool, error) { return false, nil }

// parentalCore asks the parental controls core (platforms/core/infra/parental, gRPC
// openpak.parental.v1.Parental/GetProfile): GetProfileRequest{member_id=1},
// GetProfileResponse{supervised=1}.
type parentalCore struct {
	addr, key string
	once      sync.Once
	conn      *grpc.ClientConn
	err       error
}

func newParentalFromEnv() supervision {
	addr := os.Getenv("NX_PARENTAL_ADDR")
	if addr == "" {
		return noParental{}
	}
	key := os.Getenv("NX_PARENTAL_KEY")
	if f := os.Getenv("NX_PARENTAL_KEY_FILE"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			key = string(trimNL(b))
		}
	}
	return &parentalCore{addr: addr, key: key}
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (p *parentalCore) supervised(ctx context.Context, accountID string) (bool, error) {
	p.once.Do(func() {
		p.conn, p.err = grpc.NewClient(p.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	})
	if p.err != nil {
		return false, p.err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-internal-key", p.key)
	req := appendString(nil, 1, accountID)
	var res []byte
	if err := p.conn.Invoke(ctx, "/openpak.parental.v1.Parental/GetProfile", &req, &res, grpc.ForceCodec(rawCodec{})); err != nil {
		return false, err
	}
	sup := false
	err := fields(res, func(num int, v uint64, _ []byte) {
		if num == 1 {
			sup = v != 0
		}
	})
	return sup, err
}
