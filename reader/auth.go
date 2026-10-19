package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
func login(email, password string) (credentials, error) {
	body, err := json.Marshal(map[string]string{
		"email":      email,
		"password":   base64.StdEncoding.EncodeToString([]byte(password)),
		"scene":      "IOT_APP",
		"userType":   "ECOFLOW",
		"appVersion": "4.1.2.02",
		"os":         "android",
		"osVersion":  "12",
	})
	if err != nil {
		return credentials{}, err
	}
	resp, err := http.Post(apiBaseURL+"/auth/login", "application/json", strings.NewReader(string(body)))
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

// fetchMQTTCredentials retrieves broker host/port and username/password.
func fetchMQTTCredentials(creds credentials) (mqttCredentials, error) {
	req, err := http.NewRequest(http.MethodGet,
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
func loginAndFetchMQTT(email, password string) (credentials, mqttCredentials, error) {
	creds, err := login(email, password)
	if err != nil {
		return credentials{}, mqttCredentials{}, err
	}
	mqtt, err := fetchMQTTCredentials(creds)
	if err != nil {
		return credentials{}, mqttCredentials{}, err
	}
	return creds, mqtt, nil
}

// mustEnvVars reads required configuration from the environment.
func mustEnvVars(keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	var missing []string
	for _, k := range keys {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			missing = append(missing, k)
			continue
		}
		out[k] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return out, nil
}
