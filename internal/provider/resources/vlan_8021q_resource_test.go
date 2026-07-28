package resources

import "testing"

func TestValidateVLANName(t *testing.T) {
	tests := []struct {
		name     string
		vlanName string
		wantErr  bool
	}{
		{name: "empty", vlanName: "", wantErr: true},
		{name: "ten bytes", vlanName: "1234567890"},
		{name: "eleven bytes", vlanName: "12345678901", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVLANName(test.vlanName)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateVLANName(%q) error = %v, wantErr %t", test.vlanName, err, test.wantErr)
			}
		})
	}
}
