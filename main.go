package main

import (
	goPlugin "github.com/hashicorp/go-plugin"
	log "github.com/sirupsen/logrus"

	"github.com/argoproj/argo-rollouts/metricproviders/plugin/rpc"
	"github.com/argoproj/argo-rollouts/utils/plugin/types"

	"github.com/argoproj-labs/rollouts-plugin-metric-instana/internal/plugin"
)

// handshakeConfig must match exactly what the Argo Rollouts controller expects
var handshakeConfig = goPlugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ARGO_ROLLOUTS_RPC_PLUGIN",
	MagicCookieValue: "metricprovider",
}

func main() {
	logCtx := *log.WithFields(log.Fields{"plugin": plugin.PluginName})

	// Wire up our implementation
	rpcPluginImpl := &plugin.RpcPlugin{
		LogCtx: logCtx,
	}

	// Verify interface is satisfied at compile time
	var _ types.RpcMetricProvider = rpcPluginImpl

	var pluginMap = map[string]goPlugin.Plugin{
		"RpcMetricProviderPlugin": &rpc.RpcMetricProviderPlugin{Impl: rpcPluginImpl},
	}

	logCtx.Infof("Starting %s plugin", plugin.PluginName)

	goPlugin.Serve(&goPlugin.ServeConfig{
		HandshakeConfig: handshakeConfig,
		Plugins:         pluginMap,
	})
}
