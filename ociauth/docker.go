package ociauth

import (
	"github.com/docker/oci/internal/ocidocker"
)

// DockerWrapper is an ociauth.Config implementation that wraps an underlying ociauth.Config in order to check
// credentials for every Docker Hub domain when credentials for any Hub domain are requested.
type dockerWrapper struct {
	Config
}

// EntryForRegistry returns credentials for a given host.
func (w dockerWrapper) EntryForRegistry(host string) (ConfigEntry, error) {
	var zero ConfigEntry // "EntryForRegistry" doesn't return an error on a miss - it just returns an empty object (so we create this to have something to trivially compare against for our fallback)
	if entry, err := w.Config.EntryForRegistry(host); err == nil && entry != zero {
		return entry, err
	} else if _, ok := ocidocker.DockerHubHosts[host]; ok {
		for _, dockerHubHost := range ocidocker.DockerHubHostsSorted() {
			if dockerHubHost == "" {
				continue
			}
			if entry, err = w.Config.EntryForRegistry(dockerHubHost); err == nil && entry != zero {
				return entry, err
			}
		}
		return entry, err
	} else {
		return entry, err
	}
}
