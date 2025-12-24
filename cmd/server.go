package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daeuniverse/dae/common/consts"
	"github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/mohae/deepcopy"
	"github.com/sirupsen/logrus"
)

var (
	httpServer *HttpServer
)

type HttpServer struct {
	server *http.Server
	log    *logrus.Logger
}

func updateConfigs(w http.ResponseWriter, req *http.Request) {
	var requestBody struct {
		HttpPort     *int      `json:"http_port"`
		HttpListen   *string   `json:"http_listen"`
		LogLevel     *string   `json:"log_level"`
		LanInterface *[]string `json:"lan_interface"`
		WanInterface *[]string `json:"wan_interface"`
	}
	err := json.NewDecoder(req.Body).Decode(&requestBody)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to decode request body")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to decode request body",
		})
		return
	}

	newConf := deepcopy.Copy(conf).(*config.Config)
	if requestBody.HttpPort != nil {
		newConf.Global.HttpPort = *requestBody.HttpPort
		httpServer.log.Infof("Http port updated: %d", newConf.Global.HttpPort)
	}
	if requestBody.HttpListen != nil {
		newConf.Global.HttpListen = *requestBody.HttpListen
		httpServer.log.Infof("Http listen updated: %s", newConf.Global.HttpListen)
	}
	if requestBody.LogLevel != nil {
		newConf.Global.LogLevel = *requestBody.LogLevel
		httpServer.log.Infof("Log level updated: %s", newConf.Global.LogLevel)
	}
	if requestBody.LanInterface != nil {
		newConf.Global.LanInterface = *requestBody.LanInterface
		httpServer.log.Infof("Lan interface updated: %v", newConf.Global.LanInterface)
	}
	if requestBody.WanInterface != nil {
		newConf.Global.WanInterface = *requestBody.WanInterface
		httpServer.log.Infof("Wan interface updated: %v", newConf.Global.WanInterface)
	}
	configBytes, err := newConf.Marshal(4)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to marshal config")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to marshal config",
		})
		return
	}
	if err := os.WriteFile(cfgFile, configBytes, 0644); err != nil {
		httpServer.log.WithError(err).Errorln("Failed to save config to file")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to save config to file",
		})
		return
	}
	// go _restart()
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"message": "Success",
	})
}

func getConfigs(w http.ResponseWriter, req *http.Request) {
	json.NewEncoder(w).Encode(conf)
}

func getVersion(w http.ResponseWriter, req *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"version": config.Version,
	})
}

func _restart() {
	// Read PID from file
	pidBytes, err := os.ReadFile(PidFilePath)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to read pid file")
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to parse pid")
		return
	}

	// Check if reload is already in progress
	code, _, err := readSignalProgressFile()
	if err == nil && code != consts.ReloadDone && code != consts.ReloadError {
		httpServer.log.Warnln("Another reload operation is in progress")
		return
	}

	// Set the progress as ReloadSend
	if err := os.WriteFile(SignalProgressFilePath, []byte{consts.ReloadSend}, 0644); err != nil {
		httpServer.log.WithError(err).Errorln("Failed to write progress file")
		return
	}

	// Send SIGUSR1 signal to trigger reload
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		httpServer.log.WithError(err).Errorln("Failed to send reload signal")
		return
	}

	// Wait a bit for the signal to be processed
	time.Sleep(500 * time.Millisecond)
	code, _, _ = readSignalProgressFile()
	if code == consts.ReloadSend {
		// Old version dae is running or signal not processed
		httpServer.log.Warnln("Signal not processed, may be old version")
		return
	}

	// Wait for reload to complete
	maxWaitTime := 30 * time.Second
	startTime := time.Now()
	for {
		if time.Since(startTime) > maxWaitTime {
			httpServer.log.Warnln("Reload timeout")
			return
		}

		time.Sleep(200 * time.Millisecond)
		code, content, err := readSignalProgressFile()
		if err != nil {
			httpServer.log.WithError(err).Warnln("Failed to read progress file")
			continue
		}

		if code == consts.ReloadDone {
			httpServer.log.Infof("Reload completed: %s", content)
			return
		}

		if code == consts.ReloadError {
			httpServer.log.Errorf("Reload failed: %s", content)
			return
		}
	}
}

