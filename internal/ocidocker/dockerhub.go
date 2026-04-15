package ocidocker

import (
	"sync"
)

var (
	// DockerHubHosts is a map of hostnames associated with Docker Hub's registry. The number is a priority ranking
	// used to determine the order we will search for auth credentials
	DockerHubHosts = map[string]byte{
		"docker.io":               0,
		"index.docker.io":         1,
		"registry-1.docker.io":    2,
		"registry.hub.docker.com": 3,
	}
	// DockerHubHostsSorted is a slice of hostnames associated with Docker Hub's registry, sorted by their ranking
	DockerHubHostsSorted = sync.OnceValue(func() []string {
		sorted := make([]string, len(DockerHubHosts))
		for host, rank := range DockerHubHosts {
			sorted[rank] = host
		}
		return sorted
	})
)
