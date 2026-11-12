package obscheck

import "testing"

func TestQueries(t *testing.T) {
	js := `{"panels":[{"title":"A","targets":[{"expr":"up","legendFormat":"u","datasource":{"uid":"prometheus"}},{"expr":" "}]},{"title":"B","targets":[{"expr":"rate(x[1m])"}]}]}`
	q, err := Queries([]byte(js))
	if err != nil || len(q) != 2 {
		t.Fatalf("got %v %v", q, err)
	}
	if q[0].Panel != "A" || q[0].DSUID != "prometheus" || q[1].Expr != "rate(x[1m])" {
		t.Fatalf("unexpected %+v", q)
	}
}

func TestParsePromQuery(t *testing.T) {
	r, err := ParsePromQuery([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1.0,"42"]}]}}`))
	if err != nil || r.Series != 1 || r.First != "42" {
		t.Fatalf("got %+v %v", r, err)
	}
	r, _ = ParsePromQuery([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	if r.Series != 0 {
		t.Fatalf("empty result should have 0 series: %+v", r)
	}
}

func TestGrafanaFrames(t *testing.T) {
	ok := `{"results":{"A":{"frames":[{"data":{"values":[[1],[2]]}},{"data":{"values":[[]]}}]}}}`
	n, err := GrafanaFrames([]byte(ok), "A")
	if err != nil || n != 1 {
		t.Fatalf("got %d %v", n, err)
	}
	if _, err := GrafanaFrames([]byte(`{"results":{"A":{"error":"boom"}}}`), "A"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseRules(t *testing.T) {
	js := `{"data":{"groups":[{"rules":[{"name":"X","type":"alerting","state":"firing","health":"ok","alerts":[1,2]},{"name":"rec","type":"recording"}]}]}}`
	r, err := ParseRules([]byte(js))
	if err != nil || len(r) != 1 || r[0].Name != "X" || r[0].State != "firing" || r[0].Active != 2 {
		t.Fatalf("got %+v %v", r, err)
	}
}
