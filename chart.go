package steward

// Chart widgets, rendered by Basecoat's Chart component (Chart.js under the
// hood) so they inherit the panel's --chart-N theme colours, tooltips, and
// legend styling rather than inventing a second visual language.
//
// The Go API is column-oriented and typed: you hand over Labels plus one
// ChartSeries per line or bar set. Converting that into the row-shaped payload
// Basecoat wants happens here, so no map[string]any reaches calling code.

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ChartType selects the Chart.js chart type.
type ChartType string

const (
	ChartBar      ChartType = "bar"
	ChartLine     ChartType = "line"
	ChartPie      ChartType = "pie"
	ChartDoughnut ChartType = "doughnut"
	ChartRadar    ChartType = "radar"
)

// chartPalette cycles the theme's chart colours, so a series without an
// explicit Color still matches the rest of the panel.
var chartPalette = []string{
	"var(--chart-1)", "var(--chart-2)", "var(--chart-3)",
	"var(--chart-4)", "var(--chart-5)",
}

// ChartSeries is one set of values plotted against ChartData.Labels.
type ChartSeries struct {
	// Label names the series in the legend and tooltip.
	Label string
	// Values must be the same length as ChartData.Labels.
	Values []float64
	// Color is any CSS colour. Empty takes the next palette entry.
	Color string
	// Fill shades the area under a line series.
	Fill bool
	// Key overrides the payload key for this series. Empty derives one, which
	// is what you normally want.
	Key string
}

// ChartData is a chart's full description.
type ChartData struct {
	Type   ChartType
	Labels []string
	Series []ChartSeries
	// Legend draws Basecoat's generated legend beneath the canvas.
	Legend bool
	// Stacked stacks bar series on both axes.
	Stacked bool
}

// chartPayload mirrors the object basecoat.chart() expects.
type chartPayload struct {
	Type     string           `json:"type"`
	LabelKey string           `json:"labelKey"`
	Data     []map[string]any `json:"data"`
	Series   map[string]any   `json:"series"`
	Legend   bool             `json:"legend"`
	Options  map[string]any   `json:"options,omitempty"`
}

// labelKey is the row key holding each point's label. Fixed rather than
// configurable, because callers give labels as a separate slice.
const chartLabelKey = "label"

// errChartNoData distinguishes "nothing to plot" from a malformed chart, so the
// dashboard can show an empty state for the former and report a bug for the
// latter.
var errChartNoData = errors.New("chart: no data")

// payload validates the chart and converts it to Basecoat's row-oriented shape.
func (cd *ChartData) payload() (*chartPayload, error) {
	// An empty result set is a normal state, not a caller error: a fresh install
	// or a filtered-out period genuinely has nothing to plot. It is reported as
	// "no data" by the widget rather than as a failure, so an empty table does
	// not look like a broken query.
	if len(cd.Labels) == 0 {
		return nil, errChartNoData
	}
	if len(cd.Series) == 0 {
		return nil, errChartNoData
	}

	typ := cd.Type
	if typ == "" {
		typ = ChartBar
	}

	out := &chartPayload{
		Type:     string(typ),
		LabelKey: chartLabelKey,
		Legend:   cd.Legend,
		Series:   map[string]any{},
		Data:     make([]map[string]any, len(cd.Labels)),
	}
	for i, l := range cd.Labels {
		out.Data[i] = map[string]any{chartLabelKey: l}
	}

	seen := map[string]bool{}
	for i, s := range cd.Series {
		if len(s.Values) != len(cd.Labels) {
			return nil, fmt.Errorf("chart: series %q has %d values, want %d to match Labels",
				s.Label, len(s.Values), len(cd.Labels))
		}
		key := s.Key
		if key == "" {
			key = fmt.Sprintf("s%d", i+1)
		}
		if key == chartLabelKey {
			return nil, fmt.Errorf("chart: series key %q collides with the label key", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("chart: duplicate series key %q", key)
		}
		seen[key] = true

		colour := s.Color
		if colour == "" {
			colour = chartPalette[i%len(chartPalette)]
		}
		cfg := map[string]any{"label": s.Label, "color": colour}
		if s.Fill {
			cfg["surface"] = "gradient"
			cfg["dataset"] = map[string]any{"fill": true}
		}
		out.Series[key] = cfg

		for j, v := range s.Values {
			out.Data[j][key] = v
		}
	}

	if cd.Stacked {
		out.Options = map[string]any{
			"scales": map[string]any{
				"x": map[string]any{"stacked": true},
				"y": map[string]any{"stacked": true},
			},
		}
	}
	return out, nil
}

// json renders the payload for embedding in a script block. json.Marshal
// escapes <, > and & to \u00XX, so a label containing "</script>" cannot break
// out of the surrounding element.
func (cd *ChartData) json() ([]byte, error) {
	p, err := cd.payload()
	if err != nil {
		return nil, err
	}
	return json.Marshal(p)
}
