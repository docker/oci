package ocidocker

import "testing"

func TestHostsSorted(t *testing.T) {
	sorted := DockerHubHostsSorted()
	for i, host := range sorted {
		if i != int(DockerHubHosts[host]) {
			t.Errorf("host %s not sorted", host)
		}
	}
}
