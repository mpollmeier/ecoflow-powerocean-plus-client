package powerocean

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const apiBaseURL = "https://api.ecoflow.com"

type credentials struct {
	Token  string
	UserID string
}

type mqttCredentials struct {
	CertificateAccount  string `json:"certificateAccount"`
	CertificatePassword string `json:"certificatePassword"`
	URL                 string `json:"url"`
	Port                string `json:"port"`
	Protocol            string `json:"protocol"`
}

// login authenticates against the consumer API. The password is sent as
// base64 of its UTF-8 bytes, matching the Android app. See docs/auth.md.
func (c *Client) login(ctx context.Context) (credentials, error) {
	body, err := json.Marshal(map[string]string{
		"email":      c.cfg.Email,
		"password":   base64.StdEncoding.EncodeToString([]byte(c.cfg.Password)),
		"scene":      "IOT_APP",
		"userType":   "ECOFLOW",
		"appVersion": "4.1.2.02",
		"os":         "android",
		"osVersion":  "12",
	})
	if err != nil {
		return credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBaseURL+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return credentials{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return credentials{}, err
	}
	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
			User  struct {
				UserID string `json:"userId"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return credentials{}, fmt.Errorf("login response: %w (body %s)", err, raw)
	}
	if out.Code != "0" {
		return credentials{}, fmt.Errorf("login rejected: code=%s message=%s", out.Code, out.Message)
	}
	return credentials{Token: out.Data.Token, UserID: out.Data.User.UserID}, nil
}

// fetchMQTTCredentials retrieves broker host/port and MQTT username/password.
func (c *Client) fetchMQTTCredentials(ctx context.Context, creds credentials) (mqttCredentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/iot-auth/app/certification?userId=%s", apiBaseURL, creds.UserID), nil)
	if err != nil {
		return mqttCredentials{}, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return mqttCredentials{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return mqttCredentials{}, err
	}
	var out struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    mqttCredentials `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return mqttCredentials{}, fmt.Errorf("certification response: %w (body %s)", err, raw)
	}
	if out.Code != "0" {
		return mqttCredentials{}, fmt.Errorf("certification rejected: code=%s message=%s", out.Code, out.Message)
	}
	return out.Data, nil
}

// loginAndFetchMQTT performs the full auth flow (docs/auth.md steps 1–2).
func (c *Client) loginAndFetchMQTT(ctx context.Context) (credentials, mqttCredentials, error) {
	creds, err := c.login(ctx)
	if err != nil {
		return credentials{}, mqttCredentials{}, err
	}
	mqtt, err := c.fetchMQTTCredentials(ctx, creds)
	if err != nil {
		return credentials{}, mqttCredentials{}, err
	}
	return creds, mqtt, nil
}
