package hello

import (
	"context"
	"github.com/influxdata/telegraf/agent"
	"github.com/influxdata/telegraf/config"
	_ "github.com/influxdata/telegraf/plugins/inputs/file"
	_ "github.com/influxdata/telegraf/plugins/inputs/mqtt_consumer"
	_ "github.com/influxdata/telegraf/plugins/outputs/file"
	_ "github.com/influxdata/telegraf/plugins/parsers/json_v2"
	_ "github.com/influxdata/telegraf/plugins/processors/thingsboard"
	_ "github.com/influxdata/telegraf/plugins/serializers/json"
	"github.com/stretchr/testify/require"
	"testing"
)

type ThingsBoardShim struct {
	agent *agent.Agent
}

func TestAgent(t *testing.T) {
	cfg := config.NewConfig()
	require.NoError(t, cfg.LoadConfig("telegraf.conf"))

	ag := agent.NewAgent(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := ag.Run(ctx)
	require.NoError(t, err)
}
