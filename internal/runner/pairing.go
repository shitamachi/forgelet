package runner

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// credentialFile is where a paired device keeps its credential. The desktop app
// writes nothing itself: the runner stores what the control plane issued.
const credentialFile = "credential"

func validToken(token string) bool {
	value, ok := strings.CutPrefix(token, "frr_")
	if !ok || len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func readCredential(workRoot string) string {
	data, err := os.ReadFile(filepath.Join(workRoot, credentialFile))
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(string(data))
	if !validToken(token) {
		return ""
	}
	return token
}

func writeCredential(workRoot, token string) error {
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return err
	}
	path := filepath.Join(workRoot, credentialFile)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func dropCredential(workRoot string) {
	_ = os.Remove(filepath.Join(workRoot, credentialFile))
}

// pair shows a code, waits for an administrator to approve it in the Web
// console, and returns the credential the control plane hands out once.
func pair(ctx context.Context, baseURL, workRoot string, status func(Status)) (string, error) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "device"
	}
	body, err := json.Marshal(map[string]string{
		"hostname": host, "os": runtime.GOOS, "arch": runtime.GOARCH, "version": version(),
	})
	if err != nil {
		return "", err
	}
	delay := 2 * time.Second
	for ctx.Err() == nil {
		id, code, secret, expires, err := requestPairing(ctx, baseURL, body)
		if err != nil {
			status(Status{State: "pairing", Message: "无法连接控制面，正在重试"})
			wait(ctx, delay)
			delay = min(15*time.Second, delay*2)
			continue
		}
		delay = 2 * time.Second
		status(Status{State: "pairing", Code: code, ExpiresAt: expires,
			Message: "在 Forgelet 的 Runners 页面批准该设备"})
		token, expired, err := waitForPairing(ctx, baseURL, id, secret, status, code, expires)
		switch {
		case err == nil && token != "":
			if err = writeCredential(workRoot, token); err != nil {
				return "", fmt.Errorf("保存设备凭据: %w", err)
			}
			return token, nil
		case expired:
			// The code timed out: ask for a new one.
			continue
		default:
			return "", err
		}
	}
	return "", ctx.Err()
}

func requestPairing(ctx context.Context, baseURL string, body []byte) (id, code, secret string, expires time.Time, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/runner/v1/pair", bytes.NewReader(body))
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := pairingClient.Do(request)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", "", "", time.Time{}, fmt.Errorf("pairing: HTTP %d", response.StatusCode)
	}
	var reply struct {
		ID        string    `json:"id"`
		Code      string    `json:"code"`
		Secret    string    `json:"secret"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&reply); err != nil || reply.ID == "" || reply.Secret == "" {
		return "", "", "", time.Time{}, errors.New("pairing: invalid response")
	}
	return reply.ID, reply.Code, reply.Secret, reply.ExpiresAt, nil
}

// waitForPairing polls until approval. It reports an expired code so the caller
// asks for a fresh one; any other error ends the attempt.
func waitForPairing(ctx context.Context, baseURL, id, secret string, status func(Status), code string, expires time.Time) (string, bool, error) {
	for ctx.Err() == nil {
		wait(ctx, 2*time.Second)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimRight(baseURL, "/")+"/runner/v1/pair/"+id, nil)
		if err != nil {
			return "", false, err
		}
		request.Header.Set("Authorization", "Bearer "+secret)
		response, err := pairingClient.Do(request)
		if err != nil {
			status(Status{State: "pairing", Code: code, ExpiresAt: expires, Message: "等待批准时连接中断，正在重试"})
			continue
		}
		switch response.StatusCode {
		case http.StatusAccepted:
			_ = response.Body.Close()
			status(Status{State: "pairing", Code: code, ExpiresAt: expires, Message: "等待管理员批准"})
		case http.StatusOK:
			var reply struct {
				Name  string `json:"name"`
				Token string `json:"token"`
			}
			err := json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&reply)
			_ = response.Body.Close()
			if err != nil || !validToken(reply.Token) {
				return "", false, errors.New("pairing: invalid credential")
			}
			status(Status{State: "available", Device: reply.Name, Message: "设备已批准"})
			return reply.Token, false, nil
		case http.StatusNotFound, http.StatusConflict:
			_ = response.Body.Close()
			return "", true, nil
		default:
			_ = response.Body.Close()
		}
	}
	return "", false, ctx.Err()
}

// pairingClient has no overall timeout: approval can take minutes, and each
// request is bounded by the caller's context.
var pairingClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
