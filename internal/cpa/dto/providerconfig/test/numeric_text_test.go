package providerconfig_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"cpa-usage-keeper/internal/cpa/dto/providerconfig"
)

func TestProviderProjectionUsesExactNumericTextAndKeepsRawDocument(t *testing.T) {
	raw := `[{"name":{"not":"consumed"},"prefix":123,"base-url":456,"priority":5,"excluded-models":[9007199254740993,"MODEL-X",null],"headers":{"number":9007199254740993},"keys":[{"api-key":9007199254740993,"auth_index":"numeric","prefix":null,"name":789,"note":42,"excluded-models":null},{"api-key":"00123","auth_index":"string","prefix":"00123","excluded-models":[]}]}]`
	var document providerconfig.Document
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := document.ProviderKeys("")
	if err != nil {
		t.Fatal(err)
	}
	first, second := keys[0], keys[1]
	if first.APIKey != "9007199254740993" || first.Prefix != "123" || first.BaseURL != "456" || first.Name != "789" || first.Note == nil || *first.Note != "42" {
		t.Fatalf("numeric text = %#v", first)
	}
	if !reflect.DeepEqual(first.ExcludedModels, []string{"9007199254740993", "model-x"}) {
		t.Fatalf("inherited exclusions = %#v", first.ExcludedModels)
	}
	if second.APIKey != "00123" || second.Prefix != "00123" || len(second.ExcludedModels) != 0 {
		t.Fatalf("string overrides = %#v", second)
	}
	after, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || string(document.Groups[0].Keys[0].Fields["api-key"]) != "9007199254740993" {
		t.Fatalf("projection changed raw values: %s", after)
	}
	writable, err := json.Marshal(document.Writable())
	if err != nil {
		t.Fatal(err)
	}
	var written providerconfig.Document
	if err := json.Unmarshal(writable, &written); err != nil {
		t.Fatal(err)
	}
	if string(written.Groups[0].Keys[0].Fields["api-key"]) != "9007199254740993" || string(written.Groups[0].Fields["prefix"]) != "123" {
		t.Fatalf("write converted raw numbers: %s", writable)
	}
}

func TestOpenAIProjectionSeparatesNumericGroupAndMemberText(t *testing.T) {
	var document providerconfig.Document
	raw := `[{"name":9007199254740993,"prefix":123,"base-url":456,"note":0,"priority":3,"disabled":false,"keys":[{"api-key":9007199254740993,"auth_index":"numeric"},{"api-key":"00123","auth_index":"string"}]}]`
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(document)
	groups, err := document.OpenAIProviders()
	if err != nil {
		t.Fatal(err)
	}
	group := groups[0]
	if group.Name != "9007199254740993" || group.Prefix != "123" || group.BaseURL != "456" || group.Note == nil || *group.Note != "0" {
		t.Fatalf("group = %#v", group)
	}
	if group.APIKeyEntries[0].APIKey != "9007199254740993" || group.APIKeyEntries[1].APIKey != "00123" {
		t.Fatalf("members = %#v", group.APIKeyEntries)
	}
	after, _ := json.Marshal(document)
	if string(before) != string(after) || string(document.Groups[0].Fields["name"]) != "9007199254740993" {
		t.Fatalf("group raw values changed: %s", after)
	}
}

func TestTextProjectionRejectsWrongStructuresAndKeepsOtherTypesStrict(t *testing.T) {
	for _, body := range []string{
		`[{"prefix":true,"keys":[]}]`,
		`[{"prefix":{},"keys":[]}]`,
		`[{"keys":[{"api-key":[],"auth_index":"a"}]}]`,
		`[{"excluded-models":123,"keys":[]}]`,
		`[{"excluded-models":[{}],"keys":[]}]`,
		`[{"priority":"1","keys":[]}]`,
		`[{"keys":[{"api-key":"key","auth_index":123}]}]`,
	} {
		var document providerconfig.Document
		if err := json.Unmarshal([]byte(body), &document); err != nil {
			t.Fatal(err)
		}
		if _, err := document.ProviderKeys(""); err == nil {
			t.Fatalf("accepted invalid native projection: %s", body)
		}
	}
	for _, body := range []string{
		`[{"name":[],"keys":[]}]`,
		`[{"disabled":0,"keys":[]}]`,
		`[{"priority":"1","keys":[]}]`,
		`[{"keys":[{"api-key":{},"auth_index":"a"}]}]`,
		`[{"keys":[{"api-key":"key","auth_index":123}]}]`,
	} {
		var document providerconfig.Document
		if err := json.Unmarshal([]byte(body), &document); err != nil {
			t.Fatal(err)
		}
		if _, err := document.OpenAIProviders(); err == nil {
			t.Fatalf("accepted invalid OpenAI projection: %s", body)
		}
	}
}
