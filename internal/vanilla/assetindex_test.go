package vanilla

import "testing"

func TestAssetIndexSpec(t *testing.T) {
	raw := []byte(`{
		"id": "1.20.1",
		"assetIndex": {
			"id": "5",
			"sha1": "0123456789abcdef0123456789abcdef01234567",
			"size": 12345,
			"totalSize": 999,
			"url": "https://example.com/5.json"
		}
	}`)
	spec, err := AssetIndexSpec(raw)
	if err != nil {
		t.Fatalf("AssetIndexSpec: %v", err)
	}
	if spec.ID != "5" || spec.Size != 12345 || spec.URL != "https://example.com/5.json" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.SHA1 != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("sha1 = %q", spec.SHA1)
	}
}

func TestAssetIndexSpecRejectsIncomplete(t *testing.T) {
	cases := map[string]string{
		"bad json":     `{`,
		"missing all":  `{"id":"1.20.1"}`,
		"missing url":  `{"id":"1.20.1","assetIndex":{"id":"5","sha1":"aa"}}`,
		"missing sha1": `{"id":"1.20.1","assetIndex":{"id":"5","url":"https://example.com/5.json"}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := AssetIndexSpec([]byte(raw)); err == nil {
				t.Fatal("不完整的 assetIndex 应报错")
			}
		})
	}
}
