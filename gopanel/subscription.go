package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const subscriptionKey = "clash_subscription_yaml"
const proxyGroup = "美国自建"

type clashTemplate struct {
	Port  int      `json:"mixed-port" yaml:"mixed-port"`
	LAN   bool     `json:"allow-lan" yaml:"allow-lan"`
	Mode  string   `json:"mode" yaml:"mode"`
	Log   string   `json:"log-level" yaml:"log-level"`
	IPv6  bool     `json:"ipv6" yaml:"ipv6"`
	Rules []string `json:"rules" yaml:"rules"`
}

var clashDomain = regexp.MustCompile(`(?i)^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var clashCountry = regexp.MustCompile(`^[A-Za-z]{2}$`)

func validateClashRule(line string, last bool) error {
	p := strings.Split(line, ",")
	for _, field := range p {
		if field == "" || strings.TrimSpace(field) != field {
			return errors.New("字段不能为空或含首尾空格")
		}
	}
	if last != (p[0] == "MATCH") {
		return errors.New("最后一条须为 MATCH，且只能出现一次")
	}
	if len(p) == 4 && p[3] == "no-resolve" && (p[0] == "IP-CIDR" || p[0] == "IP-CIDR6" || p[0] == "GEOIP") {
		p = p[:3]
	}
	if (last && len(p) != 2) || (!last && len(p) != 3) {
		return errors.New("规则字段数量有误")
	}
	switch p[len(p)-1] {
	case "DIRECT", "REJECT", proxyGroup:
	default:
		return errors.New("策略须为 DIRECT、REJECT 或美国自建")
	}
	switch p[0] {
	case "MATCH":
	case "DOMAIN", "DOMAIN-SUFFIX":
		if len(p[1]) > 253 || !clashDomain.MatchString(p[1]) {
			return errors.New("域名无效")
		}
	case "DOMAIN-KEYWORD":
		if strings.ContainsAny(p[1], " \r\t\n") {
			return errors.New("域名关键字无效")
		}
	case "IP-CIDR", "IP-CIDR6":
		prefix, err := netip.ParsePrefix(p[1])
		if err != nil || (p[0] == "IP-CIDR6" && !prefix.Addr().Is6()) {
			return errors.New("IP 网段无效")
		}
	case "GEOIP":
		if !clashCountry.MatchString(p[1]) && !strings.EqualFold(p[1], "LAN") {
			return errors.New("国家或地区代码无效")
		}
	case "DST-PORT", "SRC-PORT":
		n, err := strconv.Atoi(p[1])
		if err != nil || n < 1 || n > 65535 {
			return errors.New("端口无效")
		}
	case "NETWORK":
		if !strings.EqualFold(p[1], "TCP") && !strings.EqualFold(p[1], "UDP") {
			return errors.New("传输协议无效")
		}
	default:
		return errors.New("不支持此规则类型")
	}
	return nil
}

func parseClash(source string) (clashTemplate, error) {
	var t clashTemplate
	if len(source) == 0 || len(source) > 128<<10 {
		return t, failure(422, "YAML 须为 1–131072 字节")
	}
	d := yaml.NewDecoder(strings.NewReader(source))
	d.KnownFields(true)
	if err := d.Decode(&t); err != nil {
		return t, failure(422, "YAML 无效："+err.Error())
	}
	if d.Decode(new(any)) != io.EOF {
		return t, failure(422, "只允许一份 YAML 配置")
	}
	if t.Mode != "rule" || t.Port < 1 || t.Port > 65535 {
		return t, failure(422, "mode 须为 rule，mixed-port 须为有效端口")
	}
	switch t.Log {
	case "silent", "error", "warning", "info", "debug":
	default:
		return t, failure(422, "log-level 无效")
	}
	if len(t.Rules) == 0 {
		return t, failure(422, "rules 不能为空")
	}
	for i, line := range t.Rules {
		if err := validateClashRule(line, i == len(t.Rules)-1); err != nil {
			return t, failure(422, fmt.Sprintf("第 %d 条规则：%v", i+1, err))
		}
	}
	return t, nil
}

func (app *panel) clashSource(ctx context.Context, db querier) (string, error) {
	s, err := one(ctx, db, "SELECT value FROM state WHERE key=?", subscriptionKey)
	if err == nil {
		return s.text("value"), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(app.root, "subscription-template.json"))
	if err != nil {
		return "", err
	}
	var t clashTemplate
	if err = json.Unmarshal(data, &t); err != nil {
		return "", err
	}
	source, err := yaml.Marshal(t)
	return string(source), err
}

func (app *panel) routesSubscription() {
	const path = "/api/vps/subscription"
	app.route("GET "+path, true, func(w http.ResponseWriter, r *http.Request) (any, error) {
		w.Header().Set("Cache-Control", "no-store")
		source, err := app.clashSource(r.Context(), app.database)
		return row{"source": source, "revision": digest(source)}, err
	})
	edit := func(save bool) endpoint {
		return func(w http.ResponseWriter, r *http.Request) (any, error) {
			var body struct{ Source, Revision string }
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err := decode(r, &body); err != nil {
				return nil, err
			}
			t, err := parseClash(body.Source)
			if err != nil {
				return nil, err
			}
			if !save {
				return row{"rules": len(t.Rules), "fallback": t.Rules[len(t.Rules)-1]}, nil
			}
			err = app.write(r.Context(), func(tx *sql.Tx) error {
				current, err := app.clashSource(r.Context(), tx)
				if err != nil {
					return err
				}
				if digest(current) != body.Revision {
					return failure(409, "配置已被更新，请重新载入已发布版本后再编辑")
				}
				return state(tx, subscriptionKey, body.Source)
			})
			return row{"source": body.Source, "revision": digest(body.Source)}, err
		}
	}
	app.route("POST "+path+"/preview", true, edit(false))
	app.route("PUT "+path, true, edit(true))
}
