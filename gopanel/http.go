package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type httpError struct {
	status  int
	message string
}

func (err httpError) Error() string            { return err.message }
func failure(status int, message string) error { return httpError{status, message} }

type endpoint func(http.ResponseWriter, *http.Request) (any, error)

func (app *panel) route(pattern string, auth bool, handle endpoint) {
	app.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var err error
		if auth {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			var csrf string
			csrf, err = app.session(ctx, r)
			cancel()
			if errors.Is(err, sql.ErrNoRows) {
				err = failure(401, "请先登录")
			}
			if err == nil && r.Method != "GET" && r.Method != "HEAD" && !same(csrf, r.Header.Get("X-CSRF-Token")) {
				err = failure(403, "会话校验失败，请刷新后重试")
			}
		}
		var result any
		if err == nil {
			result, err = handle(w, r)
		}
		var rejected httpError
		switch {
		case errors.As(err, &rejected):
			if rejected.status == 503 || rejected.status == 429 {
				w.Header().Set("Retry-After", "1")
			}
			writeJSON(w, rejected.status, detail{rejected.message})
		case err != nil:
			databaseError(w, err)
			logError("request "+pattern, err)
		case result != nil:
			writeJSON(w, 200, result)
		}
	})
}

func (app *panel) report(handle func(context.Context, *sql.Tx, *http.Request) (any, error)) endpoint {
	return func(w http.ResponseWriter, r *http.Request) (any, error) {
		select {
		case app.reportSlots <- struct{}{}:
			defer func() { <-app.reportSlots }()
		default:
			return nil, failure(503, "查询繁忙，请稍后重试")
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		tx, err := app.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		return handle(ctx, tx, r)
	}
}

func decode(r *http.Request, body any) error {
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(body) != nil || decoder.Decode(new(any)) != io.EOF {
		return failure(422, "请求参数无效")
	}
	return nil
}

func same(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
func digest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func randomToken() string         { return base64.RawURLEncoding.EncodeToString(randBytes(32)) }
func randomHex(size int) string   { return hex.EncodeToString(randBytes(size)) }
func randBytes(size int) []byte   { data := make([]byte, size); rand.Read(data); return data }
func validName(value string) bool { return value != "" && utf8.RuneCountInString(value) <= 64 }
func unixNow() int64              { return time.Now().Unix() }

func sessionHash(r *http.Request) string {
	var token string
	for _, cookie := range r.CookiesNamed("__Host-treeroot") {
		token = cookie.Value
	}
	return digest(token)
}

func sessionCookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "__Host-treeroot", Value: token, Path: "/", MaxAge: age, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func passwordHash(password, salt string) (string, error) {
	if salt == "" {
		salt = randomHex(16)
	}
	raw, err := hex.DecodeString(salt)
	if err != nil {
		return "", err
	}
	digest, err := pbkdf2.Key(sha256.New, password, raw, 600000, 32)
	if err != nil {
		return "", err
	}
	return salt + ":" + hex.EncodeToString(digest), nil
}

func verifyPassword(password, stored string) bool {
	salt, _, found := strings.Cut(stored, ":")
	if !found || len(salt) != 32 {
		return false
	}
	actual, err := passwordHash(password, salt)
	return err == nil && same(stored, actual)
}

func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if loopback(r.RemoteAddr) {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if ip, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
			return ip.Unmap().String()
		}
	}
	return host
}

func (app *panel) login(w http.ResponseWriter, r *http.Request) (any, error) {
	var body struct{ Username, Password *string }
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if body.Username == nil || body.Password == nil || utf8.RuneCountInString(*body.Username) > 64 {
		return nil, failure(422, "请求参数无效")
	}
	select {
	case app.hashSlots <- struct{}{}:
		defer func() { <-app.hashSlots }()
	default:
		return nil, failure(429, "登录请求繁忙，请稍后重试")
	}
	ctx := r.Context()
	ip, now := clientIP(r), unixNow()
	check := func(db querier) error {
		attempts, err := one(ctx, db, "SELECT COUNT(*) n FROM login_attempts WHERE ip=? AND ts>?", ip, now-900)
		if err != nil {
			return err
		}
		if attempts.number("n") >= 8 {
			return failure(429, "尝试次数过多，请 15 分钟后重试")
		}
		return nil
	}
	if err := check(app.database); err != nil {
		return nil, err
	}
	admin, err := one(ctx, app.database, "SELECT username,password_hash FROM admin WHERE id=1")
	if err != nil {
		return nil, err
	}
	valid := *body.Username == admin.text("username") && verifyPassword(*body.Password, admin.text("password_hash"))
	token, csrf := randomToken(), randomToken()
	err = app.write(ctx, func(tx *sql.Tx) error {
		if err := check(tx); err != nil {
			return err
		}
		current, err := one(ctx, tx, "SELECT password_hash FROM admin WHERE id=1")
		if err != nil {
			return err
		}
		valid = valid && same(current.text("password_hash"), admin.text("password_hash"))
		if !valid {
			_, err = tx.Exec("INSERT INTO login_attempts VALUES(?,?)", ip, now)
			return err
		}
		_, err = tx.Exec("INSERT INTO sessions VALUES(?,?,?)", digest(token), csrf, now+43200)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, failure(401, "账号或密码不正确")
	}
	sessionCookie(w, token, 43200)
	return row{"csrf": csrf, "username": *body.Username}, nil
}

func (app *panel) logout(w http.ResponseWriter, r *http.Request) (any, error) {
	err := app.write(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM sessions WHERE token_hash=?", sessionHash(r))
		return err
	})
	if err != nil {
		return nil, err
	}
	sessionCookie(w, "", -1)
	return row{"ok": true}, nil
}

