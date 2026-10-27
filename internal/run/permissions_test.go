package run

import (
	"encoding/json"
	"testing"
)

func TestPermissionDefaultAndExplicitDenyRemainDistinct(t *testing.T) {
	for _, permissions := range []map[string]string{nil, {}, {"contents": "none"}, {"contents": "read"}} {
		data, err := json.Marshal(Plan{Permissions: permissions})
		if err != nil {
			t.Fatal(err)
		}
		var restored Plan
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if (restored.Permissions == nil) != (permissions == nil) || restored.Permissions["contents"] != permissions["contents"] {
			t.Fatalf("permissions changed after persistence: %s", data)
		}
	}
}