func restart(w http.ResponseWriter, req *http.Request) {
	go _restart()
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"message": "Success",
	})

}

func updateNodes(w http.ResponseWriter, req *http.Request) {
	var requestBody struct {
		Nodes []config.KeyableString `json:"nodes"`
	}
	err := json.NewDecoder(req.Body).Decode(&requestBody)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to decode nodes")
		return
	}
	conf.Node = requestBody.Nodes
	configBytes, err := conf.Marshal(4)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to marshal config")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to marshal config",
		})
		return
	}
	if err := os.WriteFile(cfgFile, configBytes, 0644); err != nil {
		httpServer.log.WithError(err).Errorln("Failed to save config to file")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to save config to file",
		})
		return
	}
	go _restart()
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

func getGroups(w http.ResponseWriter, req *http.Request) {
	type GroupInfo struct {
		Name               string   `json:"name"`
		Filter             string   `json:"filter,omitempty"`
		Policy             string   `json:"policy"`
		TcpCheckUrl        []string `json:"tcp_check_url,omitempty"`
		TcpCheckHttpMethod string   `json:"tcp_check_http_method,omitempty"`
		UdpCheckDns        []string `json:"udp_check_dns,omitempty"`
		CheckInterval      string   `json:"check_interval,omitempty"`
		CheckTolerance     string   `json:"check_tolerance,omitempty"`
	}

	var groups []GroupInfo
	for _, group := range conf.Group {
		groupInfo := GroupInfo{
			Name: group.Name,
		}

		// 格式化 Filter (二维数组转换为单个字符串)
		// 如果有多个 filter 条件，将它们用 " && " 连接
		var filterParts []string
		for _, filterRow := range group.Filter {
			var funcStrs []string
			for _, f := range filterRow {
				funcStrs = append(funcStrs, f.String(false, false, false))
			}
			if len(funcStrs) > 0 {
				filterParts = append(filterParts, strings.Join(funcStrs, " && "))
			}
		}
		if len(filterParts) > 0 {
			groupInfo.Filter = strings.Join(filterParts, " && ")
		}

		// 格式化 Policy
		switch policy := group.Policy.(type) {
		case string:
			groupInfo.Policy = policy
		case *config_parser.Function:
			groupInfo.Policy = policy.String(false, false, false)
		case []*config_parser.Function:
			if len(policy) > 0 {
				var policyStrs []string
				for _, f := range policy {
					policyStrs = append(policyStrs, f.String(false, false, false))
				}
				groupInfo.Policy = strings.Join(policyStrs, " && ")
			} else {
				groupInfo.Policy = ""
			}
		default:
			groupInfo.Policy = fmt.Sprintf("%v", group.Policy)
		}

		// 格式化可选字段
		if len(group.TcpCheckUrl) > 0 {
			groupInfo.TcpCheckUrl = group.TcpCheckUrl
		}
		if group.TcpCheckHttpMethod != "" {
			groupInfo.TcpCheckHttpMethod = group.TcpCheckHttpMethod
		}
		if len(group.UdpCheckDns) > 0 {
			groupInfo.UdpCheckDns = group.UdpCheckDns
		}
		if group.CheckInterval > 0 {
			groupInfo.CheckInterval = group.CheckInterval.String()
		}
		if group.CheckTolerance > 0 {
			groupInfo.CheckTolerance = group.CheckTolerance.String()
		}

		groups = append(groups, groupInfo)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"groups": groups,
	})
}

