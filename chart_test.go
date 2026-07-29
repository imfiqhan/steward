package steward

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChartPayloadShape(t *testing.T) {
	cd := &ChartData{
		Type:   ChartBar,
		Labels: []string{"Jan", "Feb"},
		Series: []ChartSeries{
			{Label: "Desktop", Values: []float64{186, 305}},
			{Label: "Mobile", Values: []float64{80, 200}, Color: "#ff0000"},
		},
		Legend: true,
	}
	p, err := cd.payload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}

	if p.Type != "bar" || p.LabelKey != "label" || !p.Legend {
		t.Errorf("payload header = %+v", p)
	}
	// Column-oriented input becomes one row per label.
	if len(p.Data) != 2 {
		t.Fatalf("got %d rows, want 2", len(p.Data))
	}
	if p.Data[0]["label"] != "Jan" || p.Data[0]["s1"] != 186.0 || p.Data[0]["s2"] != 80.0 {
		t.Errorf("row 0 = %+v", p.Data[0])
	}
	if p.Data[1]["label"] != "Feb" || p.Data[1]["s1"] != 305.0 {
		t.Errorf("row 1 = %+v", p.Data[1])
	}

	// An explicit colour wins; an empty one takes the theme palette.
	s1 := p.Series["s1"].(map[string]any)
	if s1["color"] != "var(--chart-1)" {
		t.Errorf("series 1 colour = %v, want the first palette entry", s1["color"])
	}
	s2 := p.Series["s2"].(map[string]any)
	if s2["color"] != "#ff0000" {
		t.Errorf("series 2 colour = %v, want the explicit value", s2["color"])
	}
	if s1["label"] != "Desktop" {
		t.Errorf("series 1 label = %v", s1["label"])
	}
}

func TestChartPayloadRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		cd   *ChartData
		want string
	}{
		{"no labels", &ChartData{Series: []ChartSeries{{Label: "a", Values: []float64{1}}}}, "Labels is empty"},
		{"no series", &ChartData{Labels: []string{"Jan"}}, "no Series"},
		{
			"length mismatch",
			&ChartData{Labels: []string{"Jan", "Feb"}, Series: []ChartSeries{{Label: "a", Values: []float64{1}}}},
			"want 2 to match Labels",
		},
		{
			"duplicate key",
			&ChartData{Labels: []string{"Jan"}, Series: []ChartSeries{
				{Key: "x", Label: "a", Values: []float64{1}},
				{Key: "x", Label: "b", Values: []float64{2}},
			}},
			"duplicate series key",
		},
		{
			"key collides with label key",
			&ChartData{Labels: []string{"Jan"}, Series: []ChartSeries{
				{Key: "label", Label: "a", Values: []float64{1}},
			}},
			"collides with the label key",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.cd.payload()
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestChartStackedAddsScaleOptions(t *testing.T) {
	cd := &ChartData{
		Labels:  []string{"Jan"},
		Series:  []ChartSeries{{Label: "a", Values: []float64{1}}},
		Stacked: true,
	}
	p, err := cd.payload()
	if err != nil {
		t.Fatal(err)
	}
	scales, ok := p.Options["scales"].(map[string]any)
	if !ok {
		t.Fatalf("no scales in options: %+v", p.Options)
	}
	for _, axis := range []string{"x", "y"} {
		a := scales[axis].(map[string]any)
		if a["stacked"] != true {
			t.Errorf("axis %s not stacked", axis)
		}
	}
}

// A label cannot terminate the surrounding script element: json.Marshal escapes
// <, > and & before the payload is ever embedded.
func TestChartJSONEscapesScriptTerminator(t *testing.T) {
	cd := &ChartData{
		Labels: []string{`</script><img src=x onerror=alert(1)>`},
		Series: []ChartSeries{{Label: "a", Values: []float64{1}}},
	}
	raw, err := cd.json()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "</script>") {
		t.Errorf("payload carries a literal script terminator: %s", raw)
	}
	// Still valid JSON, and the label survives intact once parsed.
	var back chartPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if back.Data[0]["label"] != `</script><img src=x onerror=alert(1)>` {
		t.Errorf("label did not round-trip: %v", back.Data[0]["label"])
	}
}

func TestChartDefaultsToBar(t *testing.T) {
	cd := &ChartData{Labels: []string{"a"}, Series: []ChartSeries{{Label: "s", Values: []float64{1}}}}
	p, err := cd.payload()
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != "bar" {
		t.Errorf("default type = %q, want bar", p.Type)
	}
}
