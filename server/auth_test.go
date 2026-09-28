package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func mintToken(deviceID, secret string, exp int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(deviceID + "|" + strconv.FormatInt(exp, 10)))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestVerifyConnectToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	good := mintToken("dev1", "s3cret", now.Unix()+60)
	cases := []struct {
		name  string
		token string
		dev   string
		want  bool
	}{
		{"valid", good, "dev1", true},
		{"other device", good, "dev2", false},
		{"expired", mintToken("dev1", "s3cret", now.Unix()-1), "dev1", false},
		{"wrong secret", mintToken("dev1", "other", now.Unix()+60), "dev1", false},
		{"tampered", "x" + good, "dev1", false},
		{"empty", "", "dev1", false},
		{"no signature", good[:len(good)-44], "dev1", false},
	}
	for _, c := range cases {
		if got := verifyConnectToken(c.token, c.dev, "s3cret", now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRoleMayAct(t *testing.T) {
	if roleMayAct(roleWeb, "ADVANCE") {
		t.Error("web client must not be able to send ADVANCE")
	}
	for _, a := range []string{"CREATE_TASK", "PAUSE_TASK", "RESUME_TASK", "CANCEL_TASK", "ANSWER"} {
		if !roleMayAct(roleWeb, a) {
			t.Errorf("web client should be able to send %s", a)
		}
	}
	if !roleMayAct(roleWorker, "ADVANCE") {
		t.Error("worker must be able to send ADVANCE")
	}
	if roleMayAct("", "CREATE_TASK") {
		t.Error("unknown role must not act")
	}
}