func getRoutingRules(w http.ResponseWriter, req *http.Request) {
	var rules []string
	for _, rule := range conf.Routing.Rules {
		rules = append(rules, rule.String(false, false, false))
	}

	var fallbackStr string
	switch fb := conf.Routing.Fallback.(type) {
	case string:
		fallbackStr = fb
	case *config_parser.Function:
		fallbackStr = fb.String(false, false, false)
	case []*config_parser.Function:
		if len(fb) > 0 {
			fallbackStr = fb[0].String(false, false, false)
		} else {
			fallbackStr = "direct"
		}
	default:
		fallbackStr = fmt.Sprintf("%v", conf.Routing.Fallback)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"rules":    rules,
		"fallback": fallbackStr,
	})
}

// ParseRoutingRules parses an array of routing rule strings and returns the parsed routing rule objects.
// It accepts an array of routing rule strings
// (e.g., []string{"domain(geosite:category-ads) -> block", "ip(geoip:cn) -> direct"})
// and returns an array of parsed routing rule objects and any error that occurred.
func ParseRoutingRules(rules []string, groups []string) ([]*config_parser.RoutingRule, error) {
	if len(rules) == 0 {
		return []*config_parser.RoutingRule{}, nil
	}

	// Wrap the routing rules array into a complete routing configuration block
	var builder strings.Builder
	builder.WriteString("routing {\n")
	for _, rule := range rules {
		// Trim leading and trailing whitespace
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		builder.WriteString("    ")
		builder.WriteString(rule)
		builder.WriteString("\n")
	}
	builder.WriteString("}")

	// Parse the configuration using config_parser.Parse
	sections, err := config_parser.Parse(builder.String())
	if err != nil {
		return nil, fmt.Errorf("failed to parse routing rules: %w", err)
	}

	// Extract routing rules from the parsing results
	var routingRules []*config_parser.RoutingRule
	for _, section := range sections {
		if section.Name == "routing" {
			for _, item := range section.Items {
				if item.Type == config_parser.ItemType_RoutingRule {
					if rule, ok := item.Value.(*config_parser.RoutingRule); ok {
						if (rule.Outbound.Name != "direct") &&
							(rule.Outbound.Name != "must_direct") &&
							!slices.Contains(groups, rule.Outbound.Name) {
							return nil, fmt.Errorf("group %s not found", rule.Outbound.Name)
						}
						routingRules = append(routingRules, rule)
					}
				}
			}
		}
	}
	return routingRules, nil
}

// ParseFallback parses a fallback string and returns a FunctionOrString.
// It accepts either a simple string (e.g., "direct") or a function format (e.g., "fixed(0)").
func ParseFallback(fallbackStr string) (config.FunctionOrString, error) {
	if fallbackStr == "" {
		return "direct", nil // Default fallback
	}

	// Try to parse as a function first
	// Wrap in a routing section to use the parser
	configStr := fmt.Sprintf("routing {\n    fallback: %s\n}", fallbackStr)
	sections, err := config_parser.Parse(configStr)
	if err != nil {
		// If parsing fails, treat as a simple string
		return fallbackStr, nil
	}

	// Extract fallback from parsed sections
	for _, section := range sections {
		if section.Name == "routing" {
			for _, item := range section.Items {
				if item.Type == config_parser.ItemType_Param {
					if param, ok := item.Value.(*config_parser.Param); ok && param.Key == "fallback" {
						if param.AndFunctions != nil && len(param.AndFunctions) > 0 {
							// Return the first function
							return param.AndFunctions[0], nil
						} else if param.Val != "" {
							// Return as string
							return param.Val, nil
						}
					}
				}
			}
		}
	}

	// Fallback to string if parsing didn't yield a result
	return fallbackStr, nil
}

