package wolf

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionSeccompChangesOnlyFutexWaitv(t *testing.T) {
	base := []byte(`{
		"defaultAction":"SCMP_ACT_ERRNO", "defaultErrnoRet":38,
		"archMap":[{"architecture":"SCMP_ARCH_X86_64","subArchitectures":["SCMP_ARCH_X86"]}],
		"futureMetadata":{"revision":1234567890123456789},
		"syscalls":[
			{"names":["futex_waitv","keyctl"],"action":"SCMP_ACT_ERRNO","errnoRet":1},
			{"names":["futex_waitv"],"action":"SCMP_ACT_ALLOW","includes":{"caps":["CAP_SYS_ADMIN"]}},
			{"names":["futex_waitv","futex"],"action":"SCMP_ACT_ALLOW","args":[{"index":0,"value":42,"op":"SCMP_CMP_EQ"}]},
			{"names":["read","write"],"action":"SCMP_ACT_ALLOW","excludes":{"arches":["s390"]}}
		]}`)
	got, err := SessionSeccomp(base)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(base, &before)
	_ = json.Unmarshal(got, &after)
	for key, old := range before {
		if key == "syscalls" {
			continue
		}
		var a, b bytes.Buffer
		_ = json.Compact(&a, old)
		_ = json.Compact(&b, after[key])
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Errorf("changed profile field %s", key)
		}
	}
	var oldRules, newRules []map[string]json.RawMessage
	_ = json.Unmarshal(before["syscalls"], &oldRules)
	_ = json.Unmarshal(after["syscalls"], &newRules)
	if len(newRules) != 4 {
		t.Fatalf("unexpected rules: %s", got)
	}
	// Removing the conditional futex-only rule must leave every other rule's
	// conditions and error values intact, including the remaining futex rule.
	for i, oldIndex := range []int{0, 2, 3} {
		old := oldRules[oldIndex]
		for key, raw := range old {
			if key == "names" {
				continue
			}
			var a, b any
			_ = json.Unmarshal(raw, &a)
			_ = json.Unmarshal(newRules[i][key], &b)
			if !reflect.DeepEqual(a, b) {
				t.Errorf("changed rule %d field %s", oldIndex, key)
			}
		}
	}
	wantNames := []string{`["keyctl"]`, `["futex"]`, `["read","write"]`, `["futex_waitv"]`}
	for i, want := range wantNames {
		var compact bytes.Buffer
		_ = json.Compact(&compact, newRules[i]["names"])
		if compact.String() != want {
			t.Errorf("rule %d names %s", i, compact.String())
		}
	}
	if len(newRules[3]) != 2 || string(newRules[3]["action"]) != `"SCMP_ACT_ALLOW"` {
		t.Error("futex_waitv must have exactly one unconditional allow rule")
	}
	again, err := SessionSeccomp(got)
	if err != nil || !bytes.Equal(got, again) {
		t.Error("derivation must be idempotent")
	}
}

func TestSessionSeccompRejectsInvalidBase(t *testing.T) {
	for _, base := range []string{
		`null`, `{}`, `broken`,
		`{"defaultAction":"SCMP_ACT_ALLOW","syscalls":[]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO"}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[null]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["read"]}]}`,
		`{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":42,"action":"SCMP_ACT_ALLOW"}]}`,
	} {
		if _, err := SessionSeccomp([]byte(base)); err == nil {
			t.Errorf("accepted invalid base %s", base)
		}
	}
}