func (app *panel) password(_ http.ResponseWriter, r *http.Request) (any, error) {
	var body struct{ Current, Password *string }
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	if body.Current == nil || body.Password == nil {
		return nil, failure(422, "请求参数无效")
	}
	select {
	case app.hashSlots <- struct{}{}:
		defer func() { <-app.hashSlots }()
	default:
		return nil, failure(429, "改密请求繁忙，请稍后重试")
	}
	ctx := r.Context()
	admin, err := one(ctx, app.database, "SELECT password_hash FROM admin WHERE id=1")
	if err != nil {
		return nil, err
	}
	if !verifyPassword(*body.Current, admin.text("password_hash")) {
		return nil, failure(400, "原密码不正确")
	}
	next, err := passwordHash(*body.Password, "")
	if err != nil {
		return nil, err
	}
	err = app.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.Exec("UPDATE admin SET password_hash=? WHERE id=1 AND password_hash=? AND EXISTS(SELECT 1 FROM sessions WHERE token_hash=? AND expires>?)", next, admin.text("password_hash"), sessionHash(r), unixNow())
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return failure(400, "原密码不正确或会话已过期")
		}
		_, err = tx.Exec("DELETE FROM sessions WHERE token_hash!=?", sessionHash(r))
		return err
	})
	return row{"ok": true}, err
}

func (app *panel) routes() {
	app.mux.HandleFunc("GET /api/me", app.me)
	app.route("GET /api/activity", false, func(w http.ResponseWriter, r *http.Request) (any, error) {
		query, ok := parseActivity(r.URL.RawQuery)
		if !ok {
			return nil, failure(422, "查询参数无效")
		}
		app.activity(w, r, query)
		return nil, nil
	})
	app.route("POST /api/login", false, app.login)
	app.route("POST /api/logout", true, app.logout)
	app.route("POST /api/password", true, app.password)
	app.routesUsers()
	app.routesEgress()
	app.routesHome()
	app.route("GET /api/server", true, app.serverInfo)
	app.route("GET /api/resources", true, app.resourceInfo)
	for _, path := range []string{"/{$}", "/admin"} {
		app.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(app.root, "index.html"))
		})
	}
	for _, name := range []string{"app.js", "style.css", "echarts.min.js", "egress.js", "egress.css"} {
		app.mux.HandleFunc("GET /assets/"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=86400")
			if strings.HasSuffix(name, ".js") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			http.ServeFile(w, r, filepath.Join(app.root, name))
		})
	}
	app.route("GET /sub/{token}/{kind}", false, app.subscription)
	for _, kind := range []string{"clash.yaml", "shadowrocket"} {
		app.route("GET /{token}/"+kind, false, func(w http.ResponseWriter, r *http.Request) (any, error) {
			r.SetPathValue("kind", kind)
			return app.subscription(w, r)
		})
	}
}

func (app *panel) subscription(w http.ResponseWriter, r *http.Request) (any, error) {
	kind := r.PathValue("kind")
	if kind != "clash.yaml" && kind != "shadowrocket" {
		return nil, failure(404, "Not Found")
	}
	user, err := one(r.Context(), app.database, "SELECT name,client_uuid,upload,download FROM users WHERE token=? AND enabled=1", r.PathValue("token"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, failure(404, "订阅不存在或已禁用")
	}
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(app.baseURL)
	host, name, uuid := base.Host, "美国-圣何塞 · "+user.text("name"), user.text("client_uuid")
	encode := func(text string) string { return base64.StdEncoding.EncodeToString([]byte(text)) }
	w.Header().Set("profile-update-interval", "6")
	w.Header().Set("profile-title", "base64:"+encode("TreeRoot · "+user.text("name")))
	w.Header().Set("subscription-userinfo", fmt.Sprintf("upload=%d; download=%d; total=0; expire=0", user.number("upload"), user.number("download")))
	if kind == "shadowrocket" {
		data, _ := json.Marshal(row{"v": "2", "ps": name, "add": host, "port": "443", "id": uuid, "aid": "0", "scy": "auto", "net": "ws", "type": "none", "host": host, "path": app.config.WSPath, "tls": "tls", "sni": host, "alpn": "http/1.1"})
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, err = io.WriteString(w, encode("vmess://"+encode(string(data))+"\n"))
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(app.root, "subscription-template.json"))
	if err != nil {
		return nil, err
	}
	var template row
	if err := json.Unmarshal(data, &template); err != nil {
		return nil, err
	}
	template["proxies"] = []row{{"name": name, "type": "vmess", "server": host, "port": 443, "uuid": uuid, "alterId": 0, "cipher": "auto", "udp": true, "tls": true, "servername": host, "skip-cert-verify": false, "network": "ws", "ws-opts": row{"path": app.config.WSPath, "headers": row{"Host": host}}}}
	template["proxy-groups"] = []row{{"name": "美国自建", "type": "select", "proxies": []string{name}}}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=TreeRoot.yaml")
	return nil, json.NewEncoder(w).Encode(template)
}