// ParseGroupFilter parses a filter string and returns [][]*config_parser.Function.
// The string represents a filter condition (multiple functions joined by &&).
// Example: "subtag(regex: '^my_', another_sub) && !name(keyword: 'ExpireAt:')"
func ParseGroupFilter(filterStr string) ([][]*config_parser.Function, error) {
	filterStr = strings.TrimSpace(filterStr)
	if filterStr == "" {
		return [][]*config_parser.Function{}, nil
	}

	// Wrap in a group section to parse
	configStr := fmt.Sprintf("group {\n    test_group {\n        filter: %s\n    }\n}", filterStr)
	sections, err := config_parser.Parse(configStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse filter %q: %w", filterStr, err)
	}

	// Extract filter from parsed sections
	for _, section := range sections {
		if section.Name == "group" {
			for _, item := range section.Items {
				// Section items are stored as ItemType_Param with Section as Value
				if subSection, ok := item.Value.(*config_parser.Section); ok && subSection.Name == "test_group" {
					for _, subItem := range subSection.Items {
						if subItem.Type == config_parser.ItemType_Param {
							if param, ok := subItem.Value.(*config_parser.Param); ok && param.Key == "filter" {
								if param.AndFunctions != nil && len(param.AndFunctions) > 0 {
									// Return as [][]*config_parser.Function (wrap in 2D array)
									return [][]*config_parser.Function{param.AndFunctions}, nil
								}
							}
						}
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("failed to extract filter from %q", filterStr)
}

// ParseGroupPolicy parses a policy string and returns FunctionListOrString.
// It accepts either a simple string (e.g., "random") or a function format (e.g., "fixed(0)").
func ParseGroupPolicy(policyStr string) (config.FunctionListOrString, error) {
	if policyStr == "" {
		return nil, fmt.Errorf("policy cannot be empty")
	}

	// Try to parse as a function first
	// Wrap in a group section to use the parser
	configStr := fmt.Sprintf("group {\n    test_group {\n        policy: %s\n    }\n}", policyStr)
	sections, err := config_parser.Parse(configStr)
	if err != nil {
		// If parsing fails, treat as a simple string
		return policyStr, nil
	}

	// Extract policy from parsed sections
	for _, section := range sections {
		if section.Name == "group" {
			for _, item := range section.Items {
				// Section items are stored as ItemType_Param with Section as Value
				if subSection, ok := item.Value.(*config_parser.Section); ok && subSection.Name == "test_group" {
					for _, subItem := range subSection.Items {
						if subItem.Type == config_parser.ItemType_Param {
							if param, ok := subItem.Value.(*config_parser.Param); ok && param.Key == "policy" {
								if param.AndFunctions != nil && len(param.AndFunctions) > 0 {
									if len(param.AndFunctions) == 1 {
										return param.AndFunctions[0], nil
									}
									return param.AndFunctions, nil
								} else if param.Val != "" {
									return param.Val, nil
								}
							}
						}
					}
				}
			}
		}
	}

	// Fallback to string if parsing didn't yield a result
	return policyStr, nil
}

func getProxyConfig(w http.ResponseWriter, req *http.Request) {
	var rules []string
	for _, rule := range conf.Routing.Rules {
		rules = append(rules, rule.String(false, false, false))
	}

	var fallbackStr string
	switch fb := conf.Routing.Fallback.(type) {
	case string:
		fallbackStr = fb
	case *config_parser.Function:
		fallbackStr = fb.String(false, false, false)
	case []*config_parser.Function:
		if len(fb) > 0 {
			fallbackStr = fb[0].String(false, false, false)
		} else {
			fallbackStr = "direct"
		}
	default:
		fallbackStr = fmt.Sprintf("%v", conf.Routing.Fallback)
	}

	type GroupInfo struct {
		Name               string   `json:"name"`
		Filter             string   `json:"filter,omitempty"`
		Policy             string   `json:"policy"`
		TcpCheckUrl        []string `json:"tcp_check_url,omitempty"`
		TcpCheckHttpMethod string   `json:"tcp_check_http_method,omitempty"`
		UdpCheckDns        []string `json:"udp_check_dns,omitempty"`
		CheckInterval      string   `json:"check_interval,omitempty"`
		CheckTolerance     string   `json:"check_tolerance,omitempty"`
	}

	var groups []GroupInfo
	for _, group := range conf.Group {
		groupInfo := GroupInfo{
			Name: group.Name,
		}

		// 格式化 Filter (二维数组转换为单个字符串)
		// 如果有多个 filter 条件，将它们用 " && " 连接
		var filterParts []string
		for _, filterRow := range group.Filter {
			var funcStrs []string
			for _, f := range filterRow {
				funcStrs = append(funcStrs, f.String(false, false, false))
			}
			if len(funcStrs) > 0 {
				filterParts = append(filterParts, strings.Join(funcStrs, " && "))
			}
		}
		if len(filterParts) > 0 {
			groupInfo.Filter = strings.Join(filterParts, " && ")
		}

		// 格式化 Policy
		switch policy := group.Policy.(type) {
		case string:
			groupInfo.Policy = policy
		case *config_parser.Function:
			groupInfo.Policy = policy.String(false, false, false)
		case []*config_parser.Function:
			if len(policy) > 0 {
				var policyStrs []string
				for _, f := range policy {
					policyStrs = append(policyStrs, f.String(false, false, false))
				}
				groupInfo.Policy = strings.Join(policyStrs, " && ")
			} else {
				groupInfo.Policy = ""
			}
		default:
			groupInfo.Policy = fmt.Sprintf("%v", group.Policy)
		}

		// 格式化可选字段
		if len(group.TcpCheckUrl) > 0 {
			groupInfo.TcpCheckUrl = group.TcpCheckUrl
		}
		if group.TcpCheckHttpMethod != "" {
			groupInfo.TcpCheckHttpMethod = group.TcpCheckHttpMethod
		}
		if len(group.UdpCheckDns) > 0 {
			groupInfo.UdpCheckDns = group.UdpCheckDns
		}
		if group.CheckInterval > 0 {
			groupInfo.CheckInterval = group.CheckInterval.String()
		}
		if group.CheckTolerance > 0 {
			groupInfo.CheckTolerance = group.CheckTolerance.String()
		}

		groups = append(groups, groupInfo)
	}

	json.NewEncoder(w).Encode(map[string]any{
		"routing": map[string]any{
			"rules":    rules,
			"fallback": fallbackStr,
		},
		"groups": groups,
		"nodes":  conf.Node,
	})
}

func updateProxyConfig(w http.ResponseWriter, req *http.Request) {
	var requestBody struct {
		Routing *struct {
			Rules    []string `json:"rules"`
			Fallback string   `json:"fallback,omitempty"`
		} `json:"routing"`
		Groups *[]struct {
			Name   string `json:"name"`
			Filter string `json:"filter"`
			Policy string `json:"policy"`
		} `json:"groups"`
		Nodes *[]config.KeyableString `json:"nodes"`
	}
	err := json.NewDecoder(req.Body).Decode(&requestBody)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to decode routing rules")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to decode routing rules",
		})
		return
	}

	var groups []string
	if requestBody.Groups != nil {
		for _, group := range *requestBody.Groups {
			groups = append(groups, group.Name)
		}
	} else {
		for _, group := range conf.Group {
			groups = append(groups, group.Name)
		}
	}

	if requestBody.Routing != nil {
		routingRules, err := ParseRoutingRules(requestBody.Routing.Rules, groups)
		if err != nil {
			httpServer.log.WithError(err).Errorln("Failed to parse routing rules")
			json.NewEncoder(w).Encode(map[string]any{
				"status":  "error",
				"message": fmt.Sprintf("Failed to parse routing rules: %v", err),
			})
			return
		}
		conf.Routing.Rules = routingRules

		// Parse and update fallback if provided
		if requestBody.Routing.Fallback != "" {
			fallback, err := ParseFallback(requestBody.Routing.Fallback)
			if err != nil {
				httpServer.log.WithError(err).Errorln("Failed to parse fallback")
				json.NewEncoder(w).Encode(map[string]any{
					"status":  "error",
					"message": "Failed to parse fallback",
				})
				return
			}
			conf.Routing.Fallback = fallback
			httpServer.log.Infof("Fallback updated: %v", fallback)
		}
	}

	if requestBody.Nodes != nil {
		conf.Node = *requestBody.Nodes
	}

	if requestBody.Groups != nil {
		var newGroups []config.Group
		for _, groupReq := range *requestBody.Groups {
			if groupReq.Name == "" {
				httpServer.log.Errorln("Group name cannot be empty")
				json.NewEncoder(w).Encode(map[string]any{
					"status":  "error",
					"message": "Group name cannot be empty",
				})
				return
			}

			// Parse filter
			var filters [][]*config_parser.Function
			if groupReq.Filter != "" {
				parsedFilters, err := ParseGroupFilter(groupReq.Filter)
				if err != nil {
					httpServer.log.WithError(err).Errorln("Failed to parse group filter")
					json.NewEncoder(w).Encode(map[string]any{
						"status":  "error",
						"message": fmt.Sprintf("Failed to parse filter for group %s: %v", groupReq.Name, err),
					})
					return
				}
				filters = parsedFilters
			}

			// Parse policy
			policy, err := ParseGroupPolicy(groupReq.Policy)
			if err != nil {
				httpServer.log.WithError(err).Errorln("Failed to parse group policy")
				json.NewEncoder(w).Encode(map[string]any{
					"status":  "error",
					"message": fmt.Sprintf("Failed to parse policy for group %s: %v", groupReq.Name, err),
				})
				return
			}

			// Create new group
			newGroup := config.Group{
				Name:   groupReq.Name,
				Filter: filters,
				Policy: policy,
			}

			// Try to preserve optional fields from existing group if it exists
			for _, existingGroup := range conf.Group {
				if existingGroup.Name == groupReq.Name {
					newGroup.TcpCheckUrl = existingGroup.TcpCheckUrl
					newGroup.TcpCheckHttpMethod = existingGroup.TcpCheckHttpMethod
					newGroup.UdpCheckDns = existingGroup.UdpCheckDns
					newGroup.CheckInterval = existingGroup.CheckInterval
					newGroup.CheckTolerance = existingGroup.CheckTolerance
					break
				}
			}

			newGroups = append(newGroups, newGroup)
			httpServer.log.Infof("Group %s updated: filter=%v, policy=%v", groupReq.Name, groupReq.Filter, groupReq.Policy)
		}
		conf.Group = newGroups
	}

	// Save updated config to file and trigger reload to make it effective
	// Marshal the updated config
	configBytes, err := conf.Marshal(4)
	if err != nil {
		httpServer.log.WithError(err).Errorln("Failed to marshal config")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to marshal config",
		})
		return
	}

	// Save config to file
	if err := os.WriteFile(cfgFile, configBytes, 0644); err != nil {
		httpServer.log.WithError(err).Errorln("Failed to save config to file")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "error",
			"message": "Failed to save config to file",
		})
		return
	}

	// Trigger reload in background
	go _restart()
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"message": "Success",
	})
}

func startServer(log *logrus.Logger, conf *config.Config) {
	if httpServer != nil {
		httpServer.server.Shutdown(context.Background())
		httpServer = nil
	}
	addr := fmt.Sprintf("%s:%d", conf.Global.HttpListen, conf.Global.HttpPort)
	log.Infof("HTTP server addr: %s", addr)
	httpServer = &HttpServer{
		server: &http.Server{Addr: addr, Handler: logMiddleware(jsonMiddleware(router()))},
		log:    log,
	}

	if err := httpServer.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Errorf("Failed to start HTTP server: %v", err)
	}
}

// jsonMiddleware automatically sets the JSON Content-Type for the response
func jsonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpServer.log.Infof("request: method=%s, path=%s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /configs", updateConfigs)
	mux.HandleFunc("GET /configs", getConfigs)
	mux.HandleFunc("GET /version", getVersion)
	mux.HandleFunc("GET /restart", restart)
	mux.HandleFunc("PUT /nodes", updateNodes)
	mux.HandleFunc("GET /proxyConfig", getProxyConfig)
	mux.HandleFunc("PUT /proxyConfig", updateProxyConfig)
	return mux
}
