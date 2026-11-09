package node

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/lockeropen"
	"github.com/OpenVibers/OpenVibe.Node/internal/send"
)

type blindFillRequest struct {
	Action    string `json:"action"`
	RequestID string `json:"request_id"`
	Origin    string `json:"origin"`
	Envelope  string `json:"envelope"`
}
type blindFillGrant struct {
	RequestID string `json:"request_id"`
	Origin    string `json:"origin"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Code      string `json:"code"`
	Submit    bool   `json:"submit"`
	Exp       int64  `json:"exp"`
}
type blindFillPluginRequest struct {
	Action    string `json:"action"`
	RequestID string `json:"request_id"`
	Origin    string `json:"origin"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Code      string `json:"code,omitempty"`
	Submit    bool   `json:"submit"`
}

var totpDigits = regexp.MustCompile(`^[0-9]{6,8}$`)
var exactFillRequestID = regexp.MustCompile(`^fr_[A-Za-z0-9_-]{16,64}$`)
var exactHTTPSOrigin = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?$`)

func (n *Node) prepareBlindFill(value json.RawMessage) (json.RawMessage, func(), error) {
	return n.prepareBlindFillAt(value, time.Now())
}

func (n *Node) prepareBlindFillAt(value json.RawMessage, now time.Time) (json.RawMessage, func(), error) {
	var req blindFillRequest
	if json.Unmarshal(value, &req) != nil || req.Action != "blind_fill" {
		return value, func() {}, nil
	}
	if n.opt.Creds == nil || !strings.HasPrefix(n.opt.Creds.Principal, "nod_") || !exactFillRequestID.MatchString(req.RequestID) || len(req.Envelope) > 16384 {
		return nil, func() {}, fmt.Errorf("invalid blind fill request")
	}
	wantOrigin, ok := exactHTTPOrigin(req.Origin)
	if !ok {
		return nil, func() {}, fmt.Errorf("invalid blind fill origin")
	}
	encoded, err := lockeropen.DecodeEnvelope(req.Envelope)
	if err != nil {
		return nil, func() {}, err
	}
	defer wipeSecret(encoded)
	key, err := send.LoadOrCreate(n.opt.Paths.ConfigDir)
	if err != nil {
		return nil, func() {}, fmt.Errorf("Link Send key is unavailable")
	}
	plain, err := lockeropen.Open(encoded, lockeropen.JWK{Kty: key.Kty, Crv: key.Crv, X: key.X, Y: key.Y, D: key.D}, lockeropen.Subject{Type: "node", ID: n.opt.Creds.Principal})
	if err != nil {
		return nil, func() {}, err
	}
	defer wipeSecret(plain)
	var grant blindFillGrant
	var fields map[string]json.RawMessage
	if json.Unmarshal(plain, &fields) != nil || !onlyFillFields(fields) || json.Unmarshal(plain, &grant) != nil {
		return nil, func() {}, fmt.Errorf("invalid blind fill grant")
	}
	defer func() { grant.Username, grant.Password, grant.Code = "", "", "" }()
	if !exactFillRequestID.MatchString(grant.RequestID) || grant.RequestID != req.RequestID || grant.Origin != wantOrigin || len(grant.Username) == 0 || len(grant.Username) > 4096 || len(grant.Password) == 0 || len(grant.Password) > 4096 || len(grant.Code) > 8 || (grant.Code != "" && !totpDigits.MatchString(grant.Code)) {
		return nil, func() {}, fmt.Errorf("invalid blind fill grant")
	}
	if grant.Exp <= now.UnixMilli() || grant.Exp > now.Add(60*time.Second).UnixMilli() {
		return nil, func() {}, fmt.Errorf("expired blind fill grant")
	}
	if grantOrigin, valid := exactHTTPOrigin(grant.Origin); !valid || grantOrigin != wantOrigin {
		return nil, func() {}, fmt.Errorf("blind fill origin mismatch")
	}
	pluginRequest := blindFillPluginRequest{Action: "blind_fill", RequestID: req.RequestID, Origin: wantOrigin, Username: grant.Username, Password: grant.Password, Code: grant.Code, Submit: grant.Submit}
	pluginValue, err := json.Marshal(pluginRequest)
	pluginRequest.Username, pluginRequest.Password, pluginRequest.Code = "", "", ""
	if err != nil {
		return nil, func() {}, fmt.Errorf("invalid blind fill grant")
	}
	return pluginValue, func() { wipeSecret(pluginValue) }, nil
}

func onlyFillFields(fields map[string]json.RawMessage) bool {
	if len(fields) < 6 || len(fields) > 7 {
		return false
	}
	for _, key := range []string{"request_id", "origin", "username", "password", "submit", "exp"} {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	for key := range fields {
		if key != "request_id" && key != "origin" && key != "username" && key != "password" && key != "code" && key != "submit" && key != "exp" {
			return false
		}
	}
	return true
}

func exactHTTPOrigin(raw string) (string, bool) {
	if !exactHTTPSOrigin.MatchString(raw) {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Host != strings.ToLower(u.Host) {
		return "", false
	}
	return raw, true
}

func wipeSecret(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
