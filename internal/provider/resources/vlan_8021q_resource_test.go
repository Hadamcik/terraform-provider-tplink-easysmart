package resources

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/lucavb/terraform-provider-tplink-easysmart/internal/client/model"
	"github.com/lucavb/terraform-provider-tplink-easysmart/internal/providerdata"
)

func TestVLANValidateConfigRejectsUnsupportedNames(t *testing.T) {
	vlanResource := &vlan8021qResource{}
	tests := []struct {
		name     string
		vlanName string
		wantErr  bool
	}{
		{name: "empty", vlanName: "", wantErr: true},
		{name: "ten ASCII bytes", vlanName: "1234567890"},
		{name: "eleven ASCII bytes", vlanName: "12345678901", wantErr: true},
		{name: "five multibyte characters", vlanName: strings.Repeat("é", 5)},
		{name: "six multibyte characters", vlanName: strings.Repeat("é", 6), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := resource.ValidateConfigResponse{}
			vlanResource.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
				Config: vlanConfig(t, vlanResource, test.vlanName),
			}, &response)

			if got := response.Diagnostics.HasError(); got != test.wantErr {
				t.Fatalf("ValidateConfig(%q) error = %t, want %t: %v", test.vlanName, got, test.wantErr, response.Diagnostics)
			}
		})
	}
}

func TestVLANResourcesShareTableLock(t *testing.T) {
	ctx := context.Background()
	providerData := &providerdata.Data{}
	first := &vlan8021qResource{}
	second := &vlan8021qResource{}

	for _, vlanResource := range []*vlan8021qResource{first, second} {
		response := resource.ConfigureResponse{}
		vlanResource.Configure(ctx, resource.ConfigureRequest{ProviderData: providerData}, &response)
		if response.Diagnostics.HasError() {
			t.Fatalf("Configure() diagnostics: %v", response.Diagnostics)
		}
	}

	if first.vlanTableMu == nil || second.vlanTableMu == nil || first.vlanTableMu != second.vlanTableMu {
		t.Fatal("configured VLAN resources must share one VLAN table lock")
	}

	first.lockVLANTable()
	acquired := make(chan struct{})
	go func() {
		second.lockVLANTable()
		close(acquired)
		second.unlockVLANTable()
	}()

	select {
	case <-acquired:
		t.Fatal("second VLAN resource acquired the lock while the first held it")
	case <-time.After(50 * time.Millisecond):
	}

	first.unlockVLANTable()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second VLAN resource did not acquire the lock after release")
	}
}

func TestPVIDAndVLANResourcesShareTableLock(t *testing.T) {
	ctx := context.Background()
	providerData := &providerdata.Data{}
	vlanResource := &vlan8021qResource{}
	pvidResource := &portPVIDResource{}

	vlanResponse := resource.ConfigureResponse{}
	vlanResource.Configure(ctx, resource.ConfigureRequest{ProviderData: providerData}, &vlanResponse)
	pvidResponse := resource.ConfigureResponse{}
	pvidResource.Configure(ctx, resource.ConfigureRequest{ProviderData: providerData}, &pvidResponse)
	if vlanResponse.Diagnostics.HasError() || pvidResponse.Diagnostics.HasError() {
		t.Fatalf("Configure() diagnostics: VLAN=%v PVID=%v", vlanResponse.Diagnostics, pvidResponse.Diagnostics)
	}

	if vlanResource.vlanTableMu == nil || pvidResource.vlanTableMu == nil || vlanResource.vlanTableMu != pvidResource.vlanTableMu {
		t.Fatal("VLAN and PVID resources must share one VLAN table lock")
	}

	vlanResource.lockVLANTable()
	acquired := make(chan struct{})
	go func() {
		pvidResource.lockVLANTable()
		close(acquired)
		pvidResource.unlockVLANTable()
	}()

	select {
	case <-acquired:
		t.Fatal("PVID resource acquired the VLAN table lock while the VLAN resource held it")
	case <-time.After(50 * time.Millisecond):
	}

	vlanResource.unlockVLANTable()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("PVID resource did not acquire the VLAN table lock after release")
	}
}

