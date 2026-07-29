package providerdata

import (
	"sync"

	"github.com/lucavb/terraform-provider-tplink-easysmart/internal/client"
)

type Data struct {
	SwitchClient client.Client
	vlanTableMu  sync.Mutex
}

func (d *Data) Client() client.Client {
	return d.SwitchClient
}

// VLANTableLock serializes operations that read or mutate the shared VLAN
// table. The switch Web UI can otherwise apply requests against stale table
// state.
func (d *Data) VLANTableLock() *sync.Mutex {
	return &d.vlanTableMu
}
