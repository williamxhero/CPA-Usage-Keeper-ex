package cpaapikeys_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"cpa-usage-keeper/internal/cpa/dto/cpaapikeys"
)

func TestManagementAPIKeysPreservesStringsAndExactNumericText(t *testing.T) {
	var response cpaapikeys.ManagementAPIKeysResponse
	if err := json.Unmarshal([]byte(`[9007199254740993,"00123",1.25,1e+9,null]`), &response); err != nil {
		t.Fatal(err)
	}
	want := []string{"9007199254740993", "00123", "1.25", "1e+9", ""}
	if !reflect.DeepEqual(response.APIKeys, want) {
		t.Fatalf("keys = %#v, want %#v", response.APIKeys, want)
	}
}

func TestManagementAPIKeysRejectsWrongStructures(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[true]`, `[{}]`, `[[]]`} {
		var response cpaapikeys.ManagementAPIKeysResponse
		if err := json.Unmarshal([]byte(raw), &response); err == nil {
			t.Fatalf("accepted invalid client keys: %s", raw)
		}
	}
}
