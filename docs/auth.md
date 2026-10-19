# EcoFlow cloud auth (consumer login → MQTT credentials)

Verified against API host `https://api.ecoflow.com` with a real account (see
`reader/auth.go`, which implements this flow and connects successfully).

## 1. Login

```
POST https://api.ecoflow.com/auth/login
Content-Type: application/json
```

```json
{
  "email": "<email>",
  "password": "<base64(UTF-8 bytes of password)>",
  "scene": "IOT_APP",
  "userType": "ECOFLOW",
  "appVersion": "4.1.2.02",
  "os": "android",
  "osVersion": "12"
}
```

- Password is **base64 of the UTF-8 bytes** of the password string
  (app-side equivalent: `Base64.encodeToString(password.getBytes(UTF_8))`).
  Non-ASCII characters (`ß` etc.) are encoded as their UTF-8 bytes first.
- `scene`/`userType` are fixed for the consumer app path. `appVersion`/`os`/`osVersion`
  are sent by the app; the values above work.

Response (subset):

```json
{
  "code": "0",
  "data": {
    "token": "<bearer token>",
    "user": { "userId": 123456789 }
  }
}
```

Bean reference: `com.ecoflow.user.api.bean.login.LoginRequestBean` /
`LoginResponseBean` (decompiled tree).

## 2. MQTT credentials

```
GET https://api.ecoflow.com/iot-auth/app/certification?userId=<userId>
Authorization: Bearer <token>
```

Response (subset):

```json
{
  "code": "0",
  "message": "...",
  "data": {
    "certificateAccount": "<mqtt username>",
    "certificatePassword": "<mqtt password>",
    "url": "<broker host>",
    "port": "8883",
    "protocol": "ssl"
  }
}
```

Bean reference: `com.ecoflow.cbb.databus.bean.MqttCertificate` (wrapper
`code/message/data`) and `MqttCertificateConfig`
(`certificateAccount`, `certificatePassword`, `url`, `port`, `protocol`);
endpoint `hf/b.d(@Query("userId"))`. The app caches this under the
`mqtt_login_certificate` cache key.

Notes:

- Despite the naming, this is **username/password MQTT auth**, not TLS client
  certificates — connect with `ssl://host:port`, username/password from the
  response. (Server TLS cert is not pinned by the app; the PoC uses
  `InsecureSkipVerify`.)
- There is a second, SN-signed certification request path
  (`MqttAccountReq.create(sn, userId, …)` with `sign` from the native
  `TreatyEngine.getMqttCheckSign`, and `MqttAccountResponse` whose
  `username`/`password` may be AES/ECB-encrypted per `encryptFlag`). The simple
  `?userId=` endpoint above is sufficient for read-only telemetry and is the
  path the reader uses.

## 3. Client ID convention

App-style client id: `ANDROID_<UUID>_<userId>` (UUID upper-case hex with dashes).
Any unique string works; the broker enforces one connection per client id.

## 4. Session lifetime

Tokens expire server-side; the prototype fails loudly on `code != "0"` /
connection loss. Refresh = re-run steps 1–2.
