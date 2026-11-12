// Command obscheck verifies the observability stack through its HTTP APIs and prints evidence:
//
//	obscheck -prom http://127.0.0.1:9090 -grafana http://127.0.0.1:3000 -dashboard monitoring/grafana/dashboards/geth.json
//
// It checks that Prometheus targets are UP, that every dashboard panel query returns data (directly from Prometheus
// and through Grafana's datasource proxy /api/ds/query), that alert rules loaded healthy, and that Grafana provisioned
// the datasource and the dashboard. Exit status 1 when any check fails. `-watch NAME` instead polls one alert and prints
// every state transition (inactive -> pending -> firing), which is how the controlled-failure evidence was recorded.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sergiiogg7/geth-lab/internal/obscheck"
)

var client = &http.Client{Timeout: 15 * time.Second}

func main() {
	var (
		prom     = flag.String("prom", "http://127.0.0.1:9090", "Prometheus base URL")
		graf     = flag.String("grafana", "http://127.0.0.1:3000", "Grafana base URL (empty = skip Grafana checks)")
		gUser    = flag.String("grafana-user", "admin", "Grafana user")
		gPass    = flag.String("grafana-pass", "lab-throwaway", "Grafana password")
		dash     = flag.String("dashboard", "monitoring/grafana/dashboards/geth.json", "dashboard JSON file")
		watch    = flag.String("watch", "", "alert name: poll it and print state transitions instead of running the checks")
		watchFor = flag.Duration("watch-for", 5*time.Minute, "how long -watch runs")
		watchEnd = flag.String("watch-until", "firing", "stop -watch when the alert reaches this state")
	)
	flag.Parse()
	if *watch != "" {
		os.Exit(watchAlert(*prom, *watch, *watchFor, *watchEnd))
	}
	failed := 0
	check := func(ok bool, format string, a ...any) {
		tag := "PASS"
		if !ok {
			tag = "FAIL"
			failed++
		}
		fmt.Printf("%s  %s\n", tag, fmt.Sprintf(format, a...))
	}

	fmt.Println("== Prometheus targets")
	var tg struct {
		Data struct {
			ActiveTargets []struct {
				Labels    map[string]string `json:"labels"`
				ScrapeURL string            `json:"scrapeUrl"`
				Health    string            `json:"health"`
				LastError string            `json:"lastError"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := getJSON(*prom+"/api/v1/targets", "", "", &tg); err != nil {
		check(false, "targets: %v", err)
	}
	check(len(tg.Data.ActiveTargets) > 0, "%d active target(s)", len(tg.Data.ActiveTargets))
	for _, t := range tg.Data.ActiveTargets {
		check(t.Health == "up", "job=%s instance=%s %s health=%s %s", t.Labels["job"], t.Labels["instance"], t.ScrapeURL, t.Health, t.LastError)
	}

	raw, err := os.ReadFile(*dash)
	if err != nil {
		fmt.Println("FAIL ", err)
		os.Exit(1)
	}
	queries, err := obscheck.Queries(raw)
	check(err == nil && len(queries) > 0, "dashboard %s: %d panel queries", *dash, len(queries))

	fmt.Println("== Dashboard queries straight from Prometheus")
	for _, q := range queries {
		body, err := get(*prom+"/api/v1/query?query="+url.QueryEscape(q.Expr), "", "")
		if err != nil {
			check(false, "%-45s %s: %v", q.Panel, q.Expr, err)
			continue
		}
		r, err := obscheck.ParsePromQuery(body)
		check(err == nil && r.Status == "success" && r.Series > 0, "%-45s %-48s series=%d first=%s %s", trunc(q.Panel, 45), q.Expr, r.Series, r.First, r.Err)
	}

	fmt.Println("== Alert rules")
	body, err := get(*prom+"/api/v1/rules", "", "")
	if err != nil {
		check(false, "rules: %v", err)
	} else {
		rules, _ := obscheck.ParseRules(body)
		check(len(rules) > 0, "%d alerting rules loaded", len(rules))
		for _, r := range rules {
			check(r.Health == "ok", "%-28s health=%s state=%s active=%d", r.Name, r.Health, r.State, r.Active)
		}
	}

	if *graf != "" {
		fmt.Println("== Grafana")
		var h struct{ Version, Database string }
		if err := getJSON(*graf+"/api/health", "", "", &h); err != nil {
			check(false, "health: %v", err)
		} else {
			check(h.Database == "ok", "Grafana %s database=%s", h.Version, h.Database)
		}
		var ds []struct{ Name, UID, Type, URL string }
		if err := getJSON(*graf+"/api/datasources", *gUser, *gPass, &ds); err != nil {
			check(false, "datasources: %v", err)
		}
		prov := false
		for _, d := range ds {
			if d.UID == "prometheus" {
				prov = true
				check(true, "datasource provisioned: name=%s uid=%s type=%s url=%s", d.Name, d.UID, d.Type, d.URL)
			}
		}
		check(prov, "datasource uid=prometheus present")
		var sr []struct{ Title, UID, FolderTitle string }
		if err := getJSON(*graf+"/api/search?query=geth", *gUser, *gPass, &sr); err != nil {
			check(false, "search: %v", err)
		}
		found := false
		for _, s := range sr {
			if s.UID == "geth-lab-overview" {
				found = true
				check(true, "dashboard provisioned: %q uid=%s folder=%s", s.Title, s.UID, s.FolderTitle)
			}
		}
		check(found, "dashboard uid=geth-lab-overview present")

		fmt.Println("== Dashboard queries through Grafana (/api/ds/query)")
		for _, q := range queries {
			payload, _ := json.Marshal(map[string]any{
				"from": "now-5m", "to": "now",
				"queries": []any{map[string]any{
					"refId": "A", "datasource": map[string]string{"type": "prometheus", "uid": q.DSUID},
					"expr": q.Expr, "instant": true, "range": false,
				}},
			})
			resp, err := post(*graf+"/api/ds/query", *gUser, *gPass, payload)
			if err != nil {
				check(false, "%-45s %s: %v", q.Panel, q.Expr, err)
				continue
			}
			n, err := obscheck.GrafanaFrames(resp, "A")
			check(err == nil && n > 0, "%-45s %-48s frames=%d %s", trunc(q.Panel, 45), q.Expr, n, errStr(err))
		}
	}
	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d check(s) FAILED\n", failed)
		os.Exit(1)
	}
	fmt.Println("all checks passed")
}

func watchAlert(prom, name string, dur time.Duration, until string) int {
	deadline := time.Now().Add(dur)
	last := "?"
	for time.Now().Before(deadline) {
		body, err := get(prom+"/api/v1/rules", "", "")
		state := "unreachable"
		if err == nil {
			rules, _ := obscheck.ParseRules(body)
			state = "not-found"
			for _, r := range rules {
				if r.Name == name {
					state = r.State
				}
			}
		}
		if state != last {
			fmt.Printf("%s  alert %s: %s -> %s\n", time.Now().UTC().Format("15:04:05"), name, last, state)
			last = state
		}
		if state == until {
			return 0
		}
		time.Sleep(time.Second)
	}
	fmt.Printf("timeout: alert %s ended in state %s, wanted %s\n", name, last, until)
	return 1
}

func get(u, user, pass string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return do(req, user, pass)
}

func post(u, user, pass string, payload []byte) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	return do(req, user, pass)
}

func do(req *http.Request, user, pass string) ([]byte, error) {
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return b, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trunc(strings.TrimSpace(string(b)), 200))
	}
	return b, nil
}

func getJSON(u, user, pass string, v any) error {
	b, err := get(u, user, pass)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
