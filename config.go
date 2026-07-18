package main

import "github.com/Tannex/cq/internal/appconfig"

var configLoader = appconfig.DefaultLoader(debugLog.Printf)

type config = appconfig.Config

func loadConfig() (config, error) {
	return configLoader.Load()
}

func defaultConfigFile() (string, error) {
	return configLoader.File()
}
