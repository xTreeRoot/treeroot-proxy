package homegateway

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type RulePatch struct {
	Prepend []string `yaml:"prepend"`
	Append  []string `yaml:"append"`
	Delete  []string `yaml:"delete"`
}
type RuleChange struct {
	Operation string `json:"operation"`
	Line      string `json:"line"`
}
type RulePreview struct {
	Config  Config       `json:"config"`
	Changes []RuleChange `json:"changes"`
}

// PreviewRules translates the supported Clash rule patch into the same validated
// device policies as the form editor. It never changes persistent configuration.
func PreviewRules(current Config, source, scope, subject string, nodes []Node) (RulePreview, error) {
	var result RulePreview
	if len(source) == 0 || len(source) > 64<<10 {
		return result, errors.New("规则 YAML 须为 1–65536 字节")
	}
	decoder := yaml.NewDecoder(strings.NewReader(source))
	decoder.KnownFields(true)
	var patch RulePatch
	if e := decoder.Decode(&patch); e != nil {
		return result, fmt.Errorf("规则 YAML 无效：%w", e)
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		return result, errors.New("只允许一份 YAML 配置")
	}
	if len(patch.Prepend)+len(patch.Append)+len(patch.Delete) == 0 {
		return result, errors.New("没有待处理的规则")
	}
	if len(patch.Prepend)+len(patch.Append)+len(patch.Delete) > 512 {
		return result, errors.New("单次最多处理 512 条规则")
	}
	if scope == "all" {
		subject = ""
	} else if scope == "device" {
		var e error
		if subject, e = MAC(subject); e != nil {
			return result, e
		}
	} else if scope != "group" || subject == "" || len(subject) > 128 {
		return result, errors.New("请选择设备范围")
	}
	parse := func(line string) (Rule, error) {
		r := Rule{Scope: scope, Subject: subject, Enabled: true}
		parts := strings.Split(line, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 2 || len(parts) > 3 {
			return r, errors.New("只支持 类型,目标,策略；MATCH 使用 MATCH,策略")
		}
		if strings.EqualFold(parts[0], "MATCH") {
			if len(parts) != 2 {
				return r, errors.New("MATCH 格式应为 MATCH,策略")
			}
			r.TargetType = "any"
		} else {
			if len(parts) != 3 {
				return r, errors.New("规则格式应为 类型,目标,策略")
			}
			r.Target = strings.ToLower(parts[1])
			switch strings.ToUpper(parts[0]) {
			case "DOMAIN":
				r.TargetType = "domain"
			case "DOMAIN-SUFFIX":
				r.TargetType = "suffix"
			case "DOMAIN-KEYWORD":
				r.TargetType = "keyword"
			case "IP-CIDR", "IP-CIDR6":
				r.TargetType = "cidr"
			case "DST-PORT":
				r.TargetType, r.Target, r.Port = "any", "", parts[1]
			case "NETWORK":
				r.TargetType, r.Target, r.Protocol = "any", "", strings.ToLower(parts[1])
			default:
				return r, fmt.Errorf("不支持的规则类型 %s", parts[0])
			}
		}
		policy := parts[len(parts)-1]
		switch strings.ToUpper(policy) {
		case "DIRECT":
			r.Action = "direct"
		case "REJECT":
			r.Action = "reject"
		default:
			for _, node := range nodes {
				if node.ID == policy || node.Name == policy {
					if r.Node != "" && r.Node != node.ID {
						return r, errors.New("节点名称重复，请使用节点编号")
					}
					r.Node, r.Action = node.ID, "proxy"
				}
			}
			if r.Node == "" {
				return r, fmt.Errorf("策略 %s 不是 DIRECT、REJECT 或已启用的 VPS 节点", policy)
			}
		}
		r.ID = "advanced-check"
		check := Config{Default: "direct", Rules: []Rule{r}, Nodes: nodes}
		if e := check.Validate(); e != nil {
			return r, e
		}
		return r, nil
	}
	key := func(r Rule) string {
		return strings.Join([]string{r.Scope, r.Subject, r.TargetType, strings.ToLower(r.Target), r.Port, r.Protocol, r.Action, r.Node}, "\x00")
	}
	parseList := func(name string, lines []string) ([]Rule, error) {
		rules := []Rule{}
		for i, line := range lines {
			r, e := parse(line)
			if e != nil {
				return nil, fmt.Errorf("%s 第 %d 条：%w", name, i+1, e)
			}
			rules = append(rules, r)
		}
		return rules, nil
	}
	pre, e := parseList("prepend", patch.Prepend)
	if e != nil {
		return result, e
	}
	post, e := parseList("append", patch.Append)
	if e != nil {
		return result, e
	}
	del, e := parseList("delete", patch.Delete)
	if e != nil {
		return result, e
	}
	remove := map[string]bool{}
	for _, r := range del {
		remove[key(r)] = true
	}
	// Preserve the current effective order, then renumber only the selected scope.
	base := append([]Rule(nil), current.Rules...)
	sort.SliceStable(base, func(i, j int) bool { return base[i].Priority < base[j].Priority })
	kept := []Rule{}
	for _, r := range base {
		if remove[key(r)] {
			result.Changes = append(result.Changes, RuleChange{"delete", ruleLine(r, nodes)})
		} else {
			kept = append(kept, r)
		}
	}
	seen := map[string]bool{}
	for _, r := range kept {
		seen[key(r)] = true
	}
	ids := map[string]bool{}
	for _, r := range current.Rules {
		ids[r.ID] = true
	}
	add := func(rules []Rule, operation string) []Rule {
		added := []Rule{}
		for _, r := range rules {
			k := key(r)
			if seen[k] {
				continue
			}
			seen[k] = true
			h := sha256.Sum256([]byte(k))
			r.ID = "adv-" + hex.EncodeToString(h[:16])
			for ids[r.ID] {
				r.ID += "x"
			}
			ids[r.ID] = true
			added = append(added, r)
			result.Changes = append(result.Changes, RuleChange{operation, ruleLine(r, nodes)})
		}
		return added
	}
	before, after := add(pre, "prepend"), add(post, "append")
	merged := append(append(before, kept...), after...)
	for i := range merged {
		merged[i].Priority = (i + 1) * 100
	}
	current.Rules = merged
	current.Nodes = append([]Node(nil), current.Nodes...)
	for _, r := range merged {
		if r.Action != "proxy" {
			continue
		}
		found := false
		for _, n := range current.Nodes {
			if n.ID == r.Node {
				found = true
			}
		}
		if !found {
			for _, n := range nodes {
				if n.ID == r.Node {
					current.Nodes = append(current.Nodes, Node{ID: n.ID, Name: n.Name})
				}
			}
		}
	}
	if e = current.Validate(); e != nil {
		return result, e
	}
	result.Config = current
	if result.Changes == nil {
		result.Changes = []RuleChange{}
	}
	return result, nil
}

func ruleLine(r Rule, nodes []Node) string {
	kind := map[string]string{"domain": "DOMAIN", "suffix": "DOMAIN-SUFFIX", "keyword": "DOMAIN-KEYWORD", "cidr": "IP-CIDR", "any": "MATCH", "ip": "IP"}[r.TargetType]
	target := r.Target
	if r.Port != "" {
		kind, target = "DST-PORT", r.Port
	}
	if r.Protocol != "" {
		kind, target = "NETWORK", strings.ToUpper(r.Protocol)
	}
	policy := map[string]string{"direct": "DIRECT", "reject": "REJECT", "proxy": r.Node}[r.Action]
	if target == "" {
		return kind + "," + policy
	}
	return kind + "," + target + "," + policy
}
