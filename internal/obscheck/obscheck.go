// Package obscheck holds the pure parts of the observability verifier: reading the queries out of a Grafana
// dashboard JSON file and interpreting Prometheus / Grafana API responses. The HTTP calls live in cmd/obscheck.
package obscheck

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Query is one panel query of the dashboard.
type Query struct {
	Panel  string
	Legend string
	Expr   string
	DSUID  string
}

type dashboard struct {
	Panels []struct {
		Title   string `json:"title"`
		Targets []struct {
			Expr         string `json:"expr"`
			LegendFormat string `json:"legendFormat"`
			Datasource   struct {
				UID string `json:"uid"`
			} `json:"datasource"`
		} `json:"targets"`
	} `json:"panels"`
}

// Queries extracts every Prometheus expression of every panel, in order.
func Queries(dashboardJSON []byte) ([]Query, error) {
	var d dashboard
	if err := json.Unmarshal(dashboardJSON, &d); err != nil {
		return nil, fmt.Errorf("dashboard json: %w", err)
	}
	var out []Query
	for _, p := range d.Panels {
		for _, t := range p.Targets {
			if strings.TrimSpace(t.Expr) == "" {
				continue
			}
			out = append(out, Query{Panel: p.Title, Legend: t.LegendFormat, Expr: t.Expr, DSUID: t.Datasource.UID})
		}
	}
	return out, nil
}

// PromResult summarises a Prometheus /api/v1/query response.
type PromResult struct {
	Status string
	Series int
	First  string // first sample value, as text
	Err    string
}

// ParsePromQuery reads a Prometheus instant-query response.
func ParsePromQuery(body []byte) (PromResult, error) {
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return PromResult{}, err
	}
	res := PromResult{Status: r.Status, Series: len(r.Data.Result), Err: r.Error}
	if len(r.Data.Result) > 0 && len(r.Data.Result[0].Value) == 2 {
		res.First = fmt.Sprint(r.Data.Result[0].Value[1])
	}
	return res, nil
}

// GrafanaFrames counts the data frames with at least one value in a Grafana /api/ds/query response for refID.
func GrafanaFrames(body []byte, refID string) (int, error) {
	var r struct {
		Results map[string]struct {
			Error  string `json:"error"`
			Frames []struct {
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, err
	}
	res, ok := r.Results[refID]
	if !ok {
		return 0, fmt.Errorf("no result for refId %s", refID)
	}
	if res.Error != "" {
		return 0, fmt.Errorf("%s", res.Error)
	}
	frames := 0
	for _, f := range res.Frames {
		if len(f.Data.Values) > 0 && len(f.Data.Values[0]) > 0 {
			frames++
		}
	}
	return frames, nil
}

// Rule is an alert rule as reported by /api/v1/rules.
type Rule struct {
	Name   string
	State  string // inactive | pending | firing
	Health string // ok | err | unknown
	Active int    // alert instances currently pending or firing
}

// ParseRules lists alerting rules from a /api/v1/rules response.
func ParseRules(body []byte) ([]Rule, error) {
	var r struct {
		Data struct {
			Groups []struct {
				Rules []struct {
					Name   string `json:"name"`
					Type   string `json:"type"`
					State  string `json:"state"`
					Health string `json:"health"`
					Alerts []any  `json:"alerts"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	var out []Rule
	for _, g := range r.Data.Groups {
		for _, x := range g.Rules {
			if x.Type == "alerting" {
				out = append(out, Rule{Name: x.Name, State: x.State, Health: x.Health, Active: len(x.Alerts)})
			}
		}
	}
	return out, nil
}
