//go:generate ../../../tools/readme_config_includer/generator
package thingsboard

import (
	_ "embed"
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/metric"
	"github.com/influxdata/telegraf/plugins/processors"
)

//go:embed sample.conf
var sampleConfig string

type Split struct {
	Attributes []string `toml:"attributes"`

	attrMap map[string]struct{}
}

func (*Split) SampleConfig() string {
	return sampleConfig
}

func (s *Split) Init() error {
	s.attrMap = make(map[string]struct{})
	for _, attr := range s.Attributes {
		s.attrMap[attr] = struct{}{}
	}
	return nil
}

func (s *Split) Apply(in ...telegraf.Metric) []telegraf.Metric {
	newMetrics := make([]telegraf.Metric, 0, len(in)*2)

	for _, point := range in {
		point.Accept()

		attributes := make(map[string]any, len(point.FieldList()))
		telemetries := make(map[string]any, len(point.FieldList()))
		for _, field := range point.FieldList() {
			if _, ok := s.attrMap[field.Key]; ok {
				attributes[field.Key] = field.Value
			} else {
				telemetries[field.Key] = field.Value
			}
		}

		if len(attributes) > 0 {
			m := metric.New("___thingsboard_attribute", point.Tags(), attributes, point.Time())
			newMetrics = append(newMetrics, m)
		}

		if len(telemetries) > 0 {
			m := metric.New("___thingsboard_telemetry", point.Tags(), telemetries, point.Time())
			newMetrics = append(newMetrics, m)
		}
	}

	return newMetrics
}

func init() {
	processors.Add("thingsboard", func() telegraf.Processor {
		return &Split{}
	})
}
