package ipam

import "testing"

func TestValidatePool(t *testing.T) {
	valid := Pool{Name: "apps", CIDR: "192.0.2.0/24", StartIP: "192.0.2.10", EndIP: "192.0.2.20"}
	if err := ValidatePool(valid); err != nil {
		t.Fatal(err)
	}
	valid.StartIP = "192.0.2.0"
	if err := ValidatePool(valid); err == nil {
		t.Fatal("network address accepted")
	}
}