func TestPVIDApplyWaitsForVLANTableLock(t *testing.T) {
	ctx := context.Background()
	providerData := &providerdata.Data{}
	vlanResource := &vlan8021qResource{}
	pvidResource := &portPVIDResource{}

	vlanResponse := resource.ConfigureResponse{}
	vlanResource.Configure(ctx, resource.ConfigureRequest{ProviderData: providerData}, &vlanResponse)
	pvidResponse := resource.ConfigureResponse{}
	pvidResource.Configure(ctx, resource.ConfigureRequest{ProviderData: providerData}, &pvidResponse)
	if vlanResponse.Diagnostics.HasError() || pvidResponse.Diagnostics.HasError() {
		t.Fatalf("Configure() diagnostics: VLAN=%v PVID=%v", vlanResponse.Diagnostics, pvidResponse.Diagnostics)
	}

	fakeClient := &blockingPVIDClient{
		vlanReadStarted: make(chan struct{}),
		allowVLANRead:   make(chan struct{}),
	}
	pvidResource.client = fakeClient

	vlanResource.lockVLANTable()
	result := make(chan pvidApplyResult, 1)
	go func() {
		var diagnostics diag.Diagnostics
		_, ok := pvidResource.apply(ctx, portPVIDResourceModel{
			PortID: types.Int64Value(1),
			PVID:   types.Int64Value(20),
		}, &diagnostics)
		result <- pvidApplyResult{ok: ok, diagnostics: diagnostics}
	}()

	select {
	case <-fakeClient.vlanReadStarted:
		t.Fatal("PVID apply read the VLAN table while a VLAN operation held the lock")
	case <-time.After(50 * time.Millisecond):
	}

	vlanResource.unlockVLANTable()

	select {
	case <-fakeClient.vlanReadStarted:
	case <-time.After(time.Second):
		t.Fatal("PVID apply did not read the VLAN table after the lock was released")
	}
	close(fakeClient.allowVLANRead)

	select {
	case result := <-result:
		if !result.ok || result.diagnostics.HasError() {
			t.Fatalf("PVID apply failed: ok=%t diagnostics=%v", result.ok, result.diagnostics)
		}
	case <-time.After(time.Second):
		t.Fatal("PVID apply did not complete")
	}
}

type pvidApplyResult struct {
	ok          bool
	diagnostics diag.Diagnostics
}

type blockingPVIDClient struct {
	vlanReadStarted chan struct{}
	allowVLANRead   chan struct{}
}

func (c *blockingPVIDClient) GetVLANs(context.Context) (model.VLANTable, error) {
	close(c.vlanReadStarted)
	<-c.allowVLANRead
	return model.VLANTable{VLANs: []model.VLAN{{ID: 20}}}, nil
}

func (c *blockingPVIDClient) SetPortPVID(context.Context, int, int) error {
	return nil
}

func (c *blockingPVIDClient) GetPVIDs(context.Context) ([]model.PortPVID, error) {
	return []model.PortPVID{{PortID: 1, PVID: 20}}, nil
}

func vlanConfig(t *testing.T, vlanResource *vlan8021qResource, name string) tfsdk.Config {
	t.Helper()

	schemaResponse := resource.SchemaResponse{}
	vlanResource.Schema(context.Background(), resource.SchemaRequest{}, &schemaResponse)

	portSetType := tftypes.Set{ElementType: tftypes.Number}
	configType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"id":             tftypes.String,
		"vlan_id":        tftypes.Number,
		"name":           tftypes.String,
		"tagged_ports":   portSetType,
		"untagged_ports": portSetType,
	}}

	return tfsdk.Config{
		Raw: tftypes.NewValue(configType, map[string]tftypes.Value{
			"id":             tftypes.NewValue(tftypes.String, nil),
			"vlan_id":        tftypes.NewValue(tftypes.Number, big.NewFloat(20)),
			"name":           tftypes.NewValue(tftypes.String, name),
			"tagged_ports":   tftypes.NewValue(portSetType, []tftypes.Value{}),
			"untagged_ports": tftypes.NewValue(portSetType, []tftypes.Value{}),
		}),
		Schema: schemaResponse.Schema,
	}
}
